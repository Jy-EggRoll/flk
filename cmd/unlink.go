package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/prompt"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/jy-eggroll/flk/internal/trash"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

// unlink 命令：解除已建立的链接关系
//
// 语义说明（与 create/check/fix 的字段约定保持一致）：
//
//	real/prim/src 是「权威源」，fake/seco/dst 是「派生位置」。
//	本命令用权威源的「实际文件」替换派生位置上由 flk 创建的符号链接 / 硬链接 / 副本，
//	使派生位置成为一份独立的真实数据，从此与权威源不再存在链接关系，并从存储中移除该追踪记录。
//
// 三类记录的处理方式：
//   - symlink：fake 当前是指向 real 的符号链接。删除该符号链接，并把 real 的实际文件/目录
//     （若 real 自身还是符号链接，则跟随到其最终指向的真实目录）复制到 fake 位置
//   - hardlink：seco 与 prim 共享同一 inode。删除 seco 这个名字，再用 prim 的实际内容在
//     seco 位置复制出一份独立文件，使其不再与 prim 共享 inode
//   - copy：dst 本就是一份独立文件，不存在文件系统层面的链接，无需任何物理操作，仅移除追踪记录
//
// 仅处理「当前有效」的记录：无效记录（链接已损坏/缺失）应交由 fix 命令处理，本命令不触碰
//
// --keep-record 模式：只做上述物理替换，不从配置文件中移除追踪记录。
// 解除后记录会因链接不复存在而被 check 判为「无效」，之后可用 fix 按原记录重建链接，
// 适用于「临时断开链接、稍后还想恢复」的场景。注意 copy 记录在此模式下没有任何可执行的动作
// （其唯一的动作就是移除记录），会被跳过并给出提示
var unlinkCmd = &cobra.Command{
	Use:     "unlink",
	Aliases: []string{"ul"},
	Short:   l10n.T("Remove link relationships, replacing links/copies with real files", nil),
	Long:    l10n.T("Replace the created symlinks, hard links, and copies with the real files from real/prim/src, remove the relationships, and drop the tracking records (only valid records are handled)", nil),
	RunE:    RunUnlink,
}

func init() {
	MarkNeedsStore(unlinkCmd)
	MarkSupportsJSON(unlinkCmd)
	rootCmd.AddCommand(unlinkCmd)
	unlinkCmd.Flags().StringVarP(&unlinkDevice, "device", "d", "", l10n.T("Device names to filter by, comma-separated", nil))
	unlinkCmd.Flags().BoolVar(&unlinkSymlink, "symlink", false, l10n.T("Process only symbolic links", nil))
	unlinkCmd.Flags().BoolVar(&unlinkHardlink, "hardlink", false, l10n.T("Process only hard links", nil))
	unlinkCmd.Flags().BoolVar(&unlinkCopy, "copy", false, l10n.T("Process only copies", nil))
	unlinkCmd.Flags().StringVar(&unlinkDir, "dir", "", l10n.T("Process only records containing this path", nil))
	unlinkCmd.Flags().BoolVar(&unlinkForce, "force", false, l10n.T("Skip the deletion confirmation and proceed directly", nil))
	unlinkCmd.Flags().BoolVar(&unlinkAll, "all", false, l10n.T("Automatically remove all valid links, skipping interactive mode", nil))
	unlinkCmd.Flags().BoolVar(&unlinkKeepRecord, "keep-record", false, l10n.T("Only remove the link relationship and keep the tracking record (the record becomes invalid; use fix to recreate the link)", nil))
}

var (
	unlinkDevice     string
	unlinkSymlink    bool
	unlinkHardlink   bool
	unlinkCopy       bool
	unlinkDir        string
	unlinkForce      bool
	unlinkAll        bool
	unlinkKeepRecord bool
)

