package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/jy-eggroll/flk/internal/create/symlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
)

var (
	symlinkReal string
	symlinkFake string
)

var symlinkCmd = &cobra.Command{
	Use:   "symlink",
	Short: "创建符号链接（支持文件和文件夹）",
	Long:  "创建符号链接（支持文件和文件夹）",
	RunE:  Symlink,
}

func init() {
	createCmd.AddCommand(symlinkCmd)
	symlinkCmd.Flags().StringVarP(&symlinkReal, "real", "r", "", "真实文件路径")
	symlinkCmd.Flags().StringVarP(&symlinkFake, "fake", "f", "", "链接文件路径")
	symlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	symlinkCmd.Flags().StringVar(&createDevice, "device", "all", "设备名称，用于后续设备过滤检查")
	symlinkCmd.MarkFlagRequired("real")
	symlinkCmd.MarkFlagRequired("fake")
}

func Symlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)

	normalizedReal, err := pathutil.NormalizePath(symlinkReal)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "符号链接", Error: "真实文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	var normalizedFake string
	normalizedFake, err = pathutil.NormalizePath(symlinkFake)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "符号链接", Error: "链接文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	logger.Info("创建符号链接: real=" + normalizedReal + ", fake=" + normalizedFake)

	// 如果Windows且不是管理员，提权
	if runtime.GOOS == "windows" && !isAdminOnWindowsForCreate() {
		logger.Info("使用提权创建符号链接")
		return runElevatedSymlinkForCreate()
	}

	var result output.CreateResult
	if err := symlink.Create(normalizedReal, normalizedFake, createForce); err != nil {
		result = output.CreateResult{Success: false, Type: "符号链接", Error: err.Error()}
	} else {
		result = output.CreateResult{Success: true, Type: "符号链接", Message: "创建成功"}
		// 持久化数据
		if store.GlobalManager == nil {
			if err := store.InitStore(store.StorePath); err != nil {
				logger.Error("初始化存储失败：" + err.Error())
			}
		}
		mgr := store.GlobalManager
		if mgr != nil {
			absFakePath, _ := pathutil.ToAbsolute(normalizedFake)
			fields := map[string]string{
				"real": normalizedReal,
				"fake": absFakePath,
			}
			parentPath, _ := os.Getwd()
			mgr.AddRecord(createDevice, "symlink", parentPath, fields)
			if err := mgr.Save(store.StorePath); err != nil {
				logger.Error("持久化失败：" + err.Error())
			}
		}
	}
	output.PrintCreateResult(format, result)
	if result.Success {
		return nil
	}
	return errors.New(result.Error)
}

func runElevatedSymlinkForCreate() error {
	// 检查是否已经是管理员
	if isAdminOnWindowsForCreate() {
		return Symlink(nil, nil)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取可执行文件路径失败: %w", err)
	}

	// 如果是 go run 临时文件，复制到新位置避免清理冲突
	if strings.Contains(exe, "go-build") {
		tempExe, err := copyToTempForCreate(exe)
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

	// 使用 PowerShell 提权
	command := fmt.Sprintf("Start-Process -Verb RunAs -FilePath '%s' -ArgumentList \"create symlink --real '%s' --fake '%s' --force --device '%s'\" -Wait -WindowStyle Hidden -WorkingDirectory '%s'", exe, symlinkReal, symlinkFake, createDevice, cwd)
	cmd := exec.Command("powershell.exe", "-Command", command)
	return cmd.Run()
}

func copyToTempForCreate(src string) (string, error) {
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

func isAdminOnWindowsForCreate() bool {
	if runtime.GOOS != "windows" {
		return true // 非 Windows 假设有权限
	}
	elevated := windows.GetCurrentProcessToken().IsElevated()
	return elevated
}
