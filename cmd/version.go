package cmd

import (
	"fmt"
	"runtime"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

// 由 GitHub 构建时自动注入
var Version = "dev"

// 由 GitHub 构建时自动注入
var BuildTime = "unknown"

var versionCmd = &cobra.Command{
	Use:     "version",
	Aliases: []string{"ver"},
	Short:   "显示版本信息",
	Long:    "显示版本信息",
	Run: func(cmd *cobra.Command, args []string) {
		platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
		if runtime.GOOS == "windows" {
			platform += " (exe)"
		}
		pterm.Info.Println("版本: " + Version)
		pterm.Info.Println("构建时间: " + BuildTime)
		pterm.Info.Println("平台: " + platform)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}