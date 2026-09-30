// config 包管理 flk 的 JSON 设置文件，默认路径：~/.config/flk/flk-config.json
//
// 与 store 包（flk-store.json，记录链接清单）不同，本包保存的是**软件自身的运行设置**。
// 当前有两个设置项：
//   - language: 输出语言（如 "en"、"zh-CN"），默认 "en"；命令行 --lang 与 FLK_LANG 优先级更高
//   - allowHosts: WebUI 访问白名单的长期授权列表（字符串数组），与命令行 --allow-host 取并集；
//     详见 Config.AllowHosts 的注释
//
// 刻意不引入 viper：语言必须在 cobra 解析命令行之前确定（--help 不执行 PersistentPreRunE），
// 这条路径上只需要裸 JSON 读取一个字段，viper 带来的全局单例与重依赖得不偿失。
// 读取语言走 LoadLanguage，它永远不终止进程，保证帮助信息在任何情况下都能打印。
//
// 读**全量**、写**单键**，这是本包两条路径的分工：
//   - 读：Load/LoadLanguage 走 ReadRawAt（键统一小写，容错解析，尽可能多读出内容）
//   - 写：SetLanguage 走 readEntriesAt/writeEntriesAt（保留键的原始顺序与大小写，
//     基于原始键值合并后原子落盘，见 SetLanguageAt 的注释）
//
// 为什么要写入能力：WebUI 上切换语言必须能持久化到 ~/.config/flk/flk-config.json，
// 否则重启后又回到旧语言，用户会以为切换压根没生效
//
// 与 l10n 的关系：写入失败的错误会经 /api/language 原样回显在网页上（"保存失败: ..."），
// 属于用户可见文案，因此用 l10n.T 生成而不是硬编码某一种语言。
// 反过来，只有内部不变式被破坏才可能出现的错误（例如解码器给出了非字符串的键）保持英文
// 且不带 l10n：它们不该出现在用户面前，也不值得占用语言文件条目
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jy-eggroll/flk/internal/locales"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/pkg/l10n"
)

// DefaultConfigPath 是设置文件的默认路径，与 store 的 flk-store.json 同目录，
// 便于用户集中管理；~ 由 pathutil 展开。
const DefaultConfigPath = "~/.config/flk/flk-config.json"

// Config 是设置文件的 Go 结构体映射。
type Config struct {
	Language string `json:"language"`

	// AllowHosts 是 WebUI 的访问白名单里「长期生效」的那部分，与命令行 --allow-host 取并集
	//
	// 这是**高危访问授权**：WebUI 能查看与编辑清单、检测并修复链接，后续还会支持解除链接，
	// 也就是能直接操作文件系统；放行某个主机等于授权该主机上的浏览器打开这个能操作文件的页面。
	// 因此这里列出的必须是用户信任的主机，绝不应写入 `*` 之类的通配（白名单只做字面量比对，通配不会匹配任何 Host）
	//
	// 为什么放进设置文件而不是只留命令行：--allow-host 每次启动都要重敲一遍，
	// 家庭/公司局域网里固定从某台机器访问的用户，需要一份「一次写好、长期生效」的载体；
	// 命令行形式保留，用于临时授权，两者是并集而不是覆盖关系
	AllowHosts []string `json:"allowHosts"`
}

// defaultPath 返回展开后的默认设置文件路径。
// 返回 error 而不是终止进程：本函数可能在 --help 之前被调用，此时退出会导致连帮助都打不出来。
func defaultPath() (string, error) {
	return pathutil.NormalizePath(DefaultConfigPath)
}

// LoadLanguage 只读取设置文件里的 language 字段，用于在命令行解析之前确定输出语言。
//
// 单独提供本函数而不是复用 Load，原因有三：
//   - 语言必须在 rootCmd.Execute() 之前确定（cobra 的 --help 不会执行 PersistentPreRunE）
//   - 只取一个字段，避免为纯展示路径做一次全量解码与默认值补全
//   - 主目录不可用等异常一律返回 error，由调用方回退到默认语言，绝不在此终止进程
//
// 文件不存在、不可读或格式非法时一律返回 error。
func LoadLanguage() (string, error) {
	path, err := defaultPath()
	if err != nil {
		return "", err
	}
	return LoadLanguageAt(path)
}

