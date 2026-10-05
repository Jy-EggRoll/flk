// store.go 负责设置文件的原始读写，本包对设置文件的 I/O 都集中在这里。
//
// 读**容错**、写**单键**，这是两条路径的分工：
//   - 读：ReadRawAt / Load / LoadLanguage（键统一小写，容错解析，尽可能多读出内容）
//   - 写：SetKeyAt / UnsetKeyAt / ResetAllAt（保留键的原始顺序与大小写，
//     基于原始键值合并后原子落盘）
//
// 为什么不引入 viper 的写能力：viper 的 WriteConfig 走 AllSettings()，把内存里的全部键
// 一次性落盘，**无法表达"把某个键删掉"**——而 reset 的默认模式正是删键。
//
// 读同样不走 viper：弱类型容错实际来自"字段级回退默认值"（见 Setting.effective），
// 大小写归一与重复键检测由本文件的 normalizeKeys / decodeObject 负责，
// 读、写、体检因而共用同一套解析规则，不会分叉成"能跑但体检说不行"
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jy-eggroll/eggokit/atomicfile"
	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/flk/internal/pathutil"
)

// ErrUnknownKey 表示请求了一个未注册的设置项。
// 命令层据此把错误换成"未知键 + 指向 flk config --help"的统一提示
var ErrUnknownKey = errors.New("config: unknown key")

// settingEntry 是设置文件里的一条键值对：key 保留文件中的原始写法，value 保留原始 JSON 形态。
//
// 为什么不直接复用 LoadAt 的 Config 结构体来写回：Config 只认识注册表里的键，
// 用它重建文件会静默丢掉用户手写的其它键（见 writeKeyAt 的硬要求 2）。
// value 用 json.RawMessage 原样留存，保证不改动用户写的数值精度与转义细节；
// 但**内部排版会被规范化**（单行数组/对象展开成多行，见 writeEntriesAt），
// 这是为了让"同一份设置"永远序列化成同一串字节，diff 才稳定
type settingEntry struct {
	key   string
	value json.RawMessage
}

// ReadRawAt 读取设置文件的原始键值并规范化键名（小写）。
// 文件不存在时返回空 map 而非错误——调用方据此得到全默认配置；
// 其他错误（权限、目录、JSON 语法）必须返回 error，避免静默吞掉损坏的设置文件
func ReadRawAt(path string) (map[string]any, error) {
	expanded, err := pathutil.NormalizePath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(expanded)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	return parseRaw(data)
}

// parseRaw 把设置文件的字节解析为规范化键名后的原始键值。
// 单列出来是为了让体检（validate.go）能复用同一套解析，不必各自实现一遍——
// 两套解析迟早会分叉成"能跑但体检说不行"
func parseRaw(data []byte) (map[string]any, error) {
	raw, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	return normalizeKeys(raw), nil
}

// decodeObject 把设置文件的字节解析成顶层对象，键保持文件里的原始写法。
//
// 错误信息刻意带上路径：设置在启动路径上被读取，报错必须能让用户立刻知道去看哪个文件
func decodeObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	// UseNumber 让数字保持字面量，避免用户手写的其它键里的大整数被 float64 静默改写
	//（如 12345678901234567890 变成 12345678901234567000），也避免 1e400 让解析直接失败
	dec.UseNumber()

	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	// 文件内容为 null 时 Decode 得到 nil map，后面直接赋值会 panic
	if raw == nil {
		raw = map[string]any{}
	}
	return raw, nil
}

// normalizeKeys 把 map 的键统一小写，避免同一个键出现大小写两种写法导致读取歧义。
//
// 小写是必需的：LoadAt 按小写键名查表，不归一则文件里的 "Language" 永远读不到。
// 潜在影响点：出现仅大小写不同的重复键（同时写了 "language" 与 "Language"）时，
// 本函数是**后写覆盖**、结果取决于 map 的随机遍历顺序，因此该文件读到哪个值不确定。
// 这里刻意不在读路径上直接报错——那会让整份设置文件（含其它正常字段）全部作废，
// 代价远大于收益；取而代之的是让 flk config validate 把它作为错误报出来，
// 用户有明确且不破坏读路径的修复入口
func normalizeKeys(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out
}

// Get 使用默认路径，语义与 GetAt 完全相同。
func Get(key string) (any, error) {
	path, err := defaultPath()
	if err != nil {
		return nil, err
	}
	return GetAt(path, key)
}

// GetAt 返回某个设置项"只看设置文件"意义上的生效值：文件里有就用文件的值，没有就用默认值。
//
// 刻意不采纳命令行 --lang 这类临时覆盖——那只是本次运行的参数，不属于设置。若混进来，
// `flk --lang zh-CN config get language` 会打印 zh-CN，让脚本作者以为设置被改过。
// 取值一律经 Setting.effective，因此与运行期加载（LoadAt）的判定必然同源
func GetAt(path, key string) (any, error) {
	s, ok := Lookup(key)
	if !ok {
		return nil, ErrUnknownKey
	}
	raw, err := ReadRawAt(path)
	if err != nil {
		return nil, err
	}
	return s.effective(raw), nil
}

