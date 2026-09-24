// jsonfile 提供项目统一的 JSON 文件序列化形态。
//
// flk 会写两类 JSON 文件：用户设置（internal/config）与语言文件（pkg/l10n/cmd/l10n）。
// 两者都要求"人可读、diff 可评审"，格式要求完全一致，所以把这条策略收口在这里，
// 而不是在每个写入点各配一遍 encoder。
package jsonfile

import (
	"bytes"
	"encoding/json"
)

// Marshal 以项目统一的规范形态序列化 JSON：
// 键按字典序、2 空格缩进、不转义 HTML、结尾带换行。
//
// 键的字典序由 encoding/json 编码 map 时的固有行为保证，无需额外排序。
//
// SetEscapeHTML(false) 是刻意的：路径与文案里常见的 & < > 若被转义成 \u0026 之类，
// 文件既难读也难 diff。Go 标准库默认**开启**该转义，很容易被无声踩到——本函数就是
// 为了不让每个写入点各自记得关它而存在的。
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
