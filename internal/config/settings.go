// settings.go 定义 flk 设置项的注册表。
//
// get / set / reset / validate 四个操作共用这一份声明，避免四处各写一套
// "这个键叫什么、什么类型、默认值是多少、什么算合法"——那种重复迟早会漂移成
// "set 能写进去、validate 说它非法"这类自相矛盾
//
// 新增设置项时只改这一处（外加 Config 结构体上的同名 json tag，供 flk config show 输出）：
// 键名、类型、默认值、期望值描述、解析器全部在这里声明，命令层不再重复任何取值知识
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/logger"
	"github.com/jy-eggroll/flk/internal/locales"
)

// ErrInvalidValue 表示用户输入的值不合法。
//
// 这里刻意不携带具体原因文案：合法取值的人类可读描述由 Setting.Expected 提供，
// 由调用方用统一模板渲染（见 cmd/config.go 的 errInvalidValue）。若让每个 Parse 各返回一句散文，
// 用户可见文案的数量会随校验规则数线性增长，中英双语都得跟着维护
var ErrInvalidValue = errors.New("config: invalid value")

// Kind 描述设置项的取值类型。
// 它决定 get 的输出形态（标量裸值 / 列表每行一项）与体检时的类型检查
type Kind string

const (
	// KindString 是单个字符串
	KindString Kind = "string"
	// KindList 是字符串数组，命令行形态为逗号分隔
	KindList Kind = "list"
)

// Setting 描述一个设置项。
type Setting struct {
	// Key 与设置文件里的 JSON 键完全一致，也是 set 时写入的规范键名。
	// 它取文件里既有的写法（allowHosts 是驼峰，language/logLevel 是小写），
	// 不引入第二套命名；用户手敲的任意大小写由 Lookup 统一收敛到这里
	Key string
	// Kind 决定 get 的输出形态与体检时的类型检查
	Kind Kind
	// Default 是 reset --defaults 写入的值，也是文件里缺该键时的生效值。
	// 潜在影响点：当它是切片时，注册表与调用方共享同一份底层数组，
	// 因此调用方一律**只读**（本包的写入路径都只做序列化，不会原地修改）
	Default any
	// Expected 是合法取值的人类可读描述，用于拼装报错信息。
	//
	// 这里刻意写成 l10n.T(...) 的字面量形式：包初始化阶段求值只会拿到英文源串（此时翻译器尚未就绪），
	// 但提取工具据此把这条描述收进语言文件——报错时由 errInvalidValue 再查一次表，
	// 于是 zh-CN 下用户看到的是中文描述而不是英文。写成普通字面量也能编译，
	// 代价只是这句描述永远不会被翻译（且 l10n:check 不会报错，属于静默退化）
	Expected string
	// Parse 把命令行字符串转成待写入的 JSON 值，失败时返回 ErrInvalidValue。
	// 它同时是 set 的校验器与 validate 的校验器，因此两处的判定必然一致
	Parse func(string) (any, error)
}

// settings 是全部设置项的注册表，顺序即 flk config validate 与帮助里的展示顺序
var settings = []Setting{
	{
		Key:      "allowHosts",
		Kind:     KindList,
		Default:  []string{},
		Expected: l10n.T("a non-empty comma-separated list of host names (e.g. 192.168.1.5,my.dev.lan)", nil),
		Parse:    parseHostList,
	},
	{
		Key:      "language",
		Kind:     KindString,
		Default:  locales.Default,
		Expected: l10n.T("a supported language tag (en or zh-CN)", nil),
		Parse:    parseLanguage,
	},
	{
		Key:      "logLevel",
		Kind:     KindString,
		Default:  logger.DefaultLevelText(),
		Expected: l10n.T("debug, info, warn, or error", nil),
		Parse:    parseLogLevel,
	},
}

// Settings 返回全部设置项（切片副本，调用方增删元素不影响注册表；
// 元素内部的 Default 切片仍是共享的，见 Setting.Default 的说明）
func Settings() []Setting {
	out := make([]Setting, len(settings))
	copy(out, settings)
	return out
}

// Lookup 按键名查找设置项。键名比较忽略大小写与首尾空白，
// 因为用户手敲时大小写很难记得准，而读路径（normalizeKeys）本就把文件里的键统一成了小写
func Lookup(key string) (Setting, bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	for _, s := range settings {
		if strings.ToLower(s.Key) == k {
			return s, true
		}
	}
	return Setting{}, false
}

