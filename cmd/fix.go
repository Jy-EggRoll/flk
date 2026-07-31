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
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var fixCmd = &cobra.Command{
	Use:     "fix",
	Aliases: []string{"fx"},
	Short:   "交互式修复无效链接",
	Long:    "检查链接状态并进入交互模式，允许用户选择编号修复无效链接",
	RunE:    RunFix,
}

func init() {
	MarkNeedsStore(fixCmd)
	MarkSupportsJSON(fixCmd)
	rootCmd.AddCommand(fixCmd)
	fixCmd.Flags().StringVarP(&fixDevice, "device", "d", "", "设备名称，用于过滤检查，可用逗号分隔多个设备")
	fixCmd.Flags().BoolVar(&fixSymlink, "symlink", false, "仅检查符号链接")
	fixCmd.Flags().BoolVar(&fixHardlink, "hardlink", false, "仅检查硬链接")
	fixCmd.Flags().BoolVar(&fixCopy, "copy", false, "仅检查复制")
	fixCmd.Flags().StringVar(&fixDir, "dir", "", "仅检查包含该路径的记录")
	fixCmd.Flags().BoolVar(&fixForce, "force", false, "修复时跳过删除确认，直接执行")
	fixCmd.Flags().BoolVar(&fixAll, "all", false, "自动修复所有无效链接，跳过交互模式")
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
			return nil, fmt.Errorf("检查失败: %w", err)
		}

		invalidResults := make([]output.CheckResult, 0)
		for _, result := range results {
			if !result.Valid {
				invalidResults = append(invalidResults, result)
			}
		}

		if format == output.JSON || len(invalidResults) > 0 {
			if err := output.PrintCheckResultsFix(out, format, invalidResults); err != nil {
				return nil, fmt.Errorf("输出失败: %w", err)
			}
		} else {
			pterm.Info.WithWriter(errOut).Println("所有链接都有效，无需修复")
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

	// JSON 非 --all 模式只负责列出无效项，不进入会污染机器输出或等待 stdin 的交互流程
	// JSON --all 也只在上面的首次检查输出一个文档，后续逐项状态全部进入 stderr
	if format == output.JSON && !fixAll {
		return nil
	}

	var operationErrors []error
	repairSelected := func(indices []int) {
		for _, idx := range indices {
			result := invalidResults[idx]
			if err := repairResult(result, idx, errOut); err != nil {
				pterm.Error.WithWriter(errOut).Printf("修复失败 #%d %v\n", idx+1, err)
				operationErrors = append(operationErrors, fmt.Errorf("修复 #%d 失败: %w", idx+1, err))
			} else {
				pterm.Success.WithWriter(errOut).Printf("修复成功 #%d\n", idx+1)
			}
		}
	}

	if fixAll {
		indices := make([]int, len(invalidResults))
		for idx := range invalidResults {
			indices[idx] = idx
		}
		repairSelected(indices)
		return errors.Join(operationErrors...)
	}

	for {
		pterm.DefaultBox.WithWriter(errOut).WithTitle("INFO").Println(pterm.Green("输入 all 或 a 修复所有\n输入 d<编号> 删除条目，如 d7，单次只能删除一个\n输入 exit 或 q 退出程序\n输入数字以修复对应项\n使用空格分隔"))
		input, err := pterm.DefaultInteractiveTextInput.WithMultiLine(false).Show("请输入")
		if err != nil {
			// EOF 等输入错误必须向上传播；若继续下一轮会反复得到同一错误并形成 CPU 空转
			operationErrors = append(operationErrors, fmt.Errorf("输入错误: %w", err))
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
					pterm.Warning.WithWriter(errOut).Printf("无效编号 %s\n", part)
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
				pterm.Error.WithWriter(errOut).Println("保存失败 " + err.Error())
				operationErrors = append(operationErrors, fmt.Errorf("保存失败: %w", err))
			} else {
				pterm.Success.WithWriter(errOut).Println("删除完成")
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
					pterm.Warning.WithWriter(errOut).Printf("无效编号 %s\n", part)
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

func repairResult(result output.CheckResult, idx int, errorOutput ...io.Writer) error {
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
			return fmt.Errorf("展开源路径失败: %w", err)
		}
		expandedFake, err := pathutil.NormalizePath(result.Fake)
		if err != nil {
			return fmt.Errorf("展开链接路径失败: %w", err)
		}

		// 源（real）缺失时，若链接位置（fake）恰好是一份真实文件/目录（而非悬空/正确的符号链接），
		// 则先把它回填为权威副本，再重建符号链接，避免直接判死导致数据无法挽救
		if err := backfillSourceIfMissing(expandedReal, expandedFake, "real", "fake"); err != nil {
			return err
		}

		return symlink.Create(expandedReal, expandedFake, safeop.RemoveOptions{Force: fixForce, Output: removeOutput})
	case "hardlink":
		expandedPrim, err := pathutil.NormalizePath(result.Prim)
		if err != nil {
			return fmt.Errorf("展开主文件路径失败: %w", err)
		}
		expandedSeco, err := pathutil.NormalizePath(result.Seco)
		if err != nil {
			return fmt.Errorf("展开次文件路径失败: %w", err)
		}

		// 主文件（prim）缺失时，尝试用次文件（seco）回填后再重建硬链接
		if err := backfillSourceIfMissing(expandedPrim, expandedSeco, "prim", "seco"); err != nil {
			return err
		}

		return hardlink.Create(expandedPrim, expandedSeco, safeop.RemoveOptions{Force: fixForce, Output: removeOutput})
	case "copy":
		expandedSrc, err := pathutil.NormalizePath(result.Src)
		if err != nil {
			return fmt.Errorf("展开源路径失败: %w", err)
		}
		expandedDst, err := pathutil.NormalizePath(result.Dst)
		if err != nil {
			return fmt.Errorf("展开目标路径失败: %w", err)
		}

		srcInfo, srcErr := os.Stat(expandedSrc)
		dstInfo, dstErr := os.Stat(expandedDst)

		if srcErr != nil && dstErr != nil {
			return fmt.Errorf("源文件和目标文件都不存在")
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

		return copy.Create(from, to, fixForce, false, removeOutput)
	}
	return fmt.Errorf("未知类型 %s", result.Type)
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
		return fmt.Errorf("%s 与 %s 均不可用，无法修复", sourceLabel, derivedLabel)
	}
	if derivedInfo.Mode()&os.ModeSymlink != 0 {
		// derived 本身是符号链接，复制它得到的仍是链接，无法作为权威真实数据
		return fmt.Errorf("%s 缺失且 %s 是符号链接，无法回填 %s", sourceLabel, derivedLabel, sourceLabel)
	}

	logger.Info("源缺失，尝试用派生位置回填", "from", derived, "to", source)
	if err := pathutil.Copy(derived, source); err != nil {
		return fmt.Errorf("回填 %s 失败: %w", sourceLabel, err)
	}
	return nil
}
