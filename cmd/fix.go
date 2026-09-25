package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/jy-eggroll/flk/internal/create/copy"
	"github.com/jy-eggroll/flk/internal/create/hardlink"
	"github.com/jy-eggroll/flk/internal/create/symlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/prompt"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var fixCmd = &cobra.Command{
	Use:     "fix",
	Aliases: []string{"fx"},
	Short:   l10n.T("Interactively repair invalid links", nil),
	Long:    l10n.T("Check link status and enter interactive mode to repair invalid links by number", nil),
	RunE:    RunFix,
}

func init() {
	MarkNeedsStore(fixCmd)
	MarkSupportsJSON(fixCmd)
	rootCmd.AddCommand(fixCmd)
	fixCmd.Flags().StringVarP(&fixDevice, "device", "d", "", l10n.T("Device names to filter by, comma-separated", nil))
	fixCmd.Flags().BoolVar(&fixSymlink, "symlink", false, l10n.T("Check only symbolic links", nil))
	fixCmd.Flags().BoolVar(&fixHardlink, "hardlink", false, l10n.T("Check only hard links", nil))
	fixCmd.Flags().BoolVar(&fixCopy, "copy", false, l10n.T("Check only copies", nil))
	fixCmd.Flags().StringVar(&fixDir, "dir", "", l10n.T("Check only records containing this path", nil))
	fixCmd.Flags().BoolVar(&fixForce, "force", false, l10n.T("Skip the deletion confirmation and repair directly", nil))
	fixCmd.Flags().BoolVar(&fixAll, "all", false, l10n.T("Automatically repair all invalid links, skipping interactive mode", nil))
}

var (
	fixDevice   string
	fixSymlink  bool
	fixHardlink bool
	fixCopy     bool
	fixDir      string
	fixForce    bool
	fixAll      bool
)

