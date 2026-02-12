// Package cmd 的 fix.go 提供 fix 子命令
// fix 命令用于修复无效的符号链接和硬链接
//
// 主要功能：
// 1. 检查并修复问题链接
// 2. 支持交互式和自动修复模式
// 3. 支持按设备、链接类型筛选
//
// 使用示例：
//
//	flk fix                        # 交互式修复所有问题链接
//	flk fix --auto                 # 自动修复可自动恢复的链接
//	flk fix --symlink              # 仅修复符号链接
//	flk fix --hardlink             # 仅修复硬链接
//	flk fix --device laptop        # 仅修复 laptop 设备的链接
//	flk -w /path/to/project fix    # 在指定目录下执行修复
package cmd

import (
	"fmt"
	"runtime"

	"file-link-manager/internal/fixer"
	"file-link-manager/internal/interact"
	"file-link-manager/internal/storage"

	"github.com/spf13/cobra"
)

var (
	// fixDevice 设备筛选
	fixDevice string
	// fixSymlinkOnly 仅修复符号链接
	fixSymlinkOnly bool
	// fixHardlinkOnly 仅修复硬链接
	fixHardlinkOnly bool
	// fixAutoMode 自动修复模式
	fixAutoMode bool
)

// fixCmd 是 fix 命令的定义
var fixCmd = &cobra.Command{
	Use:   "fix",
	Short: "修复无效的符号链接和硬链接",
	Long: `修复无效的符号链接和硬链接

fix 命令会检查所有记录的链接，并对发现的问题进行修复。

修复模式：
  交互式（默认）：对每个问题链接询问用户如何处理
  自动模式（--auto）：自动修复可自动恢复的链接（跳过需要用户确认的操作）

可自动修复的问题：
  - 符号链接不存在但真实路径存在 → 重新创建符号链接
  - 符号链接目标不匹配 → 更新符号链接
  - 硬链接不存在但主要文件存在 → 重新创建硬链接

需要手动确认的问题：
  - 真实路径/主要文件不存在 → 需要确认是否删除记录
  - 路径存在但类型不正确 → 需要确认是否删除现有文件

示例：
  flk fix                        # 交互式修复所有问题链接
  flk fix --auto                 # 自动修复模式
  flk fix --symlink              # 仅修复符号链接
  flk fix --hardlink             # 仅修复硬链接
  flk fix --device laptop        # 仅修复 laptop 设备的链接
  flk -w /path/to/project fix    # 在指定目录下执行修复`,
	RunE: runFix,
}

func init() {
	rootCmd.AddCommand(fixCmd)

	fixCmd.Flags().StringVarP(&fixDevice, "device", "d", "", "仅修复指定设备的链接")
	fixCmd.Flags().BoolVarP(&fixSymlinkOnly, "symlink", "s", false, "仅修复符号链接")
	fixCmd.Flags().BoolVarP(&fixHardlinkOnly, "hardlink", "H", false, "仅修复硬链接")
	fixCmd.Flags().BoolVarP(&fixAutoMode, "auto", "a", false, "自动修复模式（仅修复可自动恢复的链接）")
}

// runFix 执行 fix 命令
func runFix(cmd *cobra.Command, args []string) error {
	// 获取有效的工作目录
	workDir, err := GetEffectiveWorkDir()
	if err != nil {
		return err
	}

	// 加载存储
	st := storage.NewStorage(workDir)
	if err := st.Load(); err != nil {
		return fmt.Errorf("加载配置文件失败：%w", err)
	}

	// 获取当前操作系统类型
	osType := runtime.GOOS

	// 确定修复模式
	mode := fixer.FixAll
	if fixSymlinkOnly && !fixHardlinkOnly {
		mode = fixer.FixSymlink
	} else if fixHardlinkOnly && !fixSymlinkOnly {
		mode = fixer.FixHardlink
	}

	// 验证参数
	if fixSymlinkOnly && fixHardlinkOnly {
		return fmt.Errorf("不能同时指定 --symlink 和 --hardlink，这将导致没有链接被修复")
	}

	// 显示修复范围
	printFixScope(osType, mode, fixDevice, fixAutoMode)

	// 创建修复器
	f := fixer.NewFixer(st, osType, mode, fixDevice, fixAutoMode)

	// 执行修复
	result, err := f.Fix()
	if err != nil {
		return fmt.Errorf("修复过程出错：%w", err)
	}

	// 显示修复结果
	printFixResult(result)

	return nil
}

