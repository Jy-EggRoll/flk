// lang.go 负责在命令行解析之前确定输出语言，并把静态文案重译成当前语言。
//
// 为什么必须提前于 cobra 解析：语言的生效范围包括各命令的 Short/Long 与 flag 说明，
// 而 cobra 的 --help 路径不会执行 PersistentPreRunE（那些文本要立即翻译好才能输出），
// 因此语言取值不能依赖 cobra 的解析结果，只能自行预扫描原始参数。
//
// flk 与 ggt 的一个关键差异：flk 的命令是**包级变量**，在包初始化阶段就已构造完毕，
// 其中 Short/Long 与 flag 说明里的 l10n.T(...) 在 l10n.Init 之前求值，当时 localizer
// 尚未就绪，T 只能返回英文源串。因此 Init 之后必须再走一遍 localizeTree 把这些
// 静态文案重译一次。运行期才调用的 T（pterm 输出、错误信息）不受此影响。
package cmd

import (
	"io"
	"os"
	"strings"

	"github.com/jy-eggroll/flk/internal/config"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// langEnv 是语言的环境变量名，与 logger 的 FLK_LOG_LEVEL 属同一类约定。
// 引入它是为了让用户在不想改配置文件时也能临时切换语言，而不必新增一整套配置命令。
const langEnv = "FLK_LANG"

// chooseLanguage 按优先级挑出要使用的语言串（**未经归一化**）：
//
//	命令行 --lang/-l  >  环境变量 FLK_LANG  >  配置文件 language 字段  >  空串（交由 l10n 用默认语言）
//
// 刻意不在这里归一化：语言白名单属于 l10n.Options，而 Options 要交给 l10n.Init，
// 归一化在 Init 内部完成即可——否则这里就得再持有并手工维护一份语言列表，
// 两份列表迟早会漂移。
//
// 任何一步失败都静默降级、绝不返回错误：语言只影响展示，不该让命令整体失败。
// 尤其是配置文件损坏时也必须能正常输出帮助——这是 --help 路径会走到这里的前提。
func chooseLanguage() string {
	if lang := scanLangFlag(os.Args[1:]); lang != "" {
		return lang
	}
	if lang := strings.TrimSpace(os.Getenv(langEnv)); lang != "" {
		return lang
	}
	if lang, err := config.LoadLanguage(); err == nil && lang != "" {
		return lang
	}
	return ""
}

// scanLangFlag 从原始命令行参数里预扫描 --lang/-l 的取值。
//
// 用 pflag 而不是手写循环，原因是手写极容易在两点上出错，而 pflag 已经处理妥当：
//   - 四种等价写法 --lang=en / --lang en / -l en / -len 都要能识别
//   - 独立的 -- 之后应当停止 flag 解析，其后的内容不能被当成 flag
//
// pflag 还会对未知 flag 做"剥离取值"处理，因此子命令的 flag 取值不会被误读成语言。
// 刻意忽略 Parse 返回的错误：pflag 是边解析边赋值的，即使后面遇到无法识别的参数而
// 报错，之前已经解析出的 --lang 值依然有效。
//
// args 传入的是不含程序名的参数切片（即 os.Args[1:]），单独作为参数是为了可测试。
func scanLangFlag(args []string) string {
	fs := pflag.NewFlagSet("lang-scan", pflag.ContinueOnError)
	// 允许出现未知 flag，否则子命令的 flag 会让预扫描直接失败
	fs.ParseErrorsWhitelist.UnknownFlags = true
	// 屏蔽 pflag 自身的报错输出，避免污染用户终端
	fs.SetOutput(io.Discard)

	var lang string
	fs.StringVarP(&lang, "lang", "l", "", "")
	_ = fs.Parse(args)

	return strings.TrimSpace(lang)
}

// localizeTree 在 l10n.Init 之后，递归地把命令树在包初始化阶段固定的静态文案
// （Short/Long 与全部 flag 的说明）重新翻译为当前语言。
//
// 只能重译 Short/Long 与 flag 说明：命令的 Use、Aliases、RunE 等要么是符号、要么在
// 运行期自行调用 T()，不需要也不应该在此处理。对空串直接跳过，避免用空 id 查询。
func localizeTree(cmd *cobra.Command) {
	if cmd.Short != "" {
		cmd.Short = l10n.Retranslate(cmd.Short, nil)
	}
	if cmd.Long != "" {
		cmd.Long = l10n.Retranslate(cmd.Long, nil)
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Usage != "" {
			f.Usage = l10n.Retranslate(f.Usage, nil)
		}
	})
	for _, child := range cmd.Commands() {
		localizeTree(child)
	}
}