// RunFix 检查并修复无效记录，业务结果始终写 stdout，交互提示和逐项状态始终写 stderr
// 批量或交互操作会尽可能处理完用户选择的全部记录，再用 errors.Join 聚合真实失败，避免单项失败掩盖其余可修复项
func RunFix(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	format := output.OutputFormat(outputFormat)

	// 批量（--all/--yes）或显式 --force 都表示“不再逐项确认”
	// 这样 --all 才名副其实：不仅跳过选择循环，也不会在逐项修复时再次弹确认，
	// 从而在非终端环境下也能完整跑完，而不是卡在逐项确认上
	skipConfirm := fixForce || fixAll || prompt.AssumeYes()

	// checkAndDisplay 复用 check 的检查逻辑并只保留无效记录
	// JSON 即使没有记录也必须输出 []，便于脚本稳定解析；表格模式继续保留原有的人类可读提示
	checkAndDisplay := func() ([]output.CheckResult, error) {
		deviceFilters := parseDeviceFilters(fixDevice)
		results, err := performCheck(CheckOptions{
			DeviceFilters: deviceFilters,
			CheckSymlink:  fixSymlink,
			CheckHardlink: fixHardlink,
			CheckCopy:     fixCopy,
			CheckDir:      fixDir,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l10n.T("Check failed", nil), err)
		}

		invalidResults := make([]output.CheckResult, 0)
		for _, result := range results {
			if !result.Valid {
				invalidResults = append(invalidResults, result)
			}
		}

		if format == output.JSON || len(invalidResults) > 0 {
			if err := output.PrintCheckResultsFix(out, format, invalidResults); err != nil {
				return nil, fmt.Errorf("%s: %w", l10n.T("Output failed", nil), err)
			}
		} else {
			pterm.Info.WithWriter(errOut).Println(l10n.T("All links are valid; nothing to repair", nil))
		}

		return invalidResults, nil
	}

	invalidResults, err := checkAndDisplay()
	if err != nil {
		return err
	}
	if len(invalidResults) == 0 {
		return nil
	}

	// JSON 非批量模式只负责列出无效项，不进入会污染机器输出或等待 stdin 的交互流程
	// JSON 批量（--all 或 --yes）也只在上面的首次检查输出一个文档，后续逐项状态全部进入 stderr
	if format == output.JSON && !fixAll && !prompt.AssumeYes() {
		return nil
	}

	var operationErrors []error
	repairSelected := func(indices []int) {
		for _, idx := range indices {
			result := invalidResults[idx]
			if err := repairResult(result, idx, skipConfirm, errOut); err != nil {
				pterm.Error.WithWriter(errOut).Println(l10n.T("Repair failed #{{.Index}}: {{.Err}}", map[string]any{"Index": idx + 1, "Err": err.Error()}))
				operationErrors = append(operationErrors, fmt.Errorf("%s: %w", l10n.T("Repair #{{.Index}} failed", map[string]any{"Index": idx + 1}), err))
			} else {
				pterm.Success.WithWriter(errOut).Println(l10n.T("Repaired #{{.Index}}", map[string]any{"Index": idx + 1}))
			}
		}
	}

	// --all 或 --yes：批量修复全部无效记录，无需人工逐项选择
	// --yes 把“同意一切确认”延伸为“全部修复”，使 fix 也能在无人值守下完成
	if fixAll || prompt.AssumeYes() {
		indices := make([]int, len(invalidResults))
		for idx := range invalidResults {
			indices[idx] = idx
		}
		repairSelected(indices)
		return errors.Join(operationErrors...)
	}

	// 既非批量也未启用 --yes 时，只有真正可交互才能进入输入循环；
	// 否则明确失败并给出补救参数，避免在无终端环境下永久阻塞
	if !prompt.Interactive() {
		return fmt.Errorf("%s", l10n.T("Cannot interact with the user (stdin is not a terminal); rerun with --all or --yes", nil))
	}

	for {
		pterm.DefaultBox.WithWriter(errOut).WithTitle("INFO").Println(pterm.Green(l10n.T("Enter all or a to repair everything\nEnter d<number> to delete an entry, e.g. d7, only one at a time\nEnter exit or q to quit\nEnter numbers to repair the corresponding items\nSeparate with spaces", nil)))
		input, err := pterm.DefaultInteractiveTextInput.WithMultiLine(false).Show(l10n.T("Enter input", nil))
		if err != nil {
			// EOF 等输入错误必须向上传播；若继续下一轮会反复得到同一错误并形成 CPU 空转
			operationErrors = append(operationErrors, fmt.Errorf("%s: %w", l10n.T("Input error", nil), err))
			return errors.Join(operationErrors...)
		}

		input = strings.TrimSpace(input)
		if input == "exit" || input == "q" {
			// 用户主动退出不是失败，但此前已经发生的实际操作失败仍需决定最终退出码
			return errors.Join(operationErrors...)
		}

		if strings.HasPrefix(input, "d") {
			parts := strings.Fields(input[1:])
			var indices []int
			for _, part := range parts {
				idx, err := strconv.Atoi(part)
				if err != nil || idx < 1 || idx > len(invalidResults) {
					pterm.Warning.WithWriter(errOut).Println(l10n.T("Invalid number {{.Part}}", map[string]any{"Part": part}))
					continue
				}
				indices = append(indices, idx-1)
			}

			if len(indices) == 0 {
				continue
			}

			platform := runtime.GOOS
			mgr := store.GlobalManager
			for _, idx := range indices {
				result := invalidResults[idx]
				var entry map[string]string
				switch result.Type {
				case "symlink":
					entry = map[string]string{"real": result.Real, "fake": result.Fake}
				case "hardlink":
					entry = map[string]string{"prim": result.Prim, "seco": result.Seco}
				case "copy":
					entry = map[string]string{"src": result.Src, "dst": result.Dst}
				}
				mgr.RemoveMatchingEntry(platform, result.Device, result.Type, entry)
			}
			if err := mgr.Save(store.StorePath); err != nil {
				pterm.Error.WithWriter(errOut).Println(l10n.T("Save failed: {{.Err}}", map[string]any{"Err": err.Error()}))
				operationErrors = append(operationErrors, fmt.Errorf("%s: %w", l10n.T("Save failed", nil), err))
			} else {
				pterm.Success.WithWriter(errOut).Println(l10n.T("Deletion complete", nil))
			}

			invalidResults, err = checkAndDisplay()
			if err != nil {
				operationErrors = append(operationErrors, err)
				return errors.Join(operationErrors...)
			}
			if len(invalidResults) == 0 {
				return errors.Join(operationErrors...)
			}
			continue
		}

		var indices []int
		if input == "all" || input == "a" {
			for idx := range invalidResults {
				indices = append(indices, idx)
			}
		} else {
			parts := strings.Fields(input)
			for _, part := range parts {
				idx, err := strconv.Atoi(part)
				if err != nil || idx < 1 || idx > len(invalidResults) {
					pterm.Warning.WithWriter(errOut).Println(l10n.T("Invalid number {{.Part}}", map[string]any{"Part": part}))
					continue
				}
				indices = append(indices, idx-1)
			}
		}

		if len(indices) == 0 {
			continue
		}

		repairSelected(indices)

		invalidResults, err = checkAndDisplay()
		if err != nil {
			operationErrors = append(operationErrors, err)
			return errors.Join(operationErrors...)
		}
		if len(invalidResults) == 0 {
			return errors.Join(operationErrors...)
		}
	}
}