// RunUnlink 执行解除链接流程：列出当前有效记录，交互或批量地将其还原为独立真实文件
// 结果集写 stdout，提示和逐项状态写 stderr；一批操作会尽可能全部执行，再聚合操作及存储落盘错误
func RunUnlink(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	format := output.OutputFormat(outputFormat)

	// 批量（--all/--yes）或显式 --force 都表示“不再逐项确认”
	// 这样 --all 才名副其实：不仅跳过选择循环，也不在逐项删除时再次弹确认，
	// 从而在非终端环境下也能完整跑完，而不是卡在逐项确认上
	skipConfirm := unlinkForce || unlinkAll || prompt.AssumeYes()

	// checkAndDisplay 复用 check 的检查逻辑并仅保留有效记录
	// JSON 空集必须输出 [] 供脚本稳定解析；表格模式继续显示原有的“没有可解除”提示
	checkAndDisplay := func() ([]output.CheckResult, error) {
		deviceFilters := parseDeviceFilters(unlinkDevice)
		results, err := performCheck(CheckOptions{
			DeviceFilters: deviceFilters,
			CheckSymlink:  unlinkSymlink,
			CheckHardlink: unlinkHardlink,
			CheckCopy:     unlinkCopy,
			CheckDir:      unlinkDir,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l10n.T("Check failed", nil), err)
		}

		validResults := make([]output.CheckResult, 0)
		for _, result := range results {
			if result.Valid {
				validResults = append(validResults, result)
			}
		}

		if format == output.JSON || len(validResults) > 0 {
			if err := output.PrintCheckResults(out, format, validResults); err != nil {
				return nil, fmt.Errorf("%s: %w", l10n.T("Output failed", nil), err)
			}
		} else {
			pterm.Info.WithWriter(errOut).Println(l10n.T("There are no valid links to remove", nil))
		}

		return validResults, nil
	}

	validResults, err := checkAndDisplay()
	if err != nil {
		return err
	}
	if len(validResults) == 0 {
		return nil
	}

	// JSON 非批量模式只列出待解除项；--all 或 --yes 也只保留首次 JSON，后续状态进入 stderr
	if format == output.JSON && !unlinkAll && !prompt.AssumeYes() {
		return nil
	}

	var operationErrors []error
	unlinkSelected := func(indices []int) {
		for _, idx := range indices {
			result := validResults[idx]
			if err := unlinkResult(result, skipConfirm, errOut); err != nil {
				pterm.Error.WithWriter(errOut).Println(l10n.T("Removal failed #{{.Index}}: {{.Err}}", map[string]any{"Index": idx + 1, "Err": err.Error()}))
				operationErrors = append(operationErrors, fmt.Errorf("%s: %w", l10n.T("Removal #{{.Index}} failed", map[string]any{"Index": idx + 1}), err))
			} else {
				pterm.Success.WithWriter(errOut).Println(l10n.T("Removed #{{.Index}}", map[string]any{"Index": idx + 1}))
			}
		}
	}
	saveStore := func() {
		if err := saveStoreAfterUnlink(); err != nil {
			pterm.Error.WithWriter(errOut).Println(l10n.T("Save failed: {{.Err}}", map[string]any{"Err": err.Error()}))
			operationErrors = append(operationErrors, fmt.Errorf("%s: %w", l10n.T("Save failed", nil), err))
		}
	}

	// --all 或 --yes：批量解除所有有效链接，单项失败不阻断其余记录，结束后统一决定退出码
	// --yes 把“同意一切确认”自然延伸为“全部处理”，使其成为真正的非交互总开关
	if unlinkAll || prompt.AssumeYes() {
		pterm.Info.WithWriter(errOut).Println(l10n.T("Automatically removing all valid links...", nil))
		indices := make([]int, len(validResults))
		for idx := range validResults {
			indices[idx] = idx
		}
		unlinkSelected(indices)
		saveStore()
		return errors.Join(operationErrors...)
	}

	// 既非批量也未启用 --yes 时，只有真正可交互才能进入输入循环；
	// 否则明确失败并给出补救参数，避免在无终端环境下永久阻塞
	if !prompt.Interactive() {
		return fmt.Errorf("%s", l10n.T("Cannot interact with the user (stdin is not a terminal); rerun with --all or --yes", nil))
	}

	// 交互模式：输入编号解除对应项，all/a 全部解除，exit/q 主动退出仍视为成功
	for {
		pterm.DefaultBox.WithWriter(errOut).WithTitle("INFO").Println(pterm.Green(l10n.T("Enter all or a to remove everything\nEnter exit or q to quit\nEnter numbers to remove the corresponding items\nSeparate with spaces", nil)))
		input, err := pterm.DefaultInteractiveTextInput.WithMultiLine(false).Show(l10n.T("Enter input", nil))
		if err != nil {
			// EOF 等输入错误必须向上传播；若继续下一轮会反复得到同一错误并形成 CPU 空转
			operationErrors = append(operationErrors, fmt.Errorf("%s: %w", l10n.T("Input error", nil), err))
			return errors.Join(operationErrors...)
		}

		input = strings.TrimSpace(input)
		if input == "exit" || input == "q" {
			return errors.Join(operationErrors...)
		}

		var indices []int
		if input == "all" || input == "a" {
			for idx := range validResults {
				indices = append(indices, idx)
			}
		} else {
			parts := strings.Fields(input)
			for _, part := range parts {
				idx, err := strconv.Atoi(part)
				if err != nil || idx < 1 || idx > len(validResults) {
					pterm.Warning.WithWriter(errOut).Println(l10n.T("Invalid number {{.Part}}", map[string]any{"Part": part}))
					continue
				}
				indices = append(indices, idx-1)
			}
		}

		if len(indices) == 0 {
			continue
		}

		unlinkSelected(indices)
		saveStore()

		validResults, err = checkAndDisplay()
		if err != nil {
			operationErrors = append(operationErrors, err)
			return errors.Join(operationErrors...)
		}
		if len(validResults) == 0 {
			return errors.Join(operationErrors...)
		}
	}
}

