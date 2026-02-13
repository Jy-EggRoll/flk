//go:build windows

package main

import (
	"golang.org/x/sys/windows"

	"github.com/jy-eggroll/flk/internal/logger"
)

// init 函数：程序启动前为 main.go 的 checkWindowsAdmin 赋值
func init() {
	checkWindowsAdmin = func() {
		if isAdminOnWindows() {
			logger.Info("当前以管理员权限运行")
		} else {
			logger.Warn("当前未以管理员权限运行")
		}
	}
}

func isAdminOnWindows() bool {
	elevated := windows.GetCurrentProcessToken().IsElevated()
	return elevated
}
