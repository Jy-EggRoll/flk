package cmd

import (
	"fmt"
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
	Run:     RunFix,
}

func init() {
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

func RunFix(cmd *cobra.Command, args []string) {
	checkAndDisplay := func() []output.CheckResult {
		deviceFilters := parseDeviceFilters(fixDevice)
		results, err := performCheck(CheckOptions{
			DeviceFilters: deviceFilters,
			CheckSymlink:  fixSymlink,
			CheckHardlink: fixHardlink,
			CheckCopy:     fixCopy,
			CheckDir:      fixDir,
		})
		if err != nil {
			logger.Error("检查失败: " + err.Error())
			return nil
		}

		var invalidResults []output.CheckResult
		for _, r := range results {
			if !r.Valid {
				invalidResults = append(invalidResults, r)
			}
		}

		if len(invalidResults) > 0 {
			format := output.OutputFormat(outputFormat)
			if err := output.PrintCheckResultsFix(format, invalidResults); err != nil {
				logger.Error("输出失败: " + err.Error())
				return invalidResults
			}
		} else {
			pterm.Info.Println("所有链接都有效，无需修复")
		}

		return invalidResults
	}

	invalidResults := checkAndDisplay()
	if len(invalidResults) == 0 {
		return
	}

	// JSON 输出模式为非交互模式：JSON 供机器解析，进入交互 TUI 既无意义、又会因非 TTY 的 stdin
	// 阻塞而挂起。此模式下仅列出无效项（checkAndDisplay 已输出纯净 JSON），需实际修复请配合 --all
	if output.OutputFormat(outputFormat) == output.JSON && !fixAll {
		return
	}

	if fixAll {
		for idx, result := range invalidResults {
			if err := repairResult(result, idx); err != nil {
				pterm.Error.Printf("修复失败 #%d %v\n", idx+1, err)
			} else {
				pterm.Success.Printf("修复成功 #%d\n", idx+1)
			}
		}
		return
	}

	for {
		pterm.DefaultBox.WithTitle("INFO").Println(pterm.Green("输入 all 或 a 修复所有\n输入 d<编号> 删除条目，如 d7，单次只能删除一个\n输入 exit 或 q 退出程序\n输入数字以修复对应项\n使用空格分隔"))
		input, err := pterm.DefaultInteractiveTextInput.WithMultiLine(false).Show("请输入")
		if err != nil {
			// 输入错误（典型如 stdin 被关闭/EOF）时必须退出循环而非 continue：
			// 若 continue，下一轮仍会立即拿到同样的错误，形成不可中断的死循环（CPU 空转）
			logger.Error("输入错误 " + err.Error())
			break
		}

		input = strings.TrimSpace(input)
		if input == "exit" || input == "q" {
			break
		}

		if strings.HasPrefix(input, "d") {
			parts := strings.Fields(input[1:])
			var indices []int
			for _, part := range parts {
				idx, err := strconv.Atoi(part)
				if err != nil || idx < 1 || idx > len(invalidResults) {
					pterm.Warning.Printf("无效编号 %s\n", part)
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
				logger.Error("保存失败 " + err.Error())
			}

			pterm.Success.Println("删除完成")
			invalidResults = checkAndDisplay()
			if len(invalidResults) == 0 {
				break
			}
			continue
		}

		var indices []int
		if input == "all" || input == "a" {
			for i := range invalidResults {
				indices = append(indices, i)
			}
		} else {
			parts := strings.Fields(input)
			for _, part := range parts {
				idx, err := strconv.Atoi(part)
				if err != nil || idx < 1 || idx > len(invalidResults) {
					pterm.Warning.Printf("无效编号 %s\n", part)
					continue
				}
				indices = append(indices, idx-1)
			}
		}

		if len(indices) == 0 {
			continue
		}

		for _, idx := range indices {
			result := invalidResults[idx]
			if err := repairResult(result, idx); err != nil {
				pterm.Error.Printf("修复失败 #%d %v\n", idx+1, err)
			} else {
				pterm.Success.Printf("修复成功 #%d\n", idx+1)
			}
		}

		invalidResults = checkAndDisplay()
		if len(invalidResults) == 0 {
			break
		}
	}
}

func repairResult(result output.CheckResult, idx int) error {
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

		return symlink.Create(expandedReal, expandedFake, safeop.RemoveOptions{Force: fixForce})
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

		return hardlink.Create(expandedPrim, expandedSeco, safeop.RemoveOptions{Force: fixForce})
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

		return copy.Create(from, to, fixForce, false)
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
