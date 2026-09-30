// Package l10n 提供一套「英文源串即消息 id」的本地化方案。
//
// 模型与 VSCode 的 @vscode/l10n 相同：源码里写 l10n.T("Total size: {{.Size}}", data)，
// "Total size: {{.Size}}" 既是待翻译的英文原文，也是查表用的消息 id——不存在独立的
// 符号 key，因此也不必维护"key 与文案的对应关系"，代价是改文案即改 id。
//
// 三者的分工：
//   - <Dir>/<Options.Default>.json 是**生成物**，由配套的 pkg/l10n/cmd/l10n 工具扫描源码覆盖
//     写入，内容是 { 源串: 源串 } 的自映射，不要手工编辑
//   - <Dir>/<其它语言>.json 是**手工维护**的译文，形如 { 英文源串: 译文 }
//   - 两者都经 Options.FS 交给 Init，通常由调用方 //go:embed 提供
//
// 缺失译文时逐级回退，最终落到源串本身，因此**永远不会返回空串或裸 key**——
// 漏翻的表现只是"这句还是英文"，不会更糟。
//
// 并发契约（支持运行期切换语言）：
//   - Init 必须在任何 T()/Current()/Supported()/SetLanguage 之前完成，且全进程只调用一次：
//     语言文件只在 Init 里解析一次，之后再也不会重新解析（原因见 SetLanguage 的注释）
//   - Init 成功之后，T / Current / Supported / SetLanguage 都可以被多个 goroutine 并发调用。
//     它们读写的是一份**整体替换**的状态快照（见 snapshot 与 state），
//     因此不存在"读到半个状态"的窗口，也不需要调用方自己加锁
//   - 切换语言的可见性是即时的：SetLanguage 返回之后，任何新的 T() 都按新语言输出；
//     已在执行中的 T() 不受影响（它读的是自己那份快照）
//   - Init 失败时状态保持原样而不是被清空：宁可维持上一个可用状态（可能是"从未 Init"），
//     也不要留下半套状态；从未 Init 过时 T 安全回退源串而不是 panic
package l10n

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// snapshot 是本包的**全部**运行期状态，整体替换、原子发布。
//
// 为什么把 localizer 与 current 放进同一个结构体、用一次 atomic.Store 发布，
// 而不是各用一个原子变量：两者必须成对变化。调用方常在同一段逻辑里先取 Current()
// 再取 T()（例如"按当前语言给页面注入语言标签与文案"），若分成两个原子变量，
// 就会留出"localizer 已是新语言、current 还是旧语言"的窗口；这种不一致不会崩溃、
// 也不会被 -race 抓到（两次访问各自都是原子的），只会让上层拿到自相矛盾的组合。
type snapshot struct {
	// bundle 是 Init 解析语言文件后得到的消息集。运行期对它只读，
	// 供 SetLanguage 复用（不能重新解析，理由见 SetLanguage 的注释）
	bundle *goi18n.Bundle

	// localizer 是当前语言的翻译器，nil 表示尚未 Init 或 Init 从未成功
	localizer *goi18n.Localizer

	// current 是当前生效的语言标签
	current string

	// defaultLanguage 是默认语言标签，作为 Normalize 的回退层
	defaultLanguage string

	// supported 是随二进制发布的语言列表，顺序即匹配优先级（见 Options.Supported）
	supported []string
}

// state 是包级状态。nil（零值）代表"尚未 Init 成功"，此时 T 回退源串而不是 panic
var state atomic.Pointer[snapshot]

