// validate.go 实现 `flk config validate` 的体检逻辑。
//
// 体检与运行期加载的容错策略刻意不同：LoadAt 力求"永远能跑"，遇到非法值会静默回退默认值
// （见 Setting.effective）；体检力求"把会被静默忽略的东西说出来"。因此每条问题都标明严重级别，
// 并说明运行期会发生什么，用户才不会困惑于"为什么它说有问题但命令照样能跑"
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/flk/internal/pathutil"
)

// Level 是体检问题的严重级别。
type Level string

const (
	// LevelError 表示运行期会被静默忽略、替换成默认值、或直接导致读取结果不确定的问题
	LevelError Level = "error"
	// LevelWarning 表示运行期能容忍、或只影响可维护性的问题
	LevelWarning Level = "warning"
)

// Issue 是一条体检结果。Message 已是当前语言的文案
type Issue struct {
	Level   Level
	Message string
}

// utf8BOM 是 UTF-8 字节序标记。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// ValidateAt 体检指定路径的设置文件。
//
// 返回的 error 只用于"连读都读不了"（权限不足、路径是目录等）——那种情况无从体检。
// 文件不存在返回空清单：设置文件本来就允许不存在，不算问题
func ValidateAt(path string) ([]Issue, error) {
	expanded, err := pathutil.NormalizePath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(expanded)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var issues []Issue
	// UTF-8 BOM：Windows 记事本"另存为 UTF-8"默认会写。带 BOM 的文件 encoding/json 会解析失败，
	// 而报错信息完全看不出是 BOM 引起的，属于极难自查的一类，必须单独指出来
	if bytes.HasPrefix(data, utf8BOM) {
		issues = append(issues, Issue{
			Level:   LevelError,
			Message: l10n.T("The settings file starts with a UTF-8 BOM, which breaks JSON parsing; re-save it as UTF-8 without BOM", nil),
		})
		data = bytes.TrimPrefix(data, utf8BOM)
	}

	// 这里用不归一键名的原始解析：仅大小写不同的重复键必须在归一之前才看得出来
	raw, err := decodeObject(data)
	if err != nil {
		issues = append(issues, Issue{
			Level:   LevelError,
			Message: l10n.T("Cannot parse the settings file: {{.Err}}", map[string]any{"Err": err}),
		})
		return issues, nil
	}

	issues = append(issues, validateKeyCase(raw)...)
	issues = append(issues, validateKeys(normalizeKeys(raw))...)
	return issues, nil
}

// validateKeyCase 检查是否存在仅大小写不同的重复键（如同时写了 language 与 Language）。
//
// 这类文件在运行期的读取结果取决于 Go map 的随机遍历顺序，也就是"每次运行读到的语言可能不同"，
// 属于必须指出的错误；而读路径刻意容忍它（见 normalizeKeys 的说明），
// 体检因而成为唯一的发现入口
func validateKeyCase(raw map[string]any) []Issue {
	seen := make(map[string][]string, len(raw))
	for k := range raw {
		lower := strings.ToLower(strings.TrimSpace(k))
		seen[lower] = append(seen[lower], k)
	}

	var keys []string
	for lower, variants := range seen {
		if len(variants) > 1 {
			keys = append(keys, lower)
		}
	}
	// 排序保证输出稳定，便于 diff 与脚本消费
	sort.Strings(keys)

	issues := make([]Issue, 0, len(keys))
	for _, key := range keys {
		issues = append(issues, Issue{
			Level: LevelError,
			Message: l10n.T("The settings file has several keys that differ only in case ({{.Key}}), so which one wins is undefined; please keep just one",
				map[string]any{"Key": key}),
		})
	}
	return issues
}

