package cmd

import (
	"os"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	// "github.com/spf13/viper"
)

var (
	lang    string
	logger  = pterm.DefaultLogger.WithLevel(pterm.LogLevelTrace) // 默认以 Trace 级别输出
	cfgFile string
)

var rootCmd = &cobra.Command{
	Use:   "flk",
	Short: "简短的描述，在哪里出现？",
	Long: `flk
的
详细描述`,
	// Uncomment the following line if your bare application
	// has an action associated with it:
	Run: func(cmd *cobra.Command, args []string) {
		logger.Trace("root 被调用了")
		logger.Trace("此时 lang 的值为 " + lang)
	},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		logger.Trace("root 的前置函数被调用了")
		logger.Trace("此时 lang 的值为 " + lang)
	},
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&lang, "lang", "", "选择语言")
	logger.Trace("添加了语言选项")
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "配置文件的路径")
	logger.Trace("添加了配置文件选项")
}
