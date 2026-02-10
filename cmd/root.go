package cmd

import (
	"os"

	"github.com/jy-eggroll/flk/internal/logger"

	// "github.com/pterm/pterm"
	"github.com/spf13/cobra"
	// "github.com/spf13/viper"
)

var (
	lang    string
	cfgFile string
)

var rootCmd = &cobra.Command{
	Use:   "flk",
	Short: "flk 是一个跨平台的文件链接管理工具",
	Long:  "flk 是一个跨平台的文件链接管理工具",
	// Uncomment the following line if your bare application
	// has an action associated with it:
	Run: func(cmd *cobra.Command, args []string) {

	},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {

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
	rootCmd.PersistentFlags().StringVar(&lang, "lang", "", "选择语言")
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "配置文件的路径")
}
