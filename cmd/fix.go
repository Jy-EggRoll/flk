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
	Use:   "fix",
	Short: "交互式修复无效链接",
	Long:  "检查链接状态并进入交互模式，允许用户选择编号修复无效链接",
	Run:   RunFix,
}

func init() {
	rootCmd.AddCommand(fixCmd)
	// 复用check的flags
	fixCmd.Flags().StringVarP(&fixDevice, "device", "d","", "设备名称，用于过滤检查")
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
		results, err := performCheck(CheckOptions{
			DeviceFilter:  fixDevice,
			CheckSymlink:  fixSymlink,
			CheckHardlink: fixHardlink,
			CheckDir:      fixDir,
		})
		if err != nil {
			logger.Error("检查失败：" + err.Error())
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
			if err := output.PrintCheckResults(format, invalidResults); err != nil {
				logger.Error("输出失败：" + err.Error())
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
		input, err := pterm.DefaultInteractiveTextInput.WithMultiLine(false).Show("输入要修复的编号（空格分隔），'all' 或 'a' 修复所有，'d<编号>' 删除条目，如 d7，单次只能删除一个，'exit' 或 'e' 退出")
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
	logger.Info(fmt.Sprintf("开始修复 #%d, 类型=%s, 设备=%s, 路径=%s, BasePath=%s, Real=%s, Fake=%s", idx+1, result.Type, result.Device, result.Path, result.BasePath, result.Real, result.Fake))
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
