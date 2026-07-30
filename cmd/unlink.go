package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/jy-eggroll/flk/internal/trash"
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
	Short:   "解除链接关系，用真实文件替换链接/副本",
	Long:    "用 real/prim/src 的实际文件替换已创建的符号链接、硬链接、副本，解除对应关系并移除追踪记录（仅处理当前有效的记录）",
	Run:     RunUnlink,
}

func init() {
	rootCmd.AddCommand(unlinkCmd)
	unlinkCmd.Flags().StringVarP(&unlinkDevice, "device", "d", "", "设备名称，用于过滤，可用逗号分隔多个设备")
	unlinkCmd.Flags().BoolVar(&unlinkSymlink, "symlink", false, "仅处理符号链接")
	unlinkCmd.Flags().BoolVar(&unlinkHardlink, "hardlink", false, "仅处理硬链接")
	unlinkCmd.Flags().BoolVar(&unlinkCopy, "copy", false, "仅处理复制")
	unlinkCmd.Flags().StringVar(&unlinkDir, "dir", "", "仅处理包含该路径的记录")
	unlinkCmd.Flags().BoolVar(&unlinkForce, "force", false, "解除时跳过删除确认，直接执行")
	unlinkCmd.Flags().BoolVar(&unlinkAll, "all", false, "自动解除所有有效链接，跳过交互模式")
	unlinkCmd.Flags().BoolVar(&unlinkKeepRecord, "keep-record", false, "仅解除链接关系，保留配置文件中的追踪记录（解除后记录变为无效，可用 fix 重建链接）")
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
func RunUnlink(cmd *cobra.Command, args []string) {
	// checkAndDisplay 复用 check 的检查逻辑，仅保留「有效」记录并渲染出带编号的表格
	checkAndDisplay := func() []output.CheckResult {
		deviceFilters := parseDeviceFilters(unlinkDevice)
		results, err := performCheck(CheckOptions{
			DeviceFilters: deviceFilters,
			CheckSymlink:  unlinkSymlink,
			CheckHardlink: unlinkHardlink,
			CheckCopy:     unlinkCopy,
			CheckDir:      unlinkDir,
		})
		if err != nil {
			logger.Error("检查失败: " + err.Error())
			return nil
		}

		var validResults []output.CheckResult
		for _, r := range results {
			if r.Valid {
				validResults = append(validResults, r)
			}
		}

		if len(validResults) > 0 {
			format := output.OutputFormat(outputFormat)
			if err := output.PrintCheckResults(format, validResults); err != nil {
				logger.Error("输出失败: " + err.Error())
				return validResults
			}
		} else {
			pterm.Info.Println("没有可解除的有效链接")
		}

		return validResults
	}

	validResults := checkAndDisplay()
	if len(validResults) == 0 {
		return
	}

	// JSON 输出模式为非交互模式：JSON 供机器解析，进入交互 TUI 既无意义、又会因非 TTY 的 stdin
	// 阻塞而挂起。此模式下仅列出待解除项（上面 checkAndDisplay 已输出纯净 JSON），需实际解除请配合 --all
	if output.OutputFormat(outputFormat) == output.JSON && !unlinkAll {
		return
	}

	// --all：批量解除所有有效链接，跳过交互
	if unlinkAll {
		pterm.Info.Println("自动解除所有有效链接...")
		for idx, result := range validResults {
			if err := unlinkResult(result); err != nil {
				pterm.Error.Printf("解除失败 #%d %v\n", idx+1, err)
			} else {
				pterm.Success.Printf("解除成功 #%d\n", idx+1)
			}
		}
		saveStoreAfterUnlink()
		return
	}

	// 交互模式：输入编号解除对应项，all/a 全部解除，exit/q 退出
	for {
		pterm.DefaultBox.WithTitle("INFO").Println(pterm.Green("输入 all 或 a 解除所有\n输入 exit 或 q 退出程序\n输入数字以解除对应项\n使用空格分隔"))
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

		var indices []int
		if input == "all" || input == "a" {
			for i := range validResults {
				indices = append(indices, i)
			}
		} else {
			parts := strings.Fields(input)
			for _, part := range parts {
				idx, err := strconv.Atoi(part)
				if err != nil || idx < 1 || idx > len(validResults) {
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
			result := validResults[idx]
			if err := unlinkResult(result); err != nil {
				pterm.Error.Printf("解除失败 #%d %v\n", idx+1, err)
			} else {
				pterm.Success.Printf("解除成功 #%d\n", idx+1)
			}
		}
		saveStoreAfterUnlink()

		validResults = checkAndDisplay()
		if len(validResults) == 0 {
			break
		}
	}
}

// unlinkResult 解除单条记录的链接关系
// 成功完成物理替换后，默认从全局存储中移除该记录；--keep-record 模式下保留记录，
// 仅解除文件系统层面的链接关系（记录随后会被 check 判为无效，可用 fix 按原记录重建链接）
// 注意：本函数只更新内存中的 store，落盘由调用方在一批操作后统一执行 saveStoreAfterUnlink，减少重复写盘
func unlinkResult(result output.CheckResult) error {
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
		if err := replaceWithReal(expandedReal, expandedFake, "real", "fake"); err != nil {
			return err
		}
	case "hardlink":
		expandedPrim, err := pathutil.NormalizePath(result.Prim)
		if err != nil {
			return fmt.Errorf("展开主文件路径失败: %w", err)
		}
		expandedSeco, err := pathutil.NormalizePath(result.Seco)
		if err != nil {
			return fmt.Errorf("展开次文件路径失败: %w", err)
		}
		if err := replaceWithReal(expandedPrim, expandedSeco, "prim", "seco"); err != nil {
			return err
		}
	case "copy":
		// dst 本就是独立文件，不存在文件系统层面的链接，无需任何物理操作，仅移除追踪记录
		// --keep-record 模式下连记录也保留，对 copy 而言没有任何可执行的动作，
		// 明确提示后按成功返回，避免用户误以为「解除成功」是做了什么实际变更
		if unlinkKeepRecord {
			pterm.Warning.Println("copy 记录无文件系统层面的链接可解除，--keep-record 模式下跳过: " + result.Dst)
			return nil
		}
	default:
		return fmt.Errorf("未知类型 %s", result.Type)
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
func replaceWithReal(source, derived, sourceLabel, derivedLabel string) error {
	actualSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("%s 不可用（%v），已跳过以避免数据丢失", sourceLabel, err)
	}

	if !unlinkForce {
		pterm.Warning.Println("即将解除链接并替换为真实文件: " + derived)
		confirm, cerr := pterm.DefaultInteractiveConfirm.WithDefaultValue(false).Show("确认解除该链接关系？")
		if cerr != nil {
			return fmt.Errorf("获取确认输入失败: %w", cerr)
		}
		if !confirm {
			return safeop.ErrOperationCancelled
		}
	}

	// 将派生位置的旧链接移入回收站；不存在则视为已就绪
	if err := trash.MoveToTrash(derived); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("移动 %s 至回收站失败: %w", derivedLabel, err)
	}

	// 用权威源的真实内容在派生位置生成一份独立副本，至此二者不再共享链接关系
	if err := pathutil.Copy(actualSource, derived); err != nil {
		return fmt.Errorf("复制真实文件到 %s 失败: %w", derivedLabel, err)
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
func saveStoreAfterUnlink() {
	mgr := store.GlobalManager
	if mgr == nil {
		return
	}
	if err := mgr.Save(store.StorePath); err != nil {
		logger.Error("保存失败 " + err.Error())
	}
}
