package main

import (
	"os"

	"github.com/jy-eggroll/flk/cmd"
)

// checkWindowsAdmin 由 main_windows.go 在 Windows 构建中注入真实实现
// 其他平台保持 nil，根命令生命周期据此完全跳过管理员状态提示
var checkWindowsAdmin func() bool

func main() {
	// main 只负责注入平台能力并把命令层退出码交给操作系统
	// 参数解析、输出模式、日志、提示与错误渲染均必须发生在 Cobra 生命周期内，避免解析前输出污染 stdout
	cmd.SetWindowsAdminChecker(checkWindowsAdmin)
	os.Exit(cmd.Execute())
}
