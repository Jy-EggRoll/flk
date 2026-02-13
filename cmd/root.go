package cmd

import (
	"os"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/store"

	"github.com/spf13/cobra"
)

var (
	outputFormat string
)

var rootCmd = &cobra.Command{
	Use:   "flk",
	Short: "flk 是一个跨平台的文件链接管理工具",
	Long:  "flk 是一个跨平台的文件链接管理工具",
	Run: func(cmd *cobra.Command, args []string) {

	},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// 在命令执行前初始化持久化存储，使用当前 storePath 配置
		if err := store.InitStore(store.StorePath); err != nil {
			logger.Error("初始化存储失败 " + err.Error())
		}
	},
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	logger.Init(nil)
	rootCmd.PersistentFlags().StringVar(
		&store.StorePath,
		"storePath",
		store.DefaultStorePath,
		"用于存放 flk-store.json 的路径",
	)
	rootCmd.PersistentFlags().StringVar(&outputFormat, "output", "table", "输出格式：json/table")
}
