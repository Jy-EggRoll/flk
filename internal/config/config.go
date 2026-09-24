// config 包管理 flk 的 JSON 设置文件，默认路径：~/.config/flk/flk-config.json
//
// 与 store 包（flk-store.json，记录链接清单）不同，本包保存的是**软件自身的运行设置**。
// 当前只有一个设置项：
//   - language: 输出语言（如 "en"、"zh-CN"），默认 "en"；命令行 --lang 与 FLK_LANG 优先级更高
//
// 刻意不引入 viper：语言必须在 cobra 解析命令行之前确定（--help 不执行 PersistentPreRunE），
// 这条路径上只需要裸 JSON 读取一个字段，viper 带来的全局单例与重依赖得不偿失。
// 读取语言走 LoadLanguage，它永远不终止进程，保证帮助信息在任何情况下都能打印。
//
// 本包只读不写：设置文件由用户手工维护，避免为 l10n 迁移引入一整套尚未成型的配置管理命令。
package config

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/jy-eggroll/flk/internal/locales"
	"github.com/jy-eggroll/flk/internal/pathutil"
)

// DefaultConfigPath 是设置文件的默认路径，与 store 的 flk-store.json 同目录，
// 便于用户集中管理；~ 由 pathutil 展开。
const DefaultConfigPath = "~/.config/flk/flk-config.json"

// Config 是设置文件的 Go 结构体映射。
type Config struct {
	Language string `json:"language"`
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
	applyDefaults(cfg)
	return cfg, nil
}

// applyDefaults 对未显式设置的字段补默认值：language 为空时取默认语言。
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
