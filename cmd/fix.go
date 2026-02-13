package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
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
	fixCmd.Flags().StringVar(&fixDevice, "device", "", "设备名称，用于过滤检查")
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
	logger.Info("开始修复模式...")

	results, err := performCheck(CheckOptions{
		DeviceFilter:  fixDevice,
		CheckSymlink:  fixSymlink,
		CheckHardlink: fixHardlink,
		CheckDir:      fixDir,
	})
	if err != nil {
		logger.Error("检查失败：" + err.Error())
		return
	}

	// 过滤无效结果
	var invalidResults []output.CheckResult
	for _, r := range results {
		if !r.Valid {
			invalidResults = append(invalidResults, r)
		}
	}

	if len(invalidResults) == 0 {
		pterm.Info.Println("所有链接都有效，无需修复")
		return
	}

	// 显示带编号的table
	format := output.OutputFormat(outputFormat)
	if err := output.PrintCheckResults(format, invalidResults); err != nil {
		logger.Error("输出失败：" + err.Error())
		return
	}

	// 交互循环
	for {
		input, err := pterm.DefaultInteractiveTextInput.WithMultiLine(false).Show("输入要修复的编号（空格分隔），'all' 或 'a' 修复所有，'exit' 或 'e' 退出")
		if err != nil {
			logger.Error("输入错误：" + err.Error())
			continue
		}

		input = strings.TrimSpace(input)
		if input == "exit" || input == "e" {
			break
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
					pterm.Warning.Printf("无效编号: %s\n", part)
					continue
				}
				indices = append(indices, idx-1) // 1-based to 0-based
			}
		}

		if len(indices) == 0 {
			continue
		}

		// 修复选中的
		for _, idx := range indices {
			result := invalidResults[idx]
			if err := repairResult(result, idx); err != nil {
				pterm.Error.Printf("修复失败 #%d: %v\n", idx+1, err)
			} else {
				pterm.Success.Printf("修复成功 #%d\n", idx+1)
			}
		}

		// 重新检查？
		// 暂时不重新检查，保持简单
	}
}

func repairResult(result output.CheckResult, idx int) error {
	logger.Info(fmt.Sprintf("开始修复 #%d: 类型=%s, 设备=%s, 路径=%s, BasePath=%s, Real=%s, Fake=%s", idx+1, result.Type, result.Device, result.Path, result.BasePath, result.Real, result.Fake))
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

		logger.Info(fmt.Sprintf("修复参数: symlinkReal=%s, symlinkFake=%s, createForce=%t, createDevice=%s", symlinkReal, symlinkFake, createForce, createDevice))

		defer func() {
			symlinkReal = oldReal
			symlinkFake = oldFake
			createForce = oldForce
			createDevice = oldDevice
		}()

		// 如果Windows，提权
		if runtime.GOOS == "windows" {
			logger.Info("使用提权运行")
			return runElevatedSymlink()
		}

		logger.Info("正常运行 Symlink")
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
	return fmt.Errorf("未知类型: %s", result.Type)
}

func runElevatedSymlink() error {
	// 检查是否已经是管理员
	if isAdminOnWindows() {
		return Symlink(nil, nil)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取可执行文件路径失败: %w", err)
	}

	// 如果是 go run 临时文件，复制到新位置避免清理冲突
	if strings.Contains(exe, "go-build") {
		tempExe, err := copyToTemp(exe)
		if err != nil {
			return fmt.Errorf("复制 exe 到临时位置失败: %w", err)
		}
		defer os.Remove(tempExe) // 清理临时文件
		exe = tempExe
	}

	// 获取当前工作目录
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("获取工作目录失败: %w", err)
	}

	// 使用 -Command 传递命令
	command := fmt.Sprintf("Start-Process -Verb RunAs -FilePath '%s' -ArgumentList \"create symlink --real '%s' --fake '%s' --force --device '%s'\" -Wait -WindowStyle Hidden -WorkingDirectory '%s'", exe, symlinkReal, symlinkFake, createDevice, cwd)
	cmd := exec.Command("powershell.exe", "-Command", command)
	return cmd.Run()
}

func copyToTemp(src string) (string, error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer srcFile.Close()

	tempFile, err := os.CreateTemp("", "flk-elevated-*.exe")
	if err != nil {
		return "", err
	}
	defer tempFile.Close()

	_, err = io.Copy(tempFile, srcFile)
	if err != nil {
		os.Remove(tempFile.Name())
		return "", err
	}

	return tempFile.Name(), nil
}

func isAdminOnWindows() bool {
	if runtime.GOOS != "windows" {
		return true // 非 Windows 假设有权限
	}
	elevated := windows.GetCurrentProcessToken().IsElevated()
	return elevated
}
