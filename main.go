package main

import (
	"github.com/jy-eggroll/flk/internal/logger"

	"github.com/jy-eggroll/flk/cmd"
)

func main() {
	logger.Init(nil)
	logger.Info("欢迎使用 flk！")
	// if runtime.GOOS == "windows" {
	// 	if !isAdminOnWindows() {
	// 		pterm.Println(pterm.Red("请使用管理员权限运行 flk，这是由于 Windows 平台必须使用管理员权限才能创建符号链接，如果您使用 24H2 以上版本，可以考虑开启 sudo"))
	// 		os.Exit(0)
	// 	}
	// }
	cmd.Execute()
}

// func isAdminOnWindows() bool {
// 	elevated := windows.GetCurrentProcessToken().IsElevated()
// 	// fmt.Printf("admin %v\n", elevated)
// 	return elevated
// }