// unlinkResult 解除单条记录的链接关系
// 成功完成物理替换后，默认从全局存储中移除该记录；--keep-record 模式下保留记录，
// 仅解除文件系统层面的链接关系（记录随后会被 check 判为无效，可用 fix 按原记录重建链接）
// skipConfirm 为真时（来自 --all/--yes/--force）不再逐项确认，直接执行物理替换
// 注意：本函数只更新内存中的 store，落盘由调用方在一批操作后统一执行 saveStoreAfterUnlink，减少重复写盘
func unlinkResult(result output.CheckResult, skipConfirm bool, errorOutput ...io.Writer) error {
	// 解除过程中的确认、警告和状态都属于交互诊断信息，默认写 stderr，并允许命令注入 Cobra 的错误输出 writer
	errOut := io.Writer(os.Stderr)
	if len(errorOutput) > 0 && errorOutput[0] != nil {
		errOut = errorOutput[0]
	}

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
		if err := replaceWithReal(expandedReal, expandedFake, "real", "fake", skipConfirm, errOut); err != nil {
			return err
		}
	case "hardlink":
		expandedPrim, err := pathutil.NormalizePath(result.Prim)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the primary file path", nil), err)
		}
		expandedSeco, err := pathutil.NormalizePath(result.Seco)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the secondary file path", nil), err)
		}
		if err := replaceWithReal(expandedPrim, expandedSeco, "prim", "seco", skipConfirm, errOut); err != nil {
			return err
		}
	case "copy":
		// dst 本就是独立文件，不存在文件系统层面的链接，无需任何物理操作，仅移除追踪记录
		// --keep-record 模式下连记录也保留，对 copy 而言没有任何可执行的动作，
		// 明确提示后按成功返回，避免用户误以为「解除成功」是做了什么实际变更
		if unlinkKeepRecord {
			pterm.Warning.WithWriter(errOut).Println(l10n.T("The copy record has no filesystem-level link to remove; skipped in --keep-record mode: {{.Dst}}", map[string]any{"Dst": result.Dst}))
			return nil
		}
	default:
		return fmt.Errorf("%s", l10n.T("Unknown type {{.Type}}", map[string]any{"Type": result.Type}))
	}

	// 物理替换完成后移除追踪记录（记录中的路径为折叠形式，result 字段直接来自存储，故可原样匹配）
	// --keep-record 模式下跳过移除，让记录留在配置文件中供后续 fix 重建
	if !unlinkKeepRecord {
		removeUnlinkRecord(result)
	}
	return nil
}

