package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// 由 GitHub 构建时自动注入
var Version = "dev"

// 由 GitHub 构建时自动注入
var BuildTime = "unknown"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "显示版本信息",
	Long:  "显示版本信息",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("版本：%s\n构建时间：%s\n", Version, BuildTime)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
