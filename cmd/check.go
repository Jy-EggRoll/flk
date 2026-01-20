package cmd

import (
	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "检查全局软硬链接的生效情况",
	Long:  "检查全局软硬链接的生效情况",
	Run: func(cmd *cobra.Command, args []string) {
		logger.Trace("测试调用 check")
		logger.Info("check 调用成功")
	},
}

func init() {
	rootCmd.AddCommand(checkCmd)
	logger.Trace("添加了 check 命令")
}
