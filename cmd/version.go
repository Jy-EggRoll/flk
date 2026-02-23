package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version 应用版本号
const Version = "0.0.6"

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "显示 flk 版本信息",
	Long:  "显示 flk 工具的版本信息",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("flk 的当前版本为 %s\n", Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}