package cmd

import (
	"github.com/spf13/cobra"

	"github.com/jy-eggroll/flk/internal/logger"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "检查全局软硬链接的生效情况",
	Long:  "检查全局软硬链接的生效情况",
	Run: func(cmd *cobra.Command, args []string) {
		logger.Debug("测试调用 check")
		logger.Info("check 调用成功")
	},
}

func init() {
	logger.Init(nil)
	rootCmd.AddCommand(checkCmd)
	logger.Debug("添加了 check 命令")
}
