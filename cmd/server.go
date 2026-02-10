package cmd

import (
	"github.com/jy-eggroll/flk/internal/logger"

	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "打开网页服务器",
	Long:  "打开网页服务器",
	Run: func(cmd *cobra.Command, args []string) {
	},
}

func init() {
	logger.Init(nil)
	rootCmd.AddCommand(serverCmd)
	serverCmd.Flags().IntP("port", "p", 8999, "指定端口号")
}
