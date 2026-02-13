package main

import (
	"runtime"

	"github.com/jy-eggroll/flk/cmd"
	"github.com/jy-eggroll/flk/internal/logger"
)

// 声明 Windows 专属的管理员检查函数（由 main_windows.go 赋值）
var checkWindowsAdmin func()

func main() {
	// 通用初始化逻辑（全平台执行）
	logger.Init(nil)
	logger.Info("欢迎使用 flk！")

	// 仅 Windows 平台执行管理员权限检查（非 Windows 平台此逻辑自动跳过）
	if runtime.GOOS == "windows" && checkWindowsAdmin != nil {
		checkWindowsAdmin()
	}

	// 通用业务入口（全平台执行）
	cmd.Execute()
}