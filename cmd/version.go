package cmd

import (
	"io"

	"github.com/jy-eggroll/eggokit/ui"
)

// Version 由发布构建通过链接参数注入；本地开发构建保留 dev
var Version = "dev"

// BuildTime 由发布构建通过链接参数注入；本地开发构建保留 unknown
var BuildTime = "unknown"

// versionOptions 是 version 命令与根命令 --version 共用的一份取值。
//
// 两处各建一份的话，版本来源或展示参数就可能只在一处更新，两个入口的输出会不一致。
// Version 与 BuildTime 的名称与所在包同时被 Taskfile 的 -X 链接参数和 release.json 的
// ldflagsPackage 引用，改名或移包会让发布二进制失去版本信息，因此这里只取值，不搬变量。
var versionOptions = ui.VersionOptions{
	Program:   "flk",
	Version:   func() string { return Version },
	BuildTime: func() string { return BuildTime },
}

// versionCmd 的渲染、别名 ver 与帮助文案都由共享包提供，flk 侧不再保留另一份实现
var versionCmd = ui.NewVersionCommand(versionOptions)

// renderVersion 是根命令 --version 的分支，与 version 命令渲染同一份取值。
// writer 由 Cobra 提供，默认指向 stdout，也允许测试或嵌入方注入缓冲区；该函数不读取 store、不终止进程
func renderVersion(writer io.Writer) error {
	return ui.RenderVersion(writer, versionOptions)
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
