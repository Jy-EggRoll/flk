package cmd

import (
	"fmt"
	"io"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/upgradecli"

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
	// 平台标签与升级命令共用上游的同一份实现，保证「flk version」与「检查更新」显示的
	// 名字永远一致（含 Windows 下的 (exe) 后缀）；这个标签只用于展示，
	// 不要拿它替换按 GOOS/GOARCH 匹配发布资产名的逻辑，两者语义不同
	platform := upgradecli.PlatformLabel()
	text := l10n.T("Version: {{.Version}}\nBuild time: {{.Time}}\nPlatform: {{.Platform}}", map[string]any{"Version": Version, "Time": BuildTime, "Platform": platform})
	_, err := fmt.Fprintln(writer, text)
	return err
}

// 平台标签的实现已随升级命令一起搬到 eggokit/upgradecli，本文件不再保留本地副本
func init() {
	rootCmd.AddCommand(versionCmd)
}
