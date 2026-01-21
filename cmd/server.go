/*
Copyright © 2026 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/pterm/pterm"

	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: func(cmd *cobra.Command, args []string) {
		logger.Debug("server 命令被调用了")
		logger.Debug("当前端口号为：" + fmt.Sprint(cmd.Flags().Lookup("port").Value))
	},
}

func init() {
	logger.Init(nil)
	logger.SetLevel(pterm.LogLevelInfo)
	rootCmd.AddCommand(serverCmd)
	logger.Debug("添加了 server 命令")
	serverCmd.Flags().IntP("port", "p", 8999, "指定端口号")
	logger.Debug("添加了端口选项")
}
