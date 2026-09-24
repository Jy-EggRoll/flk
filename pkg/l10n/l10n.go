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
// 调用约定：Init 必须在任何并发调用 T() 之前完成；Init 之后本包状态只读，
// 可被多个 goroutine 并发读取。这不是巧合而是必要前提——调用方常在 worker
// 并发任务里调 T()。
package l10n

import (
	"encoding/json"
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

var (
	// localizer 是当前语言的翻译器。Init 成功后只读。
	// 允许为 nil（Init 未调用或失败），此时 T 必须安全降级而不是 panic。
	localizer *goi18n.Localizer

	// current 是当前生效的语言标签。
	current string

	// defaultLanguage 与 supported 由 Init 从 Options 落位，此后只读。
	defaultLanguage string
	supported       []string
)

// Init 按 opts 加载语言文件并建立当前语言的翻译器，失败时返回 error。
//
// lang 可以是不规范的输入（如 "zh"、"en-US"）：本函数会用 opts 收敛它，
// 无法识别时回退 opts.Default。因此调用方**不需要**先自己归一化，
// 也就不必在 Init 之前持有语言白名单。
//
// 之所以返回 error 而不是静默降级：语言文件是编译进二进制的，解析失败属于构建期
// 错误，运行期无从补救。若默默跳过，用户看到的只是"界面语言不对"，没有任何报错，
// 排查成本极高。调用方应把 error 打印后退出。
func Init(lang string, opts Options) error {
	if err := opts.validate(); err != nil {
		return err
	}

	bundle := goi18n.NewBundle(language.Make(opts.Default))
	// go-i18n 在 format 为 json 且未注册解码器时会自动退回 json.Unmarshal
	// （见 go-i18n 的 parse.go），这里显式注册只为让意图更清楚
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)

	for _, tag := range opts.Supported {
		if _, err := loadLocaleFile(bundle, opts, tag); err != nil {
			return err
		}
	}

	// 状态与 localizer 一起落位，避免中途失败留下半套状态
	defaultLanguage = opts.Default
	supported = append([]string(nil), opts.Supported...)
	localizer = goi18n.NewLocalizer(bundle, Normalize(lang, supported, defaultLanguage))
	current = Normalize(lang, supported, defaultLanguage)
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
	if localizer == nil {
		// Init 未调用或失败：返回源串而不是 panic，保证任何调用路径都不会崩
		return msg
	}
	out, _ := localizer.Localize(&goi18n.LocalizeConfig{
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

// Current 返回当前生效的语言标签。
func Current() string {
	return current
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
func Supported() []string {
	out := make([]string, len(supported))
	copy(out, supported)
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
