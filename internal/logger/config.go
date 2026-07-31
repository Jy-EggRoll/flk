package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

const logLevelEnv = "FLK_LOG_LEVEL"

// Config 描述创建日志实例所需的真实运行参数
// Writer 允许命令层或测试注入目标输出流；传入 nil 时 Init 会安全回退到 os.Stderr
// ShowTime 和 ShowSource 分别控制标准 TextHandler 的时间与源码位置字段，避免把展示策略耦合到业务调用处
// Level 使用标准库 slog.Level，确保过滤规则和结构化日志语义完全遵循 slog
// Config 在 Init 时会被读取并复制到不可变的 handler 配置中，调用方后续修改不会影响已初始化实例
type Config struct {
	Level      slog.Level
	Writer     io.Writer
	ShowTime   bool
	ShowSource bool
}

// DefaultConfig 返回适合普通命令输出的基础配置
// 默认只输出 Warn 及以上级别，不展示时间和调用方，并写入标准错误流，避免日志污染标准输出中的业务数据
func DefaultConfig() *Config {
	return &Config{
		Level:      slog.LevelWarn,
		Writer:     os.Stderr,
		ShowTime:   false,
		ShowSource: false,
	}
}

// LogLevelFromString 将环境变量中的文本转换为标准 slog 级别
// 解析前会去除首尾空白并忽略大小写；仅接受对外承诺的 debug、info、warn、error，防止拼写错误静默改变日志量
func LogLevelFromString(level string) (slog.Level, error) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("不支持的日志级别 %q，仅支持 debug、info、warn、error", level)
	}
}

// FromEnv 从当前进程环境构造日志配置
// 未设置 FLK_LOG_LEVEL 或显式设置为空字符串时保留默认 Warn 配置；非空非法值会返回错误，交由命令层决定如何向用户报告
// Info 配置自动开启时间，Debug 在时间之外再开启源码位置，使环境配置和 verbose 覆盖产生一致的分层输出
func FromEnv() (*Config, error) {
	config := DefaultConfig()
	levelText, exists := os.LookupEnv(logLevelEnv)
	if !exists || levelText == "" {
		return config, nil
	}

	level, err := LogLevelFromString(levelText)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", logLevelEnv, err)
	}
	applyLevel(config, level)
	return config, nil
}

// ApplyVerbose 根据 -v 的累计次数覆盖日志分层，同时保留 Writer 等非分层配置
// count 小于等于零时使用 Warn，等于一时使用带时间的 Info，大于等于二时使用同时带时间和业务调用方的 Debug
// 返回配置副本而不是修改传入值，便于命令层先读取环境配置，再安全地应用命令行最高优先级覆盖
func ApplyVerbose(config *Config, count int) *Config {
	if config == nil {
		config = DefaultConfig()
	}

	overridden := *config
	switch {
	case count >= 2:
		applyLevel(&overridden, slog.LevelDebug)
	case count == 1:
		applyLevel(&overridden, slog.LevelInfo)
	default:
		applyLevel(&overridden, slog.LevelWarn)
	}
	return &overridden
}

// applyLevel 同步设置过滤级别及其约定的展示层次
// 该函数只服务于环境变量和 verbose 两种策略入口；直接构造 Config 时仍可独立控制 ShowTime 与 ShowSource
func applyLevel(config *Config, level slog.Level) {
	config.Level = level
	config.ShowTime = level <= slog.LevelInfo
	config.ShowSource = level <= slog.LevelDebug
}
