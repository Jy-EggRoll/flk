// server.go 实现 Web 服务器命令
// 提供 flk server --port 12345 命令启动 Web 界面
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"file-link-manager/internal/server"
)

// serverPort 服务器端口号，默认 12345
var serverPort int

// serverCmd 定义 server 子命令
// 用于启动 Web 服务器，提供图形化界面管理链接
var serverCmd = &cobra.Command{
	Use:   "server",     // 命令名称，在命令行中使用 flk server 调用
	Short: "启动 Web 服务器", // 简短描述，显示在帮助列表中
	Long: `启动 Web 服务器，提供图形化界面管理链接

通过浏览器访问 http://localhost:<端口号> 可以：
  - 查看所有链接的状态
  - 刷新检查链接有效性
  - 创建新的符号链接或硬链接

注意：创建符号链接需要管理员权限，会弹出 UAC 提示`, // 详细描述，使用 --help 时显示
	RunE: runServer, // RunE 表示运行函数可能返回错误
}

// init 函数在包加载时自动执行
// 用于注册 server 命令和设置命令行标志
func init() {
	rootCmd.AddCommand(serverCmd) // 将 server 命令添加到根命令下

	// 添加 --port 标志
	// IntVarP 参数说明：
	// &serverPort - 绑定到 serverPort 变量
	// "port" - 长标志名（--port）
	// "p" - 短标志名（-p）
	// 12345 - 默认值
	// "服务器端口号" - 帮助描述
	serverCmd.Flags().IntVarP(&serverPort, "port", "p", 12345, "服务器端口号")
}

// runServer 执行 server 命令的核心逻辑
// cmd 是 Cobra 命令对象，args 是命令行参数
func runServer(cmd *cobra.Command, args []string) error {
	// 验证端口号范围
	// 端口号必须在 1-65535 之间
	// 1-1023 是系统保留端口，通常需要管理员权限
	if serverPort < 1 || serverPort > 65535 {
		return fmt.Errorf("端口号必须在 1-65535 之间，当前值：%d", serverPort)
	}

	// 创建并启动 Web 服务器
	// server.NewServer 创建一个新的服务器实例
	srv := server.NewServer(serverPort)

	// 启动服务器
	// 这是一个阻塞调用，服务器会一直运行直到出错或被中断
	return srv.Start()
}