// effectiveString 取出某个字符串型设置项的生效值。
// 类型断言不会失败：注册表保证这类键的默认值与 effective 的返回值都是字符串，
// 断言失败只可能源于注册表被写错，此时返回零值比 panic 更合适
func effectiveString(key string, raw map[string]any) string {
	s, ok := Lookup(key)
	if !ok {
		return ""
	}
	text, _ := s.effective(raw).(string)
	return text
}

// effectiveList 取出某个列表型设置项的生效值（语义同 effectiveString）
func effectiveList(key string, raw map[string]any) []string {
	s, ok := Lookup(key)
	if !ok {
		return nil
	}
	list, _ := s.effective(raw).([]string)
	return list
}

// SetKey 写入单个设置项（默认路径），语义与 SetKeyAt 完全相同。
func SetKey(key string, value any) error {
	path, err := defaultPath()
	if err != nil {
		return err
	}
	return SetKeyAt(path, key, value)
}

// SetKeyAt 写入单个设置项，保留文件里的其他键（含未知键）。
//
// 键必须是注册表里的项：传进来一个拼错的键名却照写不误，等于往文件里写了一个
// 永远读不到的键，用户还会以为设置生效了。因此未知键一律返回 ErrUnknownKey，
// 命令层据此给出指向 flk config --help 的提示
func SetKeyAt(path, key string, value any) error {
	s, ok := Lookup(key)
	if !ok {
		return ErrUnknownKey
	}
	return writeKeyAt(path, s.Key, value)
}

// UnsetKey 删除单个设置项（默认路径），语义与 UnsetKeyAt 完全相同。
func UnsetKey(key string) error {
	path, err := defaultPath()
	if err != nil {
		return err
	}
	return UnsetKeyAt(path, key)
}

// UnsetKeyAt 删除单个设置项，使该项回落到内置默认值。
// 键本来就不存在时是空操作（幂等），便于 reset 被重复执行
func UnsetKeyAt(path, key string) error {
	s, ok := Lookup(key)
	if !ok {
		return ErrUnknownKey
	}

	entries, err := readEntriesAt(path)
	if err != nil {
		return err
	}

	kept := entries[:0]
	for _, e := range entries {
		// 大小写不敏感地匹配：文件里可能写着 "Language"，按字面量比较会漏删
		if strings.EqualFold(strings.TrimSpace(e.key), s.Key) {
			continue
		}
		kept = append(kept, e)
	}
	return writeEntriesAt(path, kept)
}

// writeKeyAt 把单个键写进文件：定位到已有条目就替换其值，否则按注册表里的规范键名追加到末尾。
//
// 三条硬要求及其理由：
//  1. **原子写**：先写同目录下的临时文件再 rename。直接截断重写的话，写到一半崩溃或断电
//     会留下残缺的 JSON；而这份文件同时是"启动语言"的来源，残缺内容会让 language 读取失败、
//     退化为默认语言——在用户眼里与"设置被清空"没有区别
//  2. **保留未知字段**：设置文件是用户手工可编辑的，除注册表里的键之外还可能有
//     用户自己写的键（注释性字段、实验开关、第三方工具的配置）。只认自己那几个键并整体重写
//     会静默抹掉它们，属于"工具动了用户的数据却没打招呼"。因此基于原始键值合并
//  3. **键序稳定**：保住文件里原有的键顺序，新键追加在末尾。JSON 对象本身无序，但用户会
//     diff 这个文件；每次写回都把键重排一遍，会让 diff 里出现大量与本次修改无关的行
//
// 与读路径的一处刻意差异：本函数保留键的**原始大小写**（"allowHosts" 不会被写成
// "allowhosts"），匹配已有条目时才大小写不敏感（口径与 normalizeKeys 一致，
// 否则用户写成 "Language" 时会变成"旧值还在 + 追加一个新键"的重复条目）。
// 读路径把键统一小写是为了容错读取；写路径若也小写化，就等于悄悄改写了用户手写的键名
func writeKeyAt(path, key string, value any) error {
	entries, err := readEntriesAt(path)
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}

	replaced := false
	for i := range entries {
		if strings.EqualFold(strings.TrimSpace(entries[i].key), key) {
			entries[i].value = encoded
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, settingEntry{key: key, value: encoded})
	}

	return writeEntriesAt(path, entries)
}

// ResetAll 重置全部设置（默认路径），语义与 ResetAllAt 完全相同。
func ResetAll(writeDefaults bool) error {
	path, err := defaultPath()
	if err != nil {
		return err
	}
	return ResetAllAt(path, writeDefaults)
}

