// config 包管理 flk 的 JSON 设置文件，默认路径：~/.config/flk/flk-config.json
//
// 与 store 包（flk-store.json，记录链接清单）不同，本包保存的是**软件自身的运行设置**。
// 设置项的权威清单是 settings.go 的注册表，当前有三项：
//   - language: 输出语言（如 "en"、"zh-CN"），默认 "en"；命令行 --lang 优先级更高
//   - allowHosts: WebUI 访问白名单的长期授权列表（字符串数组），与命令行 --allow-host 取并集；
//     详见 Config.AllowHosts 的注释
//   - logLevel: 日志级别（debug/info/warn/error），默认 "warn"；命令行 -v/-vv 优先级更高
//
// 本包的取值来源只有两处：设置文件与命令行。**刻意不读取任何配置类环境变量**——
// 曾经的 FLK_LANG 与 FLK_LOG_LEVEL 已全部移除，因为"三处来源、优先级各不相同"一旦叠加
// 就很难解释清楚（用户改了文件不生效、却想不起自己什么时候 export 过变量），
// 而持久化设置已经由 `flk config` 命令子树正式承担
//
// 本包的文件分工（照 ggt 的结构，四个操作共用一份键定义）：
//   - settings.go：设置项注册表，键名/类型/默认值/期望值描述/解析器
//   - store.go：设置文件的原始读写与单键增删改（原子写、保留其它键与键序）
//   - validate.go：flk config validate 的体检逻辑
//   - config.go（本文件）：Config 结构体、加载与默认值补全、语言专用入口
//
// 为什么不引入 viper：语言必须在 cobra 解析命令行之前确定（--help 不执行 PersistentPreRunE），
// 这条路径上只需要直接读 JSON 一个字段；另外 viper 的 WriteConfig 走 AllSettings() 一次性落盘，
// 无法表达"把某个键删掉"（而 reset 的默认模式正是删键）。读全量、写单键因此留在本包自己实现
//
// 与 l10n 的关系：写入失败的错误会经 /api/language 原样回显在网页上（"保存失败: ..."），
// 属于用户可见文案，因此用 l10n.T 生成而不是硬编码某一种语言。
// 反过来，只有内部不变式被破坏才可能出现的错误（例如解码器给出了非字符串的键）保持英文
// 且不带 l10n：它们不该出现在用户面前，也不值得占用语言文件条目
package config

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/jy-eggroll/flk/internal/pathutil"
)

// DefaultConfigPath 是设置文件的默认路径，与 store 的 flk-store.json 同目录，
// 便于用户集中管理；~ 由 pathutil 展开。
// 对外可见的路径入口是 DefaultPath，它返回展开后的绝对路径（flk config path 用的就是它）
const DefaultConfigPath = "~/.config/flk/flk-config.json"

// Config 是设置文件的 Go 结构体映射，字段顺序即 flk config show 输出 JSON 的字段顺序。
//
// 它是"读全量 + 补默认值"这条路径的载体，也是 WebUI 侧读取设置的入口。
// 注意它**不参与写回**：写路径基于原始键值合并（见 store.go 的 SetKeyAt），
// 因为结构体只认识注册表里的键，用它重建文件会静默丢掉用户手写的其它键
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
	//
	// 序列化时保证是 [] 而不是 null：两者的语义差别（"没有额外授权"与"这个字段没设置"）
	// 不体现在行为上，但会让 flk config show 与 reset --defaults 写出的文件长得不一样
	AllowHosts []string `json:"allowHosts"`

	// LogLevel 是日志级别，取值 debug/info/warn/error，命令行 -v/-vv 优先级更高。
	// 详见 internal/logger 的 FromLevelText——那里才是"文本转 slog 级别"的唯一实现
	LogLevel string `json:"logLevel"`
}

// defaultPath 返回展开后的默认设置文件路径。
// 返回 error 而不是终止进程：本函数可能在 --help 之前被调用，此时退出会导致连帮助都打不出来
func defaultPath() (string, error) {
	return pathutil.NormalizePath(DefaultConfigPath)
}

// DefaultPath 返回展开后的默认设置文件路径，供 `flk config path` 打印。
// 与内部调用共用 defaultPath，避免"打印出来的路径"与"实际读写的路径"各写一遍而漂移
func DefaultPath() (string, error) {
	return defaultPath()
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

// SetLanguage 把语言写进默认路径的设置文件，供 WebUI 切换语言时持久化。
//
// 它只是 SetKey("language", ...) 的具名入口，单独留一个名字是因为调用点
// （cmd/serve_web.go 的 /api/language）讲的是"语言要落盘"这件事，而不是"往配置里写一个键"。
// 空语言由注册表的解析器拒绝，写路径本身不再重复判空
func SetLanguage(lang string) error {
	parsed, err := parseLanguage(lang)
	if err != nil {
		return err
	}
	return SetKey("language", parsed)
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
//
// 取值一律经注册表的 effective 得到，因此"读取时认不认这个写法"与
// "flk config get 会打印什么"必然同源，不存在两套判定。
// 容错取向是「能读多少读多少」：单个字段类型写错只影响该字段（回退默认值），
// 不会让整份设置文件作废——否则用户明明只是把 allowHosts 写成了字符串，
// 却会连带丢掉 language 等有效设置
func LoadAt(path string) (*Config, error) {
	raw, err := ReadRawAt(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Language:   effectiveString("language", raw),
		AllowHosts: effectiveList("allowHosts", raw),
		LogLevel:   effectiveString("logLevel", raw),
	}
	applyDefaults(cfg)
	return cfg, nil
}

// applyDefaults 把结构体里"空的"字段对齐到注册表的默认值。
//
// 严格说 effective 已经返回过默认值，这里只需处理一件事：AllowHosts 为空时对齐成
// 非 nil 的空切片，让 flk config show 输出 [] 而不是 null（见 Config.AllowHosts 的注释）。
// 其余字段在 effective 里就已保证非空，这里不必重复判断
func applyDefaults(cfg *Config) {
	if cfg.AllowHosts == nil {
		cfg.AllowHosts = []string{}
	}
}
