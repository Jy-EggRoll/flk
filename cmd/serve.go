package cmd

import (
	"github.com/spf13/cobra"
)

/*
serveCmd 是 serve 相关子命令的父命令，本身不做事
设计为"serve 子命令树"的根节点，后续 subcommand 扩展（如 config）挂在此命令下
*/
var serveCmd = &cobra.Command{
	Use:     "serve",
	Aliases: []string{"server"},
	Short:   "打开网页服务",
	Long:    "打开网页服务，提供可视化管理界面。\n使用 serve config 子命令以网页形式展示配置文件。",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.PersistentFlags().IntP("port", "p", 8999, "指定端口号")
	serveCmd.PersistentFlags().String("host", "127.0.0.1", "指定绑定的 Host")
}