// repairResult 按记录类型重建链接，skipConfirm 为真时（来自 --all/--yes/--force）
// 不再逐项确认，直接把 force 语义透传给底层 Create，避免在批量/非交互场景再次弹确认
func repairResult(result output.CheckResult, idx int, skipConfirm bool, errorOutput ...io.Writer) error {
	// 删除计划属于交互诊断信息，必须与业务结果分流到 stderr；可选参数保留内部直接调用时的兼容性
	removeOutput := io.Writer(os.Stderr)
	if len(errorOutput) > 0 && errorOutput[0] != nil {
		removeOutput = errorOutput[0]
	}

	// 路径已存储为折叠绝对路径，直接展开即可，无需 WorkDir hack
	//
	// 修复语义统一说明（与 copy 分支保持一致）：
	//   real/prim/src 是「权威副本」，fake/seco/dst 是「派生位置」。
	//   修复动作 = 用权威副本重新覆盖派生位置。
	//
	// 历史缺陷（已在此处修正）：
	//   1. 旧实现复用 Symlink()/Hardlink() 完整命令，会先进入 HandleTargetBackup 备份流程。
	//      该流程用 os.Stat（跟随符号链接）判断 fake 是否存在，对「悬空符号链接」误判为不存在而提前
	//      返回，导致 fix --force 未把 force 透传给后续删除步骤，仍弹交互确认；更严重的是备份流程
	//      可能把「无效的 fake」反向覆盖回权威的 real，破坏正确数据。
	//   2. Symlink()/Hardlink() 捕获 ErrOperationCancelled 后返回 nil，fix 会误报「修复成功」。
	//   3. 源缺失时直接失败，无法像 copy 那样用派生位置的真实文件回填源。
	//   现在直接调用 symlink.Create / hardlink.Create，跳过备份流程，force 语义明确、不污染源，
	//   并补齐「源缺失 → 从派生位置回填」的能力，三类修复行为彻底对齐
	switch result.Type {
	case "symlink":
		expandedReal, err := pathutil.NormalizePath(result.Real)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the source path", nil), err)
		}
		expandedFake, err := pathutil.NormalizePath(result.Fake)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the link path", nil), err)
		}

		// 源（real）缺失时，若链接位置（fake）恰好是一份真实文件/目录（而非悬空/正确的符号链接），
		// 则先把它回填为权威副本，再重建符号链接，避免直接判死导致数据无法挽救
		if err := backfillSourceIfMissing(expandedReal, expandedFake, "real", "fake"); err != nil {
			return err
		}

		return symlink.Create(expandedReal, expandedFake, safeop.RemoveOptions{Force: skipConfirm, NoTrash: noTrash, Output: removeOutput})
	case "hardlink":
		expandedPrim, err := pathutil.NormalizePath(result.Prim)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the primary file path", nil), err)
		}
		expandedSeco, err := pathutil.NormalizePath(result.Seco)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the secondary file path", nil), err)
		}

		// 主文件（prim）缺失时，尝试用次文件（seco）回填后再重建硬链接
		if err := backfillSourceIfMissing(expandedPrim, expandedSeco, "prim", "seco"); err != nil {
			return err
		}

		return hardlink.Create(expandedPrim, expandedSeco, safeop.RemoveOptions{Force: skipConfirm, NoTrash: noTrash, Output: removeOutput})
	case "copy":
		expandedSrc, err := pathutil.NormalizePath(result.Src)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the source path", nil), err)
		}
		expandedDst, err := pathutil.NormalizePath(result.Dst)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the destination path", nil), err)
		}

		srcInfo, srcErr := os.Stat(expandedSrc)
		dstInfo, dstErr := os.Stat(expandedDst)

		if srcErr != nil && dstErr != nil {
			return fmt.Errorf("%s", l10n.T("Both the source and destination files are missing", nil))
		}

		var from, to string
		// 修复方向决策（此分支只会在 copy 记录无效时进入，即内容不一致或有一方缺失）：
		// 1. 源缺失 → 用目标回填源
		// 2. 目标缺失 → 用源生成目标
		// 3. 两者都在但内容不一致 → 以修改时间较新的一方为准，覆盖较旧的一方
		if srcErr != nil {
			from, to = expandedDst, expandedSrc
		} else if dstErr != nil {
			from, to = expandedSrc, expandedDst
		} else if srcInfo.ModTime().After(dstInfo.ModTime()) {
			from, to = expandedSrc, expandedDst
		} else {
			from, to = expandedDst, expandedSrc
		}

		return copy.Create(from, to, skipConfirm, false, noTrash, removeOutput)
	}
	return fmt.Errorf("%s", l10n.T("Unknown type {{.Type}}", map[string]any{"Type": result.Type}))
}

