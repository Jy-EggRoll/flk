//go:build windows

package main

import "golang.org/x/sys/windows"

// init 为通用 main 注入 Windows 平台管理员检查能力
// 回调只返回状态，不直接打印；根命令会在 logger 初始化后以 Info/Warn 写入 Cobra 的 stderr
func init() {
	checkWindowsAdmin = isAdminOnWindows
}

// isAdminOnWindows 判断当前进程令牌是否已提升
// IsElevated 不触发 UAC，也不改变权限，仅供根生命周期展示运行状态
func isAdminOnWindows() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
