package cmd

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"file-link-manager/internal/checker"
	"file-link-manager/internal/fixer"
	"file-link-manager/internal/interact"
	"file-link-manager/internal/location"
	"file-link-manager/internal/pathutil"
	"file-link-manager/internal/storage"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "检查链接有效性",
	Long: `检查符号链接和硬链接的有效性

支持检查符号链接、硬链接或全部链接
可以在本地目录或全局范围内进行检查
使用"--device"可以只检查指定设备和通用文件的链接

如果发现问题，可以使用 --fix 或 --fix-auto 参数直接进行修复。`,
	RunE: runCheck,
}

var (
	checkSymlink         bool
	checkHardlink        bool
	checkAll             bool
	checkLogQuestionOnly bool
	checkUpdateHome      bool
	checkLocation        string
	checkGlobal          bool
	checkDevice          string
	checkFix             bool
	checkFixAuto         bool
)

func init() {
	rootCmd.AddCommand(checkCmd)

	// 添加标志
	checkCmd.Flags().BoolVarP(&checkSymlink, "symlink", "s", false, "仅检查符号链接")
	checkCmd.Flags().BoolVarP(&checkHardlink, "hardlink", "H", false, "仅检查硬链接")
	checkCmd.Flags().BoolVar(&checkAll, "all", false, "检查所有链接（默认行为，不需要指定）")
	checkCmd.Flags().BoolVar(&checkLogQuestionOnly, "log-question-only", false, "仅记录有问题的链接，不进行交互")
	checkCmd.Flags().BoolVar(&checkUpdateHome, "update-home", false, "更新家目录路径，用于在迁移设备后快速恢复所有链接")
	checkCmd.Flags().StringVar(&checkLocation, "location", "", "指定“file-link-manager-links.json”文件的父目录，检查将在该目录下进行")
	checkCmd.Flags().BoolVar(&checkGlobal, "global", false, "读取全集记录文件，检查所有已记录位置")
	checkCmd.Flags().StringVarP(&checkDevice, "device", "d", "", "指定设备名称，仅检查该设备和通用文件的链接")
	checkCmd.Flags().BoolVar(&checkFix, "fix", false, "检查完成后自动进入修复模式（交互式）")
	checkCmd.Flags().BoolVar(&checkFixAuto, "fix-auto", false, "检查完成后自动修复可修复的链接（非交互式）")
}

func runCheck(cmd *cobra.Command, args []string) error {
	// global 和 location 不能同时使用
	if checkGlobal && checkLocation != "" {
		return fmt.Errorf("“--global”和“--location”不能同时使用")
	}

	// 如果使用 global 模式
	if checkGlobal {
		return runGlobalCheck()
	}

	// 确定工作目录和 storage
	var workDir string
	var st *storage.Storage

	if checkLocation != "" {
		// 使用指定的位置
		absPath, err := filepath.Abs(checkLocation)
		if err != nil {
			return fmt.Errorf("获取绝对路径失败：%w", err)
		}
		workDir = absPath
		st = storage.NewStorage(workDir)
	} else {
		// 使用当前工作目录
		var err error
		workDir, err = GetEffectiveWorkDir()
		if err != nil {
			return err
		}
		st = storage.NewStorage(workDir)
	}

	// 如果启用了 update-home，先执行更新
	if checkUpdateHome {
		if err := updateHomePathsForStorage(st); err != nil {
			return fmt.Errorf("更新家目录失败：%w", err)
		}
	}

	// 确定检查模式
	var mode checker.CheckMode
	if checkSymlink {
		mode = checker.CheckSymlink
	} else if checkHardlink {
		mode = checker.CheckHardlink
	} else {
		mode = checker.CheckAll
	}

	// 使用当前操作系统
	osType := pathutil.GetCurrentOS()

	// 检查文件是否存在（在 update-home 模式下允许文件不存在）
	if !checkUpdateHome && !st.FileExists() {
		return errors.New("当前目录下未找到“file-link-manager-links.json”文件")
	}

	// 加载 storage
	if err := st.Load(); err != nil {
		return fmt.Errorf("加载“file-link-manager-links.json”失败：%w", err)
	}

	// 创建检查器
	chk := checker.NewChecker(st, osType, mode, checkLogQuestionOnly, checkDevice)

	// 执行检查
	interact.PrintInfo("开始检查“%s”系统的链接", osType)
	if checkDevice != "" {
		interact.PrintInfo("设备筛选已启用：“%s”", checkDevice)
	}
	result, err := chk.Check()
	if err != nil {
		return fmt.Errorf("检查失败：%w", err)
	}

	// 显示结果
	if mode == checker.CheckSymlink || mode == checker.CheckAll {
		fmt.Printf("\n符号链接：\n")
		fmt.Printf("  总数：%d\n", result.TotalSymlinks)
		fmt.Printf("  有效：%d\n", result.ValidSymlinks)
		fmt.Printf("  无效：%d\n", result.InvalidSymlinks)
	}

	if mode == checker.CheckHardlink || mode == checker.CheckAll {
		fmt.Printf("\n硬链接：\n")
		fmt.Printf("  总数：%d\n", result.TotalHardlinks)
		fmt.Printf("  有效：%d\n", result.ValidHardlinks)
		fmt.Printf("  无效：%d\n", result.InvalidHardlinks)
	}

	if checkLogQuestionOnly {
		interact.PrintSuccess("检查完成")
		return nil
	} else {
		interact.PrintSuccess("检查完成")
	}

	// 如果需要修复，且存在无效链接
	if (checkFix || checkFixAuto) && (result.InvalidSymlinks > 0 || result.InvalidHardlinks > 0) {
		interact.PrintInfo("\n发现无效链接，开始修复...")

		// 确定修复模式
		fixMode := fixer.FixAll
		if checkSymlink && !checkHardlink {
			fixMode = fixer.FixSymlink
		} else if checkHardlink && !checkSymlink {
			fixMode = fixer.FixHardlink
		}

		// 创建修复器
		f := fixer.NewFixer(st, osType, fixMode, checkDevice, checkFixAuto)

		// 执行修复
		fixResult, err := f.Fix()
		if err != nil {
			return fmt.Errorf("修复失败：%w", err)
		}

		// 显示修复结果
		printFixResult(fixResult)
	} else if checkFix || checkFixAuto {
		interact.PrintSuccess("没有发现需要修复的链接")
	}

	return nil
}