// backfillSourceIfMissing 在权威副本（source，即 real/prim）缺失、而派生位置（derived，即 fake/seco）
// 仍是一份可用真实数据时，把派生位置的内容复制回 source，为后续重建链接提供源
//
// 设计约束（保持与 symlink/hardlink 的语义边界一致）：
//   - source 已存在：无需回填，直接返回
//   - source 缺失且 derived 也缺失：无从恢复，返回错误终止修复
//   - derived 是符号链接：只可能是我们自己创建的悬空/正确 symlink，复制它没有意义（会得到一个链接
//     而非真实文件），此时同样判定为无法回填，交由上层报错
//   - 其余情况（derived 为真实文件/目录）：用 pathutil.Copy 回填，复用项目已有的复制实现（会正确
//     处理目录递归与内部符号链接）
func backfillSourceIfMissing(source, derived, sourceLabel, derivedLabel string) error {
	if _, err := os.Stat(source); err == nil {
		// source 存在，无需回填
		return nil
	}

	// source 缺失，检查 derived 是否是可用于回填的真实数据
	derivedInfo, err := os.Lstat(derived)
	if err != nil {
		return fmt.Errorf("%s", l10n.T("Neither {{.Source}} nor {{.Derived}} is usable; cannot repair", map[string]any{"Source": sourceLabel, "Derived": derivedLabel}))
	}
	if derivedInfo.Mode()&os.ModeSymlink != 0 {
		// derived 本身是符号链接，复制它得到的仍是链接，无法作为权威真实数据
		return fmt.Errorf("%s", l10n.T("{{.Source}} is missing and {{.Derived}} is a symbolic link; cannot back-fill {{.Source}}", map[string]any{"Source": sourceLabel, "Derived": derivedLabel}))
	}

	logger.Info(l10n.T("Source missing; attempting to back-fill from the derived location", nil), "from", derived, "to", source)
	if err := pathutil.Copy(derived, source); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to back-fill {{.Source}}", map[string]any{"Source": sourceLabel}), err)
	}
	return nil
}
