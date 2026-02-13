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
			logger.Warn("当前未以管理员权限运行")
		}
	}
	cmd.Execute()
}

func isAdminOnWindows() bool {
	elevated := windows.GetCurrentProcessToken().IsElevated()
	return elevated
}
