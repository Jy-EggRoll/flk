package l10n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/nicksnyder/go-i18n/v2/i18n/template"
)

// missingKeyErrorParser 让模板变量缺失时直接报错，而不是静默渲染成 <no value>。
//
// go-i18n 的 template.TextParser 默认 Option 为 "missingkey=default"（见
// go-i18n 的 template/text_parser.go），漏传模板变量既不报错也不返回 error，
// 用户会看到诸如 "{{.Count}} repos" 渲染出的 "<no value> repos"，且本包的容错
// 逻辑抓不到。改成 missingkey=error 后，这类问题会退化为回退显示源串，开发期即可暴露。
//
// 注意：不含 {{ 的纯文本消息会走 TextParser 的快速路径直接返回，不经过模板解析。
var missingKeyErrorParser = &template.TextParser{Option: "missingkey=error"}

// reservedKeys 是 go-i18n 在解析消息文件时识别为"消息字段"的键名
// （见 go-i18n 的 message.go 的 isReserved）。
//
// 本方案的语言文件是平铺的 { 源串: 译文 }，顶层键就是消息 id——若某条 id 恰好等于
// 这些词（忽略大小写），go-i18n 会把它当成消息元数据字段而非一条消息：
//   - 若它是文件里唯一的键，整份文件会被当成"一条消息"解析，所有条目丢失
//   - 若与其它键共存，则触发 mixedKeysError，整份文件解析失败
//
// 两种情况的后果都是全部译文静默失效，因此必须在加载时显式拦截。
var reservedKeys = map[string]struct{}{
	"id": {}, "description": {}, "hash": {}, "leftdelim": {}, "rightdelim": {},
	"zero": {}, "one": {}, "two": {}, "few": {}, "many": {}, "other": {}, "translation": {},
}

// ValidateMessageID 校验 id 能否安全地用作消息 id。
//
// 命中保留字的 id 会让整份语言文件解析失败或条目丢失（见 reservedKeys 的说明），
// 必须在写入语言文件之前就拦下。提取工具在生成默认语言文件时调用本函数，与运行期
// 加载共用同一份判定，避免两处规则漂移。
func ValidateMessageID(id string) error {
	if _, ok := reservedKeys[strings.ToLower(id)]; ok {
		return fmt.Errorf("message id %q collides with a go-i18n reserved word (%s), which would make the whole locale file fail to parse or silently drop entries; please reword this message",
			id, strings.Join(reservedWords(), "/"))
	}
	return nil
}

// reservedWords 返回排好序的保留字列表，仅用于错误信息。
func reservedWords() []string {
	out := make([]string, 0, len(reservedKeys))
	for k := range reservedKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// loadLocaleFile 校验并加载一份语言文件，返回其中的消息条数。
//
// 除了解析，这里还刻意做了三项结构校验，它们共同保证"文件里的每条译文都真的会被
// go-i18n 认作一条消息"——这是静默失效的唯一入口：
//
//  1. 必须是平铺的「字符串 -> 字符串」对象。嵌套会在 go-i18n 里被当作复数形式或
//     被拼成 a.b.c 形式的 id，与"源串即 id"的模型冲突，生成器也只产出平铺结构
//  2. 键不得命中 reservedKeys，否则整份文件解析失败或条目丢失
//  3. 解析出的消息条数必须等于文件里的键数，否则说明有条目被 go-i18n 丢弃
//
// 本文件里的错误信息一律用英文：它们描述的是"语言文件本身有问题"，此时翻译器恰恰
// 不可用，不存在翻译它们的可能。
func loadLocaleFile(bundle *goi18n.Bundle, opts Options, tag string) (int, error) {
	// 路径必须是 "<Dir>/<tag>.json"：go-i18n 靠它反推语言标签
	rel := tag + ".json"
	if opts.Dir != "" {
		rel = path.Join(opts.Dir, rel)
	}

	buf, err := fs.ReadFile(opts.FS, rel)
	if err != nil {
		return 0, fmt.Errorf("failed to read locale file %s: %w", rel, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		return 0, fmt.Errorf("failed to parse locale file %s: %w", rel, err)
	}
	for key, val := range raw {
		if _, ok := val.(string); !ok {
			return 0, fmt.Errorf("locale file %s: value of %q is not a string; locale files must be a flat {source string: translation} object",
				rel, key)
		}
		if err := ValidateMessageID(key); err != nil {
			return 0, fmt.Errorf("locale file %s: %w", rel, err)
		}
	}

	file, err := bundle.ParseMessageFileBytes(buf, rel)
	if err != nil {
		return 0, fmt.Errorf("failed to parse locale file %s: %w", rel, err)
	}
	if len(file.Messages) != len(raw) {
		return 0, fmt.Errorf("locale file %s: parsed %d messages but the file has %d keys, so some entries were dropped by go-i18n",
			rel, len(file.Messages), len(raw))
	}
	return len(file.Messages), nil
}
