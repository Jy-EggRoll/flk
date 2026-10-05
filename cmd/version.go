package cmd

import (
	"fmt"
	"io"
	"runtime"

	"github.com/jy-eggroll/eggokit/l10n"

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
	platform := platformLabel()
	text := l10n.T("Version: {{.Version}}\nBuild time: {{.Time}}\nPlatform: {{.Platform}}", map[string]any{"Version": Version, "Time": BuildTime, "Platform": platform})
	_, err := fmt.Fprintln(writer, text)
	return err
}

// platformLabel 产出面向用户展示的当前平台标签，形如 linux-amd64
// 抽取理由：version 与 upgrade 两处曾逐字重复同一段拼接，且当 GOOS 为 windows 时都要追加 (exe) 后缀
// 重复的不只是 Sprintf 一行，而是包含后缀判断在内的整块逻辑，因此这里连后缀一起收进同一份实现，两处才真正零重复
// 潜在影响点：这个标签只用于展示，必须保持与抽取前完全一致的历史文案（含 Windows 后缀）
// 不要用本函数去替换 internal/updater/release.go 中按 GOOS/GOARCH 匹配发布资产名的逻辑，
// 那里是「选哪个发布产物」的构建坐标，与这里「给用户看的名字」语义不同，合并会破坏资产匹配
func platformLabel() string {
	platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		platform += " (exe)"
	}
	return platform
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
