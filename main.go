package main

import (
	"os"
	"runtime"

	"github.com/jy-eggroll/flk/cmd"
	"github.com/pterm/pterm"
)

// 声明 Windows 专属的管理员检查函数（由 main_windows.go 赋值）
var checkWindowsAdmin func()

// isJSONOutput 扫描命令行参数，判断本次调用是否请求 JSON 输出
// 目的：JSON 模式下 stdout 必须是可被机器解析的纯 JSON，
// 因此要抑制「欢迎使用 flk！」及 Windows 管理员提示等装饰性文本，避免污染输出
// 这里在 cobra 解析前手动扫描 os.Args，兼容 --output json 与 --output=json 两种写法
func isJSONOutput() bool {
	for i, arg := range os.Args {
		if arg == "--output=json" {
			return true
		}
		if arg == "--output" && i+1 < len(os.Args) && os.Args[i+1] == "json" {
			return true
		}
	}
	return false
}

func main() {
	jsonMode := isJSONOutput()

	// 通用初始化逻辑（全平台执行）；JSON 模式下抑制欢迎语，保持 stdout 纯净
	if !jsonMode {
		pterm.Info.Println("欢迎使用 flk！")
	}

	// 仅 Windows 平台执行管理员权限检查（非 Windows 平台此逻辑自动跳过）
	// 同样在 JSON 模式下跳过，避免权限提示文本混入输出
	if !jsonMode && runtime.GOOS == "windows" && checkWindowsAdmin != nil {
		checkWindowsAdmin()
	}

	// 通用业务入口（全平台执行）
	cmd.Execute()
}
