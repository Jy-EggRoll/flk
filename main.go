package main

import (
	"runtime"

	"github.com/jy-eggroll/flk/internal/logger"
	"golang.org/x/sys/windows"

	"github.com/jy-eggroll/flk/cmd"
)

func main() {
	logger.Init(nil)
	logger.Info("欢迎使用 flk！")
	if runtime.GOOS == "windows" {
		if isAdminOnWindows() {
			logger.Info("当前以管理员权限运行")
		} else {
			logger.Warn("当前未以管理员权限运行 必定无法创建符号链接 但是不影响创建硬链接 建议以管理员权限运行 这样才可以在 Windows 平台上实现全部功能 如果您使用 24H2 版本及以上的系统 可以考虑开启 sudo")
		}
	}
	cmd.Execute()
}

func isAdminOnWindows() bool {
	elevated := windows.GetCurrentProcessToken().IsElevated()
	return elevated
}
