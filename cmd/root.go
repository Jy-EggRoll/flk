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
	Version: "",
	PreRunE: func(cmd *cobra.Command, args []string) error {
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

		// 校验 --output 取值：仅支持 json / table，非法值静默落到 switch 之外会导致无任何输出
		// 这里统一回退为 table 并给出 warn，避免用户因拼写错误而看不到结果
		if outputFormat != "json" && outputFormat != "table" {
			logger.Warn("未知的输出格式 " + outputFormat + "，已回退为 table")
			outputFormat = "table"
		}

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