// LoadLanguageAt 是 LoadLanguage 的显式路径版本，供测试使用（测试不该碰真实用户目录）。
func LoadLanguageAt(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var probe struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	return strings.TrimSpace(probe.Language), nil
}

// Load 读取完整设置并补齐默认值。
// 文件不存在时返回全默认配置（不报错），便于首次运行直接使用。
func Load() (*Config, error) {
	path, err := defaultPath()
	if err != nil {
		return nil, err
	}
	return LoadAt(path)
}

// LoadAt 是 Load 的显式路径版本，供测试使用（测试不该碰真实用户目录）。
func LoadAt(path string) (*Config, error) {
	cfg := &Config{}
	raw, err := ReadRawAt(path)
	if err != nil {
		return nil, err
	}
	if v, ok := raw["language"].(string); ok {
		cfg.Language = strings.TrimSpace(v)
	}
	cfg.AllowHosts = parseAllowHosts(raw["allowhosts"])
	applyDefaults(cfg)
	return cfg, nil
}

// parseAllowHosts 容错解析 allowHosts 字段：只接受 JSON 数组，数组元素必须是字符串
//
// 为什么容错而不是报错：本包对设置文件的整体取向是「能读多少读多少」，与 language 的取值方式一致——
// 一个写错类型的字段不应该让整份设置文件作废（Load 返回 error 会让 WebUI 走降级分支，
// 用户明明只是把数组写成了字符串，却连带丢失 language 等其它有效设置，这不合理）
//
// 元素去掉首尾空白、丢弃空串：空白字符串归一化后是空主机名，而白名单里不存在空串
// （见 cmd/serve_guard.go 的 normalizeHost），留着它只会让用户误以为配置已经生效
// 注意这里刻意不做「归一化」（不去端口、不转小写）：那是 guard 的职责，且它同时作用于请求侧与允许侧，
// 在配置解析阶段提前归一反而会引入第二份可能漂移的实现
func parseAllowHosts(value any) []string {
	items, ok := value.([]any)
	if !ok {
		// 字段缺失时 raw["allowhosts"] 为 nil，同样走这里，得到 nil 切片
		return nil
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
	return hosts
}

// applyDefaults 对未显式设置的字段补默认值：language 为空时取默认语言。
// allowHosts 刻意不补默认值：它的空值（不额外授权任何主机）本身就是正确且安全的默认行为，
// 填任何默认主机都等于替用户开口子——WebUI 能操作文件，白名单只能是用户显式给出的结果
func applyDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.Language) == "" {
		cfg.Language = locales.Default
	}
}

// ReadRawAt 读取设置文件的原始键值并规范化键名（小写）。
// 文件不存在时返回空 map 而非错误——调用方据此得到全默认配置；
// 其他错误（权限、目录、JSON 语法）必须返回 error，避免静默吞掉损坏的设置文件。
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
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return normalizeKeys(raw), nil
}

// normalizeKeys 把 map 的键统一小写，避免同一个键出现大小写两种写法导致读取歧义。
func normalizeKeys(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out
}

// ---------- 写入 ----------

// settingEntry 是设置文件里的一条键值对：key 保留文件中的原始写法，value 保留原始 JSON 形态。
//
// 为什么不直接复用 LoadAt 的 Config 结构体来写回：Config 只认识 language/allowHosts，
// 用它重建文件会静默丢掉用户手写的其它键（见 SetLanguageAt 的第 2 条硬要求）。
// value 用 json.RawMessage 原样留存，保证不改动用户写的数值精度与转义细节；
// 但**内部排版会被规范化**（单行数组/对象展开成多行，见 writeEntriesAt），
// 这是为了让"同一份设置"永远序列化成同一串字节，diff 才稳定
type settingEntry struct {
	key   string
	value json.RawMessage
}

// SetLanguage 把语言写入默认路径的设置文件，语义与 SetLanguageAt 完全相同。
func SetLanguage(lang string) error {
	path, err := defaultPath()
	if err != nil {
		return err
	}
	return SetLanguageAt(path, lang)
}