// printFixScope 显示修复范围
func printFixScope(osType string, mode fixer.FixMode, device string, autoMode bool) {
	interact.PrintInfo("=== 链接修复 ===")
	interact.PrintInfo("操作系统：%s", osType)

	switch mode {
	case fixer.FixSymlink:
		interact.PrintInfo("修复类型：仅符号链接")
	case fixer.FixHardlink:
		interact.PrintInfo("修复类型：仅硬链接")
	case fixer.FixAll:
		interact.PrintInfo("修复类型：所有链接")
	}

	if device != "" {
		interact.PrintInfo("设备筛选：%s", device)
	} else {
		interact.PrintInfo("设备筛选：全部")
	}

	if autoMode {
		interact.PrintInfo("修复模式：自动")
	} else {
		interact.PrintInfo("修复模式：交互式")
	}

	fmt.Println()
}

// printFixResult 显示修复结果
func printFixResult(result *fixer.FixResult) {
	fmt.Println()
	interact.PrintInfo("=== 修复结果 ===")

	// 符号链接统计
	if result.SymlinksFixed > 0 || result.SymlinksFailed > 0 ||
		result.SymlinksDeleted > 0 || result.SymlinksSkipped > 0 {
		interact.PrintInfo("符号链接：")
		if result.SymlinksFixed > 0 {
			interact.PrintSuccess("  - 已修复：%d", result.SymlinksFixed)
		}
		if result.SymlinksFailed > 0 {
			interact.PrintError("  - 修复失败：%d", result.SymlinksFailed)
		}
		if result.SymlinksDeleted > 0 {
			interact.PrintInfo("  - 记录已删除：%d", result.SymlinksDeleted)
		}
		if result.SymlinksSkipped > 0 {
			interact.PrintInfo("  - 已跳过：%d", result.SymlinksSkipped)
		}
	}

	// 硬链接统计
	if result.HardlinksFixed > 0 || result.HardlinksFailed > 0 ||
		result.HardlinksDeleted > 0 || result.HardlinksSkipped > 0 {
		interact.PrintInfo("硬链接：")
		if result.HardlinksFixed > 0 {
			interact.PrintSuccess("  - 已修复：%d", result.HardlinksFixed)
		}
		if result.HardlinksFailed > 0 {
			interact.PrintError("  - 修复失败：%d", result.HardlinksFailed)
		}
		if result.HardlinksDeleted > 0 {
			interact.PrintInfo("  - 记录已删除：%d", result.HardlinksDeleted)
		}
		if result.HardlinksSkipped > 0 {
			interact.PrintInfo("  - 已跳过：%d", result.HardlinksSkipped)
		}
	}

	// 总结
	totalFixed := result.SymlinksFixed + result.HardlinksFixed
	totalFailed := result.SymlinksFailed + result.HardlinksFailed
	totalDeleted := result.SymlinksDeleted + result.HardlinksDeleted
	totalSkipped := result.SymlinksSkipped + result.HardlinksSkipped

	if totalFixed == 0 && totalFailed == 0 && totalDeleted == 0 && totalSkipped == 0 {
		interact.PrintSuccess("没有发现需要修复的链接问题")
	} else {
		fmt.Println()
		interact.PrintInfo("总计：修复 %d，失败 %d，删除 %d，跳过 %d",
			totalFixed, totalFailed, totalDeleted, totalSkipped)
	}
}
