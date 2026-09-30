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
//
// 本文件在「WebUI 运行期切换语言」之后又承担了第二个职责：可重复本地化。
// 因为英文源串同时是消息 id，被就地覆盖后就再也拿不回来，所以重译必须建立在
// 一份英文快照之上（见 treeTexts 的注释），入口是 relocalizeCommands()
package cmd

import (
	"io"
	"os"
	"strings"
	"sync"

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

// scanHelpFlagName / scanHelpFlagShorthand 是预扫描阶段为帮助参数注册的占位 flag
// 取值本身没有任何意义，唯一作用是绕开 pflag 对「未定义的 help」的特殊处理，详见 scanLangFlag
const (
	scanHelpFlagName      = "help"
	scanHelpFlagShorthand = "h"
)

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

	// 必须为 --help/-h 注册占位 flag，否则「--help 出现在 --lang 之前」时语言设置会失效
	// pflag v1.0.10 对帮助参数有专门分支：FlagSet 里不存在 help 时，
	//   - parseLongArg：case name == "help" → f.usage(); return a, ErrHelp
	//   - parseSingleShortArg：case c == 'h' → f.usage(); err = ErrHelp
	// 这两处都排在 ParseErrorsWhitelist.UnknownFlags 白名单判断之前，因此白名单救不了它；
	// 而 parseArgs 一旦收到 error 就立即 return，--help 之后的所有参数再也不会被解析，
	// 于是 --lang/-l 被整体丢弃，表现为 `flk serve --help --lang zh-CN` 输出英文
	//（只有把 --lang 写在 --help 之前才生效，写成 --help 在前则语言失效，这是用户最容易踩的顺序，会误以为中文翻译没做）
	// 注册占位 flag 后 exists 为真，pflag 走普通 bool 赋值路径即可继续扫描完整条命令行
	// 这里只影响预扫描的取值，真实解析仍由 cobra 的命令树负责，与 --help 的实际行为无关
	fs.BoolP(scanHelpFlagName, scanHelpFlagShorthand, false, "")

	_ = fs.Parse(args)

	return strings.TrimSpace(lang)
}

// treeTexts 是命令树在**本地化之前**的英文源串快照。
//
// 为什么必须有它：英文源串同时充当消息 id（见 pkg/l10n 的包注释），一旦被译文就地覆盖，
// 原串就永久丢失——想再译成另一种语言时拿到的入参已经是中文，查表必然落空，
// 于是"切回英文"只能把中文原样留下。实测过这个缺陷：en→zh→en 之后，命令树与
// flag 说明仍然是中文，因为 localizeTree 是不可逆的。存下每个节点的原始文案后，
// 每次重译都是"先还原英文源串、再按当前语言翻译"，因此可以任意次往复而不丢信息。
//
// 为什么快照由调用方传入、而不是做成包级全局：map 的键是指针，而命令对象可能被回收、
// 其地址被后续新建的对象复用，全局 map 会把上一个对象的文案错认成本对象的原文
// （表现为"还原"出一个从未存在过的英文串）。根命令树用包级 rootTexts——它与进程同生命周期，
// 不存在指针复用问题；测试里的一次性命令树各自持有一份新快照，互不干扰。
//
// 字段与 localizeTree 处理的范围严格对应：Short/Long 是帮助里的摘要与长说明，
// Use 是用法行的骨架（当前没有译文，记录它是为了让"快照=原始文案全集"这件事成立，
// 日后若给 Use 加上译文也不会变成不可逆），usage 是全部 flag 的说明。
// Aliases/RunE/DefValue/Annotations 刻意不在其中：它们要么是符号、要么参与解析语义，
// 误改会连带破坏 --help 的默认值展示甚至 cobra 对自身 flag 的识别
type treeTexts struct {
	short map[*cobra.Command]string
	long  map[*cobra.Command]string
	use   map[*cobra.Command]string
	usage map[*pflag.Flag]string
}

// newTreeTexts 建立一份空快照，供 localizeTree 边翻译边补记
func newTreeTexts() *treeTexts {
	return &treeTexts{
		short: make(map[*cobra.Command]string),
		long:  make(map[*cobra.Command]string),
		use:   make(map[*cobra.Command]string),
		usage: make(map[*pflag.Flag]string),
	}
}

// remember 记录一条命令的原始文案，已记录过的保持不动（首次记录即为原文）。
//
// "首次遇到即记录"是安全的，因为本函数只可能作用于两类对象：
//   - 包初始化阶段构造完毕的命令：当时语言尚未确定，文案必是英文源串
//   - cobra 在 execute() 阶段自动补建的 help/completion 命令：cobra 写入的是固定英文
//
// 两者都不会出现"把译文当成原文记下来"的情况，因此不需要额外的初始化时机约束
func (t *treeTexts) remember(cmd *cobra.Command) {
	if _, ok := t.short[cmd]; !ok {
		t.short[cmd] = cmd.Short
	}
	if _, ok := t.long[cmd]; !ok {
		t.long[cmd] = cmd.Long
	}
	if _, ok := t.use[cmd]; !ok {
		t.use[cmd] = cmd.Use
	}
}

