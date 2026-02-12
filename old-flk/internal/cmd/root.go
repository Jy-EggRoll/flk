// Package cmd 提供基于 Cobra 的命令行接口
// 本包实现了 flk 命令行工具的所有子命令和参数处理
//
// 全局参数：
//
//	-w, --work-dir <目录>  指定工作目录，类似 git -C 的功能
//	                       这是 file-link-manager-links.json 存储的目录，
//	                       也是计算相对路径的起始目录
//
// 使用示例：
//
//	flk -w /path/to/project check              # 在指定目录下执行检查
//	flk --work-dir D:\MyProject create symlink # 在指定目录下创建链接
package cmd

import (
	"fmt"
	"os"

	"file-link-manager/internal/server"
	"file-link-manager/internal/version"

	"github.com/spf13/cobra"
)

// globalWorkDir 全局工作目录参数
// 当用户指定 -w 或 --work-dir 时，此变量存储用户指定的目录路径
// 空字符串表示使用当前工作目录
var globalWorkDir string

var rootCmd = &cobra.Command{
	Use:   "flk",
	Short: "跨平台文件链接管理器",
	Long:  "flk（FileLinK）是一个跨平台文件链接管理器，支持符号链接和硬链接的创建与管理",
	// Cobra 默认：无参数时显示 help
	// 我们将默认行为改为在没有子命令时直接启动 Web 服务器，便于双击启动
	RunE: func(cmd *cobra.Command, args []string) error {
		// 如果没有传入任何参数（即没有子命令），启动默认 Web 服务器
		if len(args) == 0 {
			// 创建服务器，使用默认端口 12345 以保持与现有命令一致
			srv := server.NewServer(12345) // 使用默认端口创建服务器实例
			return srv.Start()             // 启动服务器并阻塞，若发生错误则返回
		}

		// 否则输出帮助信息
		cmd.Help()
		return nil
	},
}

// Execute 执行根命令
func Execute() { // 定义项目命令行的核心执行入口函数，无入参、无返回值，负责命令汉化配置与根命令执行调度
	rootCmd.InitDefaultHelpCmd()  // 初始化根命令内置的默认 help 子命令，生成标准的 help 命令基础结构，为后续汉化做前置准备
	if rootCmd.HasSubCommands() { // 判断根命令 rootCmd 是否注册有下级子命令，有子命令时才需要对 help 命令做汉化适配处理
		helpCmd, _, _ := rootCmd.Find([]string{"help"}) // 从根命令的命令集合中查找内置的 help 子命令，返回命令对象、子命令切片、查找错误，忽略后两个返回值
		if helpCmd != nil {                             // 判断是否成功查找到内置的 help 命令对象，非空则证明存在原生 help 命令可进行汉化
			helpCmd.Short = "显示指定命令的帮助信息"      // 为 help 命令设置简短的功能描述文本，替换原生英文描述，完成短文本汉化
			helpCmd.Long = "显示有关任何指定命令的详细帮助信息" // 为 help 命令设置完整的功能描述文本，替换原生英文描述，完成长文本汉化
		} // 结束 help 命令对象非空的判断逻辑块，无 help 命令则跳过汉化步骤
	} // 结束根命令是否存在子命令的判断逻辑块，无子命令则无需处理 help 命令汉化

	rootCmd.Flags().BoolP("help", "h", false, "显示指定命令的帮助信息（等同于 help 子命令）") // 为根命令自身单独注册 help 布尔型标志，短参数 h、长参数 help，默认值 false，汉化提示文案，仅对根命令自身生效，做双层兼容配置
	rootCmd.Flags().BoolP("version", "v", false, "显示 flk 版本信息")            // 为根命令自身单独注册 version 布尔型标志，短参数 v、长参数 version，默认值 false，汉化提示文案，仅对根命令自身生效，做双层兼容配置

	if err := rootCmd.Execute(); err != nil { // 执行 Cobra 根命令的核心运行方法，触发整个命令行程序的逻辑执行，捕获命令执行过程中产生的错误对象
		os.Exit(1) // 当命令执行出现错误时，调用系统退出函数并返回状态码 1，代表程序异常终止，告知操作系统执行失败
	} // 结束命令执行错误的判断逻辑块，无错误则程序正常执行并退出
} // 结束 Execute 核心执行函数的代码逻辑块，标志着命令配置与执行方法定义完成

func init() {
	// 允许双击运行
	cobra.MousetrapHelpText = ""

	// 添加全局工作目录参数，类似 git -C 的功能
	// 这个参数允许用户指定 flk 的工作目录，避免频繁 cd
	rootCmd.PersistentFlags().StringVarP(&globalWorkDir, "work-dir", "w", "",
		"指定工作目录（存储 file-link-manager-links.json 的目录，也是计算相对路径的起始目录）")

	// 设置版本信息
	rootCmd.Version = version.Version
	rootCmd.SetVersionTemplate(fmt.Sprintf("%s\nflk（FileLinK）是一个跨平台文件链接管理器，支持符号链接和硬链接的创建与管理\n作者：%s\n",
		version.Version,
		version.Author))

	// 禁用默认的 completion 命令
	// rootCmd.CompletionOptions.DisableDefaultCmd = true

	// 	// 重写默认的帮助和使用信息
	// 	rootCmd.SetHelpTemplate(`{{.Long}}

	// 用法：
	// {{.UseLine}}
	// {{if .HasAvailableSubCommands}}
	// 可用命令：
	// {{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}  {{rpad .Name .NamePadding }} {{.Short}}{{end}}
	// {{end}}{{else}}
	// 无可用命令
	// {{end}}
	// {{if .HasAvailableLocalFlags}}标志：
	// {{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{else}}无可用标志{{end}}

	// 使用“{{.CommandPath}} [命令] --help”获取有关命令的更多信息
	// `)
}