// effective 返回某个设置项在 raw（已由 ReadRawAt 统一小写键名）中的生效值：
// 文件里有就用文件的值，缺失、类型不对或为空时用默认值。
//
// 容错取向与运行期加载一致（见 LoadAt 的注释）：单个字段写错不应该让整份设置文件作废，
// 因此这里对每个键都做形状检查，不合法就当作"未设置"。
// 反过来说，validate 子命令要的恰恰是"把这种静默降级说出来"，因此它不走本函数而是自己逐项报错
func (s Setting) effective(raw map[string]any) any {
	v, ok := raw[strings.ToLower(s.Key)]
	if !ok {
		return s.Default
	}

	switch s.Kind {
	case KindList:
		if hosts, ok := stringList(v); ok {
			return hosts
		}
	case KindString:
		text, ok := v.(string)
		if !ok {
			return s.Default
		}
		text = strings.TrimSpace(text)
		if text == "" {
			// 空串在读取侧等价于"未设置"：留着它只会让用户以为设置生效了，实际什么都没变
			return s.Default
		}
		// 有两个键存在规范形态，读出来的值要对齐运行期，否则 flk config get 打印的
		// 会是一个从未生效过的写法：
		//   - language：文件里可能写着 zh 或 zh-Hans，而运行期实际生效的是归一化后的 zh-CN
		//   - logLevel：取值大小写不敏感，统一按小写呈现
		switch s.Key {
		case "language":
			return l10n.Normalize(text, locales.Supported(), locales.Default)
		case "logLevel":
			return strings.ToLower(text)
		}
		return text
	}
	return s.Default
}

// stringList 把 JSON 解码后的值整理成字符串列表，并做元素级清洗：
// 只接受数组，元素必须是字符串，元素去首尾空白、丢弃空串。
//
// 为什么丢弃空串：白名单里的空主机名不存在（见 cmd/serve_guard.go 的 normalizeHost 会在归一化后返回空串并被跳过），
// 留着它只会让用户误以为配置已经生效。
// 注意这里刻意不做"归一化"（不去端口、不转小写）：那是 guard 的职责，且它同时作用于请求侧与允许侧，
// 在配置解析阶段提前归一反而会引入第二份可能漂移的实现
func stringList(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	hosts := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			continue
		}
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			hosts = append(hosts, trimmed)
		}
	}
	return hosts, true
}

// parseHostList 解析逗号分隔的主机名列表（如 "192.168.1.5,my.dev.lan"），供 flk config set allowHosts 使用。
//
// 用逗号分隔而不是"可重复的 flag"：set 子命令的形态是 "键 值" 两个位置参数，
// 没有地方承载重复出现的取值；逗号形式也与 serve 的 --allow-host 保持一致（那里用 StringSlice，
// 两种写法都支持），用户不必记两套语法。
//
// 全部为空（空串、纯逗号、纯空白）时判为非法而不是"清空列表"：
// 清空的正确做法是 flk config reset allowHosts，而把空值当清空会让一次手滑静默清掉白名单
func parseHostList(s string) (any, error) {
	var hosts []string
	for _, part := range strings.Split(s, ",") {
		if host := strings.TrimSpace(part); host != "" {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil, ErrInvalidValue
	}
	return hosts, nil
}

// parseLanguage 解析输出语言。
//
// 必须用 l10n.IsSupported 而不是 l10n.Normalize：后者对不认识的输入回退默认语言，
// 照搬会把用户输入的 fr 静默改写成 en——而"我要法语"和"我要英语"显然不是一回事。
// 校验通过后存入归一化结果，使文件里只有规范形态（zh 与 zh-Hans 都存成 zh-CN）
func parseLanguage(s string) (any, error) {
	v := strings.TrimSpace(s)
	if !l10n.IsSupported(v, locales.Supported()) {
		return nil, ErrInvalidValue
	}
	return l10n.Normalize(v, locales.Supported(), locales.Default), nil
}

// parseLogLevel 解析日志级别，合法取值由 logger 判定（它是"合法级别"的唯一真相）。
//
// 存入小写规范形态：logger.LogLevelFromString 本身大小写不敏感，
// 但文件与 flk config show 里只应出现一种写法，否则同一份设置会有多种等价形态
func parseLogLevel(s string) (any, error) {
	v := strings.TrimSpace(s)
	if _, err := logger.LogLevelFromString(v); err != nil {
		// 丢弃 logger 给出的具体错误：它是给终端用户的提示（含合法值清单），
		// 而 set/validate 的文案由统一模板 + Setting.Expected 渲染，
		// 两条路径各自成立，不必在这里二次拼装
		return nil, ErrInvalidValue
	}
	return strings.ToLower(v), nil
}

// ValueText 把配置值（JSON 解码后的形态，或 Parse 产出的形态）转成裸文本，
// 去掉 JSON 的引号与类型包装。
//
// 有两个用途：validate 里复用 Setting.Parse 校验文件中的取值（Parse 接收字符串），
// 以及 flk config get/set 打印取值。两处共用一份实现，避免"校验时认得的写法"与
// "展示出来的写法"不一致
func ValueText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case []string:
		return strings.Join(t, ",")
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, ValueText(item))
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprintf("%v", t)
	}
}
