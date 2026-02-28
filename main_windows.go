//go:build windows

package main

import (
	"golang.org/x/sys/windows"

	"github.com/pterm/pterm"
)

// init 函数：程序启动前为 main.go 的 checkWindowsAdmin 赋值
func init() {
	checkWindowsAdmin = func() {
		if isAdminOnWindows() {
			pterm.Info.Println("当前以管理员权限运行")
		} else {
			pterm.Warning.Println("当前未以管理员权限运行")
		}
	}
}

func isAdminOnWindows() bool {
	elevated := windows.GetCurrentProcessToken().IsElevated()
	return elevated
}