// SetLanguageAt 把 language 字段写进 path 指向的设置文件（原子写、保留其它字段与键序）。
//
// 三条硬要求及其理由：
//  1. **原子写**：先写同目录下的临时文件再 rename。直接截断重写的话，写到一半崩溃或断电
//     会留下残缺的 JSON；而这份文件同时是"启动语言"的来源，残缺内容会让 language 读取失败、
//     退化为默认语言——在用户眼里与"设置被清空"没有区别
//  2. **保留未知字段**：设置文件是用户手工可编辑的，除 language/allowHosts 之外还可能有
//     用户自己写的键（注释性字段、实验开关、第三方工具的配置）。只认自己那几个键并整体重写
//     会静默抹掉它们，属于"工具动了用户的数据却没打招呼"。因此基于原始键值合并，
//     而不是基于 Config 结构体重建
//  3. **键序稳定**：保住文件里原有的键顺序，新键追加在末尾。JSON 对象本身无序，但用户会
//     diff 这个文件；每次写回都把键重排一遍，会让 diff 里出现大量与本次修改无关的行
//
// 与读路径的一处刻意差异：本函数保留键的**原始大小写**（"allowHosts" 不会被写成
// "allowhosts"），只有匹配 "language" 时才大小写不敏感（口径与 normalizeKeys 一致，
// 否则用户写成 "Language" 时会变成"读旧值 + 追加一个新键"的重复条目）。
// 读路径把键统一小写是为了容错读取；写路径若也小写化，就等于悄悄改写了用户手写的键名
//
// 文件不存在时按"只有 language 一个键"新建；文件存在但不是合法 JSON 对象时返回 error
// 且不做任何修改——看不懂的文件只能交给用户处理，覆盖它等于丢数据
func SetLanguageAt(path, lang string) error {
	trimmed := strings.TrimSpace(lang)
	if trimmed == "" {
		// 空语言在读取侧等价于"未设置"（LoadLanguageAt 返回空串，调用方回退默认语言），
		// 写进去只会让用户以为设置生效了，实际什么都没变
		return errors.New("config: language must not be empty")
	}

	entries, err := readEntriesAt(path)
	if err != nil {
		return err
	}

	value, err := json.Marshal(trimmed)
	if err != nil {
		// string 的 json.Marshal 不会失败，这里只是让错误路径完备
		return err
	}

	replaced := false
	for i := range entries {
		if strings.EqualFold(strings.TrimSpace(entries[i].key), "language") {
			entries[i].value = value
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, settingEntry{key: "language", value: value})
	}

	return writeEntriesAt(path, entries)
}

// readEntriesAt 按文件中的原始顺序读出顶层键值对。
//
// 用 json.Decoder 的 Token/Decode 逐个成员读取，而不是 Unmarshal 到 map：
// map 会丢掉键的顺序（Go 的 map 遍历顺序随机），而写回时要求键序稳定（见 SetLanguageAt）。
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
			// 值来自 json.RawMessage，本应是合法 JSON；走到这里说明上游解码有 bug，
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

	return writeFileAtomic(expanded, buf.Bytes())
}

// writeFileAtomic 通过"同目录临时文件 + rename"原子地替换目标文件。
//
// 临时文件必须与目标同目录：rename 只在同一文件系统内才是原子的，把临时文件放到
// 系统临时目录会退化成"复制 + 删除"，崩溃窗口依然存在。
//
// 顺序是 Write → Sync → Close → Chmod → Rename：rename 只保证"文件名替换"这一步原子，
// 不保证数据已落盘；缺了 Sync，断电后可能出现"新文件名 + 空内容"，效果等同于清空设置
func writeFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)

	// 目录可能不存在（首次运行，或用户把配置放到一个新位置），先按需创建
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// 权限沿用目标文件原有的权限位，首次创建时为 0644。
	// 设置文件里没有密钥，但也没必要因为一次写回而改变用户既有的权限设置
	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, ".flk-config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	// 失败路径统一清理临时文件：留着它既污染配置目录，也会让后续排查的人
	// 误以为"有一份没写完的设置"。成功路径上它已经被 rename 掉，Remove 会失败但无妨
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// CreateTemp 建出来的文件是 0600，落位前显式对齐用户原有的权限位
	if err = os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}