// runGlobalCheck 运行全局检查（检查所有记录的位置）
func runGlobalCheck() error {
	// 创建位置管理器
	locMgr, err := location.NewManager()
	if err != nil {
		return fmt.Errorf("创建位置管理器失败：%w", err)
	}

	// 加载位置记录
	if err := locMgr.Load(); err != nil {
		return fmt.Errorf("加载位置记录失败：%w", err)
	}

	// 获取当前操作系统的所有位置
	osType := pathutil.GetCurrentOS()
	locations := locMgr.GetLocations(osType)

	if len(locations) == 0 {
		interact.PrintInfo("没有找到已记录的位置")
		return nil
	}

	interact.PrintInfo("找到 %d 个已记录的位置，开始全局检查", len(locations))
	fmt.Println()

	// 确定检查模式
	var mode checker.CheckMode
	if checkSymlink {
		mode = checker.CheckSymlink
	} else if checkHardlink {
		mode = checker.CheckHardlink
	} else {
		mode = checker.CheckAll
	}

	// 累计结果
	totalSymlinks := 0
	validSymlinks := 0
	invalidSymlinks := 0
	totalHardlinks := 0
	validHardlinks := 0
	invalidHardlinks := 0

	// 遍历所有位置进行检查
	for i, loc := range locations {
		fmt.Printf("【%d/%d】检查位置：“%s”\n", i+1, len(locations), loc)

		// 创建 storage
		st := storage.NewStorage(loc)

		// 检查文件是否存在
		if !st.FileExists() {
			interact.PrintWarning("未找到“file-link-manager-links.json”，跳过")
			fmt.Println()
			continue
		}

		// 如果需要更新家目录
		if checkUpdateHome {
			if err := updateHomePathsForStorage(st); err != nil {
				interact.PrintWarning("更新家目录失败：%v", err)
			}
		}

		// 加载 storage
		if err := st.Load(); err != nil {
			interact.PrintWarning("加载失败：%v，跳过", err)
			fmt.Println()
			continue
		}

		// 创建检查器
		chk := checker.NewChecker(st, osType, mode, checkLogQuestionOnly, checkDevice)

		// 执行检查
		result, err := chk.Check()
		if err != nil {
			interact.PrintWarning("检查失败：%v，跳过", err)
			fmt.Println()
			continue
		}

		// 累计结果
		totalSymlinks += result.TotalSymlinks
		validSymlinks += result.ValidSymlinks
		invalidSymlinks += result.InvalidSymlinks
		totalHardlinks += result.TotalHardlinks
		validHardlinks += result.ValidHardlinks
		invalidHardlinks += result.InvalidHardlinks

		// 显示此位置的结果
		if mode == checker.CheckSymlink || mode == checker.CheckAll {
			fmt.Printf("  符号链接：总数 %d，有效 %d，无效 %d\n",
				result.TotalSymlinks, result.ValidSymlinks, result.InvalidSymlinks)
		}
		if mode == checker.CheckHardlink || mode == checker.CheckAll {
			fmt.Printf("  硬链接：总数 %d，有效 %d，无效 %d\n",
				result.TotalHardlinks, result.ValidHardlinks, result.InvalidHardlinks)
		}

		// 如果需要修复，且存在无效链接
		if (checkFix || checkFixAuto) && (result.InvalidSymlinks > 0 || result.InvalidHardlinks > 0) {
			interact.PrintInfo("  发现无效链接，开始修复...")

			// 确定修复模式
			fixMode := fixer.FixAll
			if checkSymlink && !checkHardlink {
				fixMode = fixer.FixSymlink
			} else if checkHardlink && !checkSymlink {
				fixMode = fixer.FixHardlink
			}

			// 创建修复器
			f := fixer.NewFixer(st, osType, fixMode, checkDevice, checkFixAuto)

			// 执行修复
			fixResult, err := f.Fix()
			if err != nil {
				interact.PrintError("  修复失败：%v", err)
			} else {
				// 显示修复结果
				printFixResult(fixResult)
			}
		}

		fmt.Println()
	}

	// 显示总体结果
	fmt.Println("全局检查总结")
	if mode == checker.CheckSymlink || mode == checker.CheckAll {
		fmt.Printf("符号链接：总数 %d，有效 %d，无效 %d\n", totalSymlinks, validSymlinks, invalidSymlinks)
	}

	if mode == checker.CheckHardlink || mode == checker.CheckAll {
		fmt.Printf("硬链接：总数 %d，有效 %d，无效 %d\n", totalHardlinks, validHardlinks, invalidHardlinks)
	}

	if checkLogQuestionOnly {
		interact.PrintSuccess("检查完成")
	} else {
		interact.PrintSuccess("检查完成")
	}

	return nil
}