// replaceWithReal 用权威源的「实际文件」替换派生位置上的链接
// source：权威源（real/prim），derived：派生位置（fake/seco）
//
// 关键设计：
//   - 先用 filepath.EvalSymlinks 解析 source 的真实路径：若 source 自身是符号链接，会跟随到其最终
//     指向的真实文件/目录，满足需求「如果是符号链接，则是实际目录」，确保复制出的是真实数据而非又一个链接
//   - source 不可用（缺失/无法解析）时立即报错并跳过，绝不删除派生位置，避免破坏数据（源缺失不破坏）
//   - 将派生位置的旧链接移入回收站（而非真实删除），所有数据都可恢复
//   - 移动后用 pathutil.Copy 把真实数据复制到派生位置；Copy 会正确处理文件与目录（目录递归复制）
//
// skipConfirm 为真时跳过交互确认（来自 --all/--yes/--force）；为假且环境不可交互时，
// prompt.Confirm 会返回错误而不是无限等待，保证不会在无人值守场景挂起
func replaceWithReal(source, derived, sourceLabel, derivedLabel string, skipConfirm bool, errorOutput ...io.Writer) error {
	errOut := io.Writer(os.Stderr)
	if len(errorOutput) > 0 && errorOutput[0] != nil {
		errOut = errorOutput[0]
	}

	actualSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("%s", l10n.T("{{.Source}} is unavailable ({{.Err}}); skipped to avoid data loss", map[string]any{"Source": sourceLabel, "Err": err.Error()}))
	}

	if !skipConfirm {
		pterm.Warning.WithWriter(errOut).Println(l10n.T("About to remove the link and replace it with a real file: {{.Path}}", map[string]any{"Path": derived}))
		// 统一经 prompt.Confirm：--yes 直接同意，非终端且未 --yes 时返回错误而非阻塞
		confirm, cerr := prompt.Confirm(l10n.T("Confirm removing this link relationship?", nil), false)
		if cerr != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to get the confirmation input", nil), cerr)
		}
		if !confirm {
			return safeop.ErrOperationCancelled
		}
	}

	// 将派生位置的旧链接移入回收站；不存在则视为已就绪
	if err := trash.MoveToTrash(derived); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%s: %w", l10n.T("Failed to move {{.Derived}} to the trash", map[string]any{"Derived": derivedLabel}), err)
	}

	// 用权威源的真实内容在派生位置生成一份独立副本，至此二者不再共享链接关系
	if err := pathutil.Copy(actualSource, derived); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to copy the real file to {{.Derived}}", map[string]any{"Derived": derivedLabel}), err)
	}
	return nil
}

// removeUnlinkRecord 从全局存储中移除一条链接记录（按类型选择匹配字段，与 fix 的删除逻辑保持一致）
func removeUnlinkRecord(result output.CheckResult) {
	mgr := store.GlobalManager
	if mgr == nil {
		return
	}
	platform := runtime.GOOS
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

// saveStoreAfterUnlink 将内存中的存储改动落盘，供一批解除操作完成后统一调用
// --keep-record 模式下内存 store 未发生变化，此时落盘只是重写一份内容等价（仅排序）的文件，无害
// 保存错误必须返回给 RunUnlink，与单项解除错误一起决定最终退出码，不能只记录日志后伪装成功
func saveStoreAfterUnlink() error {
	mgr := store.GlobalManager
	if mgr == nil {
		return nil
	}
	return mgr.Save(store.StorePath)
}
