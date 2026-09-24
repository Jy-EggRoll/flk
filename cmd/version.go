package cmd

import (
	"fmt"
	"io"
	"runtime"

	"github.com/jy-eggroll/flk/pkg/l10n"

	"github.com/spf13/cobra"
)

// Version 由发布构建通过链接参数注入；本地开发构建保留 dev
var Version = "dev"

// BuildTime 由发布构建通过链接参数注入；本地开发构建保留 unknown
var BuildTime = "unknown"

var versionCmd = &cobra.Command{
	Use:     "version",
	Aliases: []string{"ver"},
	Short:   l10n.T("Display version information", nil),
	Long:    l10n.T("Display version information", nil),
	RunE: func(cmd *cobra.Command, args []string) error {
		return renderVersion(cmd.OutOrStdout())
	},
}

// renderVersion 是 flk --version 与 flk version/ver 的唯一渲染实现
// writer 由 Cobra 提供，默认指向 stdout，也允许测试或嵌入方注入缓冲区；该函数不读取 store、不终止进程
func renderVersion(writer io.Writer) error {
	platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		platform += " (exe)"
	}
	text := l10n.T("Version: {{.Version}}\nBuild time: {{.Time}}\nPlatform: {{.Platform}}", map[string]any{"Version": Version, "Time": BuildTime, "Platform": platform})
	_, err := fmt.Fprintln(writer, text)
	return err
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