// Init 按 opts 加载语言文件并建立当前语言的翻译器，失败时返回 error。
//
// lang 可以是不规范的输入（如 "zh"、"en-US"）：本函数会用 opts 收敛它，
// 无法识别时回退 opts.Default。因此调用方**不需要**先自己归一化，
// 也就不必在 Init 之前持有语言白名单。
//
// 之所以返回 error 而不是静默降级：语言文件是编译进二进制的，解析失败属于构建期
// 错误，运行期无从补救。若默默跳过，用户看到的只是"界面语言不对"，没有任何报错，
// 排查成本极高。调用方应把 error 打印后退出。
//
// 失败时**不修改**已有状态（可能是"从未 Init"，也可能是上一次成功的结果）：
// 半套状态（例如 language 列表已换、localizer 还是旧的）比"保持原状"更难排查
func Init(lang string, opts Options) error {
	if err := opts.validate(); err != nil {
		return err
	}

	bundle := goi18n.NewBundle(language.Make(opts.Default))
	// go-i18n 在 format 为 json 且未注册解码器时会自动退回 json.Unmarshal
	// （见 go-i18n 的 parse.go），这里显式注册只为让意图更清楚
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)

	// 语言文件在此一次性解析完毕：ParseMessageFileBytes 会写 bundle 的内部状态
	//（消息表与 matcher），运行期不得再走这条路径，否则会与并发中的 T() 竞争
	//（详见 SetLanguage 的注释）
	for _, tag := range opts.Supported {
		if _, err := loadLocaleFile(bundle, opts, tag); err != nil {
			return err
		}
	}

	// 归一化与整份状态的组装都放在加载成功之后：任何一步失败都不会留下半套状态
	resolved := Normalize(lang, opts.Supported, opts.Default)

	// 一次 Store 发布整份快照：读出它的 goroutine 要么全看到新状态、要么全看到旧状态
	state.Store(&snapshot{
		bundle:          bundle,
		localizer:       goi18n.NewLocalizer(bundle, resolved),
		current:         resolved,
		defaultLanguage: opts.Default,
		// 复制一份：调用方持有的 opts 之后可能被改动，包内状态不能跟着变
		supported: append([]string(nil), opts.Supported...),
	})
	return nil
}

// SetLanguage 把当前语言切换为 lang，只替换翻译器与语言标签。
//
// 这是"网页上切换语言、后端全量生效"的核心：切换后所有新的 T()（包括命令树的帮助
// 文案重译，见 cmd/lang.go）都按新语言输出，且不需要重启进程。
//
// 三条必须点明的前提：
//  1. **语言文件只在 Init 时解析一次**：loadLocaleFile → ParseMessageFileBytes 会写 bundle
//     的内部状态（消息表、matcher），运行期再解析就会与并发中的 T() 竞争同一份内部结构。
//     本函数因此只调用 NewLocalizer——它是纯构造函数，直接读 bundle 的既有内容，
//     不产生任何写入（已核阅 go-i18n v2.6.1 的本地源码：NewLocalizer 只做 parseTags，
//     查找路径 Bundle.getMessageTemplate 也只读 map）
//  2. lang 必须**受支持**：不受支持时返回 error 而不是回退默认语言。需要它的场合
//     （网页下拉框、写回配置文件）要的是"拒绝"，静默改写会让用户以为自己选的语言生效了
//     （宽松/严格两种口径的分工见 IsSupported 的注释）
//  3. 未 Init 成功时返回 error 而不是 panic 或假装成功：没有 bundle 就构造不出 Localizer，
//     此时"切换成功"是假的，必须让调用方知道
//
// 同语种的地区/字形变体会被接受并收敛到已发布的那一项（如 zh-Hant → zh-CN），
// 与 --lang 的宽松语义保持一致；返回值不告诉调用方最终落在哪个标签上，
// 需要时用 Current() 读取
func SetLanguage(lang string) error {
	snap := state.Load()
	if snap == nil || snap.bundle == nil {
		return errors.New("l10n: SetLanguage called before a successful Init")
	}
	if !IsSupported(lang, snap.supported) {
		return fmt.Errorf("l10n: unsupported language %q (supported: %s)", lang, strings.Join(snap.supported, ", "))
	}

	resolved := Normalize(lang, snap.supported, snap.defaultLanguage)

	// 复制旧快照后整份发布：语言列表与默认语言保持不变，只换"随语言变化"的两个字段。
	// 复制而不是原地改旧快照，是为了让"正在读旧快照的 goroutine"始终看到一份自洽的状态
	next := *snap
	next.localizer = goi18n.NewLocalizer(snap.bundle, resolved)
	next.current = resolved
	state.Store(&next)
	return nil
}

// T 返回 msg 在当前语言下的译文，data 是模板变量（可为 nil）。
//
// msg 既是原文也是消息 id。查不到译文时逐级回落，最终落到 DefaultMessage（即 msg
// 本身），所以本函数**永远不会返回空串或裸 key**，最差情况就是返回源串。
//
// 注意这里不看 error：go-i18n 在消息缺失时仍然返回兜底文本并附带非 nil error
// （见 go-i18n 的 localizer.go），以 error 为准会把"正常的回退"误判成失败。
func T(msg string, data map[string]any) string {
	// 快照只读一次：同一次调用内的语言必须自洽，不可在查询中途再读（否则可能换语言）
	snap := state.Load()
	if snap == nil || snap.localizer == nil {
		// Init 未调用或从未成功：返回源串而不是 panic，保证任何调用路径都不会崩
		return msg
	}
	out, _ := snap.localizer.Localize(&goi18n.LocalizeConfig{
		MessageID: msg,
		// DefaultMessage.ID 必须与 MessageID 一致，否则 go-i18n 返回
		// messageIDMismatchErr（见 go-i18n 的 localizer.go）
		DefaultMessage: &goi18n.Message{ID: msg, Other: msg},
		TemplateData:   data,
		TemplateParser: missingKeyErrorParser,
	})
	if out == "" {
		// 模板渲染失败等异常情况，同样回落源串
		return msg
	}
	return out
}