// localizeTree 把命令树上的静态文案重译成当前语言。
//
// 过程是"先还原快照、再翻译"，因此**可重复调用**：无论树上是英文还是任意语言的译文，
// 结果都等于"按当前语言翻译英文源串"。这一点是 WebUI 运行期切换语言的前提。
//
// 只能重译 Short/Long/Use 与 flag 说明（处理范围的取舍见 treeTexts 的注释）：
// 命令的 Aliases、RunE 等要么是符号、要么在运行期自行调用 T()，不需要也不应该在此处理。
//
// 必须同时遍历 Flags() 与 PersistentFlags()，这是 Global Flags 曾经整段英文的根因：
// 语言在 cobra 解析之前就已确定，此刻持久化 flag 还没被合并进 Flags()（合并只发生在执行/展示
// 阶段，ParseFlags、InitDefaultHelpFlag、stripFlags、LocalFlags 等都会触发），只遍历 Flags()
// 会漏掉所有用 PersistentFlags()
// 声明的说明；而 flk 的全局 flag（--lang/--output/--verbose/--yes/--no-trash/
// --store-path/--work-dir）与 serve 的 --host/-p 恰恰都是这么声明的，
// 于是它们的说明始终停留在英文源串，用户在 zh-CN 下看到的是中英混排的帮助
func localizeTree(cmd *cobra.Command, texts *treeTexts) {
	// 未传快照时兜底新建：调用方漏传只会让"可逆"退化成"当次可用"，
	// 而不是 panic——这条路径实际不可达，保留它只为让误用不至于崩在用户面前
	if texts == nil {
		texts = newTreeTexts()
	}
	// visited 跨整棵树共享：cobra 合并父子 flag 时登记的是同一个 *pflag.Flag 指针
	//（AddFlagSet 只登记指针不复制），父命令译一次即在全树生效，
	// 因此按指针去重既保证「每条说明只译一次」，也避免同一 flag 被父子两条路径重复处理
	// 快照本身已按指针去重（map 键），这里的 visited 只负责"当次遍历不重复翻译"
	localizeCommand(cmd, texts, make(map[*pflag.Flag]struct{}))
}

// localizeCommand 是 localizeTree 的递归实现，visited 由调用方在整棵树范围内共享
func localizeCommand(cmd *cobra.Command, texts *treeTexts, visited map[*pflag.Flag]struct{}) {
	// 先补记原文再翻译：未记录过的节点此刻文案仍是英文源串（见 treeTexts.remember）
	texts.remember(cmd)

	// 空串不入快照也不翻译：用空 id 查表没有意义（结果必然还是空串），跳过更省事
	if src := texts.short[cmd]; src != "" {
		cmd.Short = l10n.Retranslate(src, nil)
	}
	if src := texts.long[cmd]; src != "" {
		cmd.Long = l10n.Retranslate(src, nil)
	}
	if src := texts.use[cmd]; src != "" {
		cmd.Use = l10n.Retranslate(src, nil)
	}

	// 只改 Usage：DefValue、Annotations、Value 等属于 flag 的解析语义与默认值展示，
	// 与文案翻译无关，误改会连带破坏 --help 里的 (default ...) 以及 cobra 对自身 flag 的识别
	translateFlags := func(flags *pflag.FlagSet) {
		flags.VisitAll(func(f *pflag.Flag) {
			if _, done := visited[f]; done {
				return
			}
			visited[f] = struct{}{}
			// 与命令文案同理：先取快照里的英文源串，再翻译
			if _, ok := texts.usage[f]; !ok {
				texts.usage[f] = f.Usage
			}
			if src := texts.usage[f]; src != "" {
				f.Usage = l10n.Retranslate(src, nil)
			}
		})
	}
	// 两次遍历的先后无关紧要：重复出现的 flag 由 visited 挡掉，不会被译第二遍
	translateFlags(cmd.Flags())
	translateFlags(cmd.PersistentFlags())

	for _, child := range cmd.Commands() {
		localizeCommand(child, texts, visited)
	}
}

// rootTexts 是根命令树的英文源串快照，与 rootCmd 同生命周期
var rootTexts = newTreeTexts()

// relocalizeMu 串行化对根命令树的重译。
//
// 为什么需要锁：serve 期间 POST /api/language 可能被并发调用（多个标签页同时切换语言），
// 而重译是就地改写整棵树的 Short/Long/Use 与 flag 说明，两次重译交错会出现
// "A 还原成英文 → B 按另一种语言翻译完 → A 再翻译成第三种结果"的互相覆盖，
// 最终树上的文案与 l10n.Current() 对不上。锁只覆盖一次树遍历（微秒级），
// 不会给切换操作带来可感知的延迟
var relocalizeMu sync.Mutex

// relocalizeCommands 按当前语言重译根命令树，供运行期切换语言后调用。
//
// 与 CLI 启动路径共用同一份实现（Execute 也调它）：走两条路径就会有两份"如何本地化命令树"
// 的知识，迟早漂移成"启动时译得对、切换后译错"这类只在特定顺序下出现的缺陷。
//
// 潜在影响点：本函数就地改写包级命令对象的文案，与"同时正在渲染帮助"的路径天然互斥不了。
// flk 的进程模型下这不构成实际问题——serve 是唯一的常驻命令，它不会在执行期间渲染帮助；
// 若日后出现"边服务边打印帮助"的命令，需要改为对命令树的只读快照取文案
func relocalizeCommands() {
	relocalizeMu.Lock()
	defer relocalizeMu.Unlock()
	localizeTree(rootCmd, rootTexts)
}