// validateKeys 逐项检查取值，并指出未知键。
// raw 必须是已归一键名（小写）的形态，否则查表全部落空
func validateKeys(raw map[string]any) []Issue {
	var issues []Issue

	// 未知键多半是拼写错误。运行期完全静默地忽略它们，用户会以为设上了
	known := make(map[string]bool, len(settings))
	for _, s := range settings {
		known[strings.ToLower(s.Key)] = true
	}
	unknown := make([]string, 0, len(raw))
	for k := range raw {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		issues = append(issues, Issue{
			Level: LevelError,
			Message: l10n.T("Unknown setting {{.Key}} (possible typo); flk silently ignores it",
				map[string]any{"Key": k}),
		})
	}

	for _, s := range settings {
		v, ok := raw[strings.ToLower(s.Key)]
		if !ok {
			continue // 缺键不是问题：缺键就用默认值
		}

		switch s.Kind {
		case KindList:
			issues = append(issues, validateList(s, v)...)
		case KindString:
			text, ok := v.(string)
			if !ok {
				// 类型不对属硬错误：运行期会整项回退默认值，用户设了等于没设
				issues = append(issues, Issue{
					Level: LevelError,
					Message: l10n.T("Unexpected type for {{.Key}}: got {{.Type}}, expected a string; the value is ignored and the default is used",
						map[string]any{"Key": s.Key, "Type": jsonTypeName(v)}),
				})
				continue
			}
			text = strings.TrimSpace(text)
			if text == "" {
				issues = append(issues, Issue{
					Level: LevelWarning,
					Message: l10n.T("{{.Key}} is empty; it is treated as unset and the default ({{.Default}}) applies",
						map[string]any{"Key": s.Key, "Default": ValueText(s.Default)}),
				})
				continue
			}
			if s.Parse == nil {
				continue
			}
			if _, err := s.Parse(text); err != nil {
				issues = append(issues, invalidValueIssue(s.Key, text, s.Expected))
			}
		}
	}
	return issues
}

// validateList 检查列表型设置项的形态与元素。
//
// 逐层检查的理由：运行期的 stringList 会**静默丢弃**非字符串元素与空白元素，
// 用户看到的只是"我明明写了这个主机却没放行"，因此这里必须把每一处丢弃都点名
func validateList(s Setting, v any) []Issue {
	list, ok := v.([]any)
	if !ok {
		return []Issue{{
			Level: LevelError,
			Message: l10n.T("Unexpected type for {{.Key}}: got {{.Type}}, expected an array; the value is ignored and the default is used",
				map[string]any{"Key": s.Key, "Type": jsonTypeName(v)}),
		}}
	}

	var issues []Issue
	seen := make(map[string]bool, len(list))
	for _, item := range list {
		text, isString := item.(string)
		if !isString {
			issues = append(issues, Issue{
				Level: LevelError,
				Message: l10n.T("Expected a string in {{.Key}}, got {{.Type}}; the entry is silently dropped",
					map[string]any{"Key": s.Key, "Type": jsonTypeName(item)}),
			})
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			issues = append(issues, Issue{
				Level:   LevelWarning,
				Message: l10n.T("Empty entry in {{.Key}}; it is silently dropped", map[string]any{"Key": s.Key}),
			})
			continue
		}
		if seen[trimmed] {
			// 重复只影响可读性：运行期的白名单是集合语义，重复项不会带来额外放行
			issues = append(issues, Issue{
				Level:   LevelWarning,
				Message: l10n.T("Duplicate entry in {{.Key}}: {{.Value}}", map[string]any{"Key": s.Key, "Value": trimmed}),
			})
		}
		seen[trimmed] = true
	}
	return issues
}

// invalidValueIssue 生成"值非法"的体检条目，模板与 flk config set 的报错保持一致。
// 两处共用同一模板，用户从 set 与 validate 拿到的说法才是同一件事
func invalidValueIssue(key, value, expected string) Issue {
	return Issue{
		Level: LevelError,
		Message: l10n.T("Invalid value for {{.Key}}: {{.Value}} (expected {{.Expected}})",
			map[string]any{"Key": key, "Value": value, "Expected": expected}),
	}
}

// jsonTypeName 返回值的 JSON 类型名，用于提示用户。
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case json.Number, float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}
