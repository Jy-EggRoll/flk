//go:build windows

package main

import (
	"os"

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
			if len(os.Args) == 1 {
				pterm.Info.Println("检测到直接运行，正在尝试获取管理员权限...")
				exe, err := os.Executable()
				if err != nil {
					pterm.Error.Println("获取可执行文件路径失败:", err)
					return
				}

				verb, _ := windows.UTF16PtrFromString("runas")
				file, _ := windows.UTF16PtrFromString(exe)

				err = windows.ShellExecute(0, verb, file, nil, nil, windows.SW_NORMAL)
				if err != nil {
					pterm.Error.Println("提权失败:", err)
				} else {
					os.Exit(0)
				}
			}
		}
	}
}

func isAdminOnWindows() bool {
	elevated := windows.GetCurrentProcessToken().IsElevated()
	return elevated
}
