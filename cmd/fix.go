package cmd

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
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
	// 复用check的flags
	fixCmd.Flags().StringVarP(&fixDevice, "device", "d", "", "设备名称，用于过滤检查，可用逗号分隔多个设备")
	fixCmd.Flags().BoolVar(&fixSymlink, "symlink", false, "仅检查符号链接")
	fixCmd.Flags().BoolVar(&fixHardlink, "hardlink", false, "仅检查硬链接")
	fixCmd.Flags().StringVar(&fixDir, "dir", "", "仅检查包含该路径的记录")
}

var (
	fixDevice   string
	fixSymlink  bool
	fixHardlink bool
	fixDir      string
)

func RunFix(cmd *cobra.Command, args []string) {
	checkAndDisplay := func() []output.CheckResult {
		deviceFilters := parseDeviceFilters(fixDevice)
		results, err := performCheck(CheckOptions{
			DeviceFilters: deviceFilters,
			CheckSymlink:  fixSymlink,
			CheckHardlink: fixHardlink,
			CheckDir:      fixDir,
		})
		if err != nil {
			logger.Error("检查失败: " + err.Error())
			return nil
		}

		// 过滤无效结果
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

	// 交互循环
	for {
		pterm.DefaultBox.WithTitle("INFO").Println(pterm.Green("输入 all 或 a 修复所有\n输入 d<编号> 删除条目，如 d7，单次只能删除一个\n输入 exit 或 e 退出程序\n输入数字以修复对应项\n使用空格分隔"))
		input, err := pterm.DefaultInteractiveTextInput.WithMultiLine(false).Show("请输入")
		if err != nil {
			logger.Error("输入错误 " + err.Error())
			continue
		}

		input = strings.TrimSpace(input)
		if input == "exit" || input == "e" {
			break
		}

		if strings.HasPrefix(input, "d") {
			// 删除模式
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
				}
				mgr.RemoveMatchingEntry(platform, result.Device, result.Type, result.Path, entry)
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

		// 修复选中的
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
	if verbose {
		logger.Info(fmt.Sprintf("开始修复 #%d, 类型=%s, 设备=%s, 路径=%s, BasePath=%s, Real=%s, Fake=%s", idx+1, result.Type, result.Device, result.Path, result.BasePath, result.Real, result.Fake))

		// 从存储中读取的父键是 result.Path，规范化后是 result.BasePath
		logger.Info(fmt.Sprintf("读取条目父键: path='%s', 规范化BasePath='%s'", result.Path, result.BasePath))

		// 临时设置 workDir 为条目父路径，以便相对路径正确解析
		oldWorkDir := WorkDir
		WorkDir = result.BasePath
		logger.Info(fmt.Sprintf("临时设置 WorkDir 从 '%s' 到 '%s' (条目父路径)", oldWorkDir, WorkDir))
		defer func() {
			WorkDir = oldWorkDir
			logger.Info(fmt.Sprintf("恢复 WorkDir 到 '%s'", oldWorkDir))
		}()
	} else {
		// 临时设置 workDir 为条目父路径，以便相对路径正确解析
		oldWorkDir := WorkDir
		WorkDir = result.BasePath
		defer func() {
			WorkDir = oldWorkDir
		}()
	}

	switch result.Type {
	case "symlink":
		// 临时设置全局变量
		oldReal := symlinkReal
		oldFake := symlinkFake
		oldForce := createForce
		oldDevice := createDevice

		symlinkReal = result.Real
		if !filepath.IsAbs(symlinkReal) {
			symlinkReal = filepath.Join(result.BasePath, symlinkReal)
		}
		symlinkFake = result.Fake
		createForce = true
		createDevice = result.Device

		defer func() {
			symlinkReal = oldReal
			symlinkFake = oldFake
			createForce = oldForce
			createDevice = oldDevice
		}()
		return Symlink(nil, nil)
	case "hardlink":
		oldPrim := hardlinkPrim
		oldSeco := hardlinkSeco
		oldForce := createForce
		oldDevice := createDevice

		hardlinkPrim = result.Prim
		if !filepath.IsAbs(hardlinkPrim) {
			hardlinkPrim = filepath.Join(result.BasePath, hardlinkPrim)
		}
		hardlinkSeco = result.Seco
		if !filepath.IsAbs(hardlinkSeco) {
			hardlinkSeco = filepath.Join(result.BasePath, hardlinkSeco)
		}
		createForce = true
		createDevice = result.Device

		defer func() {
			hardlinkPrim = oldPrim
			hardlinkSeco = oldSeco
			createForce = oldForce
			createDevice = oldDevice
		}()
		return Hardlink(nil, nil)
	}
	return fmt.Errorf("未知类型 %s", result.Type)
}