// Current 返回当前生效的语言标签。从未 Init 成功时返回空串。
func Current() string {
	if snap := state.Load(); snap != nil {
		return snap.current
	}
	return ""
}

// Retranslate 与 T 语义完全相同，供「运行期对既知消息做二次翻译」的场合使用。
//
// 为什么需要它：某些项目的命令树在**包初始化阶段**就已构造完毕，其中的静态文案
// （命令说明、flag 说明）用 T 求值时 localizer 尚未就绪，只能拿到英文源串；必须在
// Init 之后再对这些已经确定的字符串重译一次，而入参必然是变量。
//
// 刻意与 T 分成两个名字：l10n 的源码提取工具只识别 `.T(...)`，且要求首参是字符串
// 字面量（消息 id 即原文，不能是变量）。本函数服务于「变量入参」这一合法场景，
// 不是定义新消息，因此不属于提取范围；调用它不会往语言文件里新增任何条目。
func Retranslate(msg string, data map[string]any) string {
	return T(msg, data)
}

// Supported 返回 Init 时登记的语言标签列表（副本，调用方改动不影响内部状态）。
// 从未 Init 成功时返回 nil——"没有任何受支持的语言"就是事实，不编造默认值
func Supported() []string {
	snap := state.Load()
	if snap == nil {
		return nil
	}
	out := make([]string, len(snap.supported))
	copy(out, snap.supported)
	return out
}

// Normalize 把外部传入的语言串收敛到 supported 中的一项，无法识别时返回 def。
//
// 刻意做成**纯函数**而不是依赖 Init 的状态：需要它的场合常常在 Init 之前或之外
// ——例如"先解析出语言、再加载对应的语言文件"，或校验一份与运行期无关的配置文件。
// 依赖包级状态会逼出一套看不见的调用顺序，调用方一旦漏掉就是静默出错。
//
// 这是**宽松**匹配，专为 --lang 这类"用户随手输入、不该因此失败"的入口而设计：
// 给什么都返回一个可用标签，最差落到 def。
//
// 需要"拒绝而不是改写"的场合**不要**用它：Normalize("fr", ...) 会返回 def，
// 照搬会把用户输入的 fr 静默改写成默认语言。那种场合用 IsSupported。
//
// 匹配分两轮，均为大小写不敏感：
//  1. 完全相等，如 "zh-CN" 命中 "zh-CN"
//  2. 主语言子标签相等，使 "zh"、"zh-Hans"、"zh-TW" 都落到 "zh-CN"，
//     "en-US" 落到 "en"——同语种内的地区差异一律收敛到已发布的那一个
func Normalize(lang string, supported []string, def string) string {
	raw := strings.ToLower(strings.TrimSpace(lang))
	if raw == "" {
		return def
	}
	for _, s := range supported {
		if strings.EqualFold(s, raw) {
			return s
		}
	}
	primary := primarySubtag(raw)
	for _, s := range supported {
		if primarySubtag(strings.ToLower(s)) == primary {
			return s
		}
	}
	return def
}

// IsSupported 严格判断 lang 是否对应 supported 中的一项。纯函数，理由同 Normalize。
//
// 与 Normalize 的区别在于对"无法识别"的态度：Normalize 回退默认语言（宽松，为
// --lang 而生），本函数如实返回 false。需要"拒绝而不是改写"的场合必须用它。
//
// 同语种的地区/字形变体视为受支持，如 zh-Hans、en-US。
func IsSupported(lang string, supported []string) bool {
	raw := strings.ToLower(strings.TrimSpace(lang))
	if raw == "" {
		return false
	}
	primary := primarySubtag(raw)
	for _, s := range supported {
		if primarySubtag(strings.ToLower(s)) == primary {
			return true
		}
	}
	return false
}

// primarySubtag 返回语言标签的主语言子标签，如 "zh-CN" -> "zh"、"en" -> "en"。
func primarySubtag(tag string) string {
	if i := strings.IndexByte(tag, '-'); i > 0 {
		return tag[:i]
	}
	return tag
}