// ResetAllAt 重置全部设置，两种模式：
//
//	writeDefaults=false → 删除设置文件，回到"从未配置过"的状态
//	writeDefaults=true  → 写入一份全默认值的设置文件
//
// 写默认值这条路径**刻意不是"删了再写"**：那样会留下"删成功、写失败"的窗口，设置会平白消失。
// 直接从注册表的默认值起步做一次原子写，中途失败则原文件完好
func ResetAllAt(path string, writeDefaults bool) error {
	if !writeDefaults {
		expanded, err := pathutil.NormalizePath(path)
		if err != nil {
			return err
		}
		// 走 EvalSymlinks：用户可能把设置文件链进自己的配置仓库，
		// 直接按链接路径删除只会删掉链接本身，仓库里那份"垃圾文件"原封不动地留着
		if resolved, resolveErr := filepath.EvalSymlinks(expanded); resolveErr == nil {
			expanded = resolved
		}
		if err := os.Remove(expanded); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeEntriesAt(path, defaultEntries())
}

// defaultEntries 返回一份全默认值的键值对，默认值取自注册表，顺序即注册表顺序。
//
// 顺序取自注册表而不是 map：写出来的文件键序稳定，用户 diff 时不会看到本次修改之外的重排。
// 值是注册表里的切片时会被 json.Marshal 复制一份，因此这里不会与注册表共享可变状态
func defaultEntries() []settingEntry {
	entries := make([]settingEntry, 0, len(settings))
	for _, s := range settings {
		encoded, err := json.Marshal(s.Default)
		if err != nil {
			// 注册表的默认值都是基本类型或字符串切片，Marshal 不会失败；
			// 走到这里说明注册表被写错了，属于内部不变式被破坏而非用户的文件有问题，
			// 因此用英文而不是 l10n.T，免得为一条不可达分支增加翻译负担
			continue
		}
		entries = append(entries, settingEntry{key: s.Key, value: encoded})
	}
	return entries
}

// readEntriesAt 按文件中的原始顺序读出顶层键值对。
//
// 用 json.Decoder 的 Token/Decode 逐个成员读取，而不是 Unmarshal 到 map：
// map 会丢掉键的顺序（Go 的 map 遍历顺序随机），而写回时要求键序稳定（见 writeKeyAt）。
//
// 文件不存在或只有空白时返回空列表：首次运行直接新建即可，不算错误。
// 其它错误（权限、非法 JSON、顶层不是对象）一律返回，让调用方放弃写入
func readEntriesAt(path string) ([]settingEntry, error) {
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
	// 空白文件（例如用户 touch 出来还没写内容的）与不存在等价，不必按"非法 JSON"拒绝
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%s", l10n.T("The settings file {{.Path}} is not valid JSON: {{.Err}}",
			map[string]any{"Path": expanded, "Err": err.Error()}))
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("%s", l10n.T("The settings file {{.Path}} must contain a JSON object at the top level", map[string]any{"Path": expanded}))
	}

	var entries []settingEntry
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			// 对象上下文里的键必然是字符串（json.Decoder 的保证），因此这是内部不变式被破坏，
			// 不是用户的文件有问题：用英文而不是 l10n.T，免得为一条不可达分支增加翻译负担
			return nil, fmt.Errorf("config: internal error: non-string object key in %s", expanded)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		entries = append(entries, settingEntry{key: key, value: raw})
	}
	return entries, nil
}

// writeEntriesAt 把键值对序列化成格式规范的 JSON 对象并原子落盘。
//
// 输出风格对齐人工编辑习惯：2 空格缩进、末尾留一个换行。末尾换行不是洁癖——
// 缺了它，用户手工改完再被程序写回时，diff 里会平白多出一行 "\ No newline at end of file"
//
// 值的内部排版交给 json.Indent 规范化：单行数组/对象会被展开成多行。
// 代价是首次写回时 diff 会大一点（用户原来的紧凑写法被展开），换来的是
// "同一份设置永远序列化成同一串字节"——否则每次写回都可能因排版差异产生噪声 diff
func writeEntriesAt(path string, entries []settingEntry) error {
	expanded, err := pathutil.NormalizePath(path)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, e := range entries {
		buf.WriteString("  ")
		key, err := json.Marshal(e.key)
		if err != nil {
			return err
		}
		buf.Write(key)
		buf.WriteString(": ")
		// 对本就多行的值（嵌套对象/数组）补缩进，使整份文件风格一致。
		// json.Indent 的第一行不加前缀，正好接在 "键: " 之后，其余行按 prefix+indent 缩进
		var value bytes.Buffer
		if err := json.Indent(&value, e.value, "  ", "  "); err != nil {
			// 值来自 json.RawMessage 或 json.Marshal，本应是合法 JSON；走到这里说明上游有 bug，
			// 属于内部不变式被破坏而非用户的文件有问题，因此用英文而不是 l10n.T
			return fmt.Errorf("config: internal error: cannot indent the value of key %q: %w", e.key, err)
		}
		buf.Write(value.Bytes())
		if i < len(entries)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")

	return atomicfile.Write(expanded, buf.Bytes())
}
