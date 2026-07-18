package cmd

import (
	"os"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var (
	outputFormat string
	WorkDir      string
	verbose      bool
)

var rootCmd = &cobra.Command{
	Use:     "flk",
	Short:   "flk 是一个跨平台的文件链接管理工具",
	Long:    "flk 是一个跨平台的文件链接管理工具",
	Version: Version,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		// -v/--version 直接复用 version 子命令的打印逻辑，打印后正常退出
		// 注意：必须在 PersistentPreRun 之前处理，避免无谓地初始化存储与日志
		if v, _ := cmd.Flags().GetBool("version"); v {
			versionCmd.Run(versionCmd, nil)
			os.Exit(0)
		}
		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {

	},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// 初始化日志配置
		var config *logger.Config
		if verbose {
			config = logger.DefaultConfig() // 详细模式：Trace级别，显示调用者和时间戳
		} else {
			config = &logger.Config{
				Level:      pterm.LogLevelWarn,
				ShowCaller: false,
				ShowTime:   false,
				TimeFormat: "",
				FileOutput: false,
				FilePath:   "",
			}
		}
		logger.Init(config)

		// 设置工作目录用于路径解析
		pathutil.SetWorkDir(WorkDir)
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
	if wd, err := os.Getwd(); err == nil {
		WorkDir = wd
	} else {
		WorkDir = "."
	}
	rootCmd.PersistentFlags().StringVar(
		&store.StorePath,
		"store-path",
		store.DefaultStorePath,
		"用于存放 flk-store.json 的路径",
	)
	rootCmd.PersistentFlags().StringVar(&outputFormat, "output", "table", "输出格式: json/table")
	rootCmd.PersistentFlags().StringVarP(&WorkDir, "work-dir", "w", WorkDir, "工作目录，作为存储和路径计算的基准")
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "启用详细日志输出")
	rootCmd.Flags().BoolP("version", "v", false, "显示版本信息")
}
