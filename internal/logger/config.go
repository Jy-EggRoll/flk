package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jy-eggroll/flk/pkg/l10n"
	"strings"
)

// defaultLevelText 是内置默认日志级别的文本形式，必须与 DefaultConfig 的 Level 同义。
//
// 为什么单独留一份文本：设置文件（~/.config/flk/flk-config.json）的 logLevel 项以它为默认值，
// 而设置文件里存的是文本。若注册表写死一个字面量，日后改默认级别就会出现
// 「flk config show 显示的默认值」与「实际生效的级别」两个真相漂移，
// 因此文本只在这里声明一次，registry 通过 DefaultLevelText 取用，
// 并由 logger_test 的 TestDefaultLevelTextMatchesDefaultConfig 锁住两处一致
//
// 同时提醒：本项目**刻意不再读取任何日志级别环境变量**（原 FLK_LOG_LEVEL 已移除），
// 级别来源只有命令行 -v/-vv 与设置文件两条，避免多一条看不见的取值来源
const defaultLevelText = "warn"

// DefaultLevelText 返回内置默认日志级别的文本形式。
// 供设置注册表声明 logLevel 的默认值与合法取值描述，避免那两处硬编码一份平级知识
func DefaultLevelText() string { return defaultLevelText }

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

// LogLevelFromString 将文本转换为标准 slog 级别，入参是设置文件里的 logLevel 取值
// 解析前会去除首尾空白并忽略大小写；仅接受对外承诺的 debug、info、warn、error，防止拼写错误静默改变日志量
// 潜在影响点：internal/config 的注册表复用本函数做取值校验，因此**本函数是"合法级别"的唯一真相**，
// 与之并存的只有 DefaultLevelText 给出的默认值文本，两者的一致性由单测锁定
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
		return 0, fmt.Errorf("%s", l10n.T("Unsupported log level {{.Level}}; only debug, info, warn, and error are supported", map[string]any{"Level": level}))
	}
}

// FromLevelText 按文本级别构造日志配置，供命令层把设置文件里的 logLevel 转成运行期配置
// 空串（含纯空白）表示"未设置"，沿用默认 Warn 配置，命令层不必自己判空；
// 非空非法值返回错误，交由命令层决定如何向用户报告
// Info 配置自动开启时间，Debug 在时间之外再开启源码位置，使设置文件与 verbose 覆盖产生一致的分层输出
//
// 刻意不接收路径也不自己去读设置文件：本包属于底层设施，读文件失败该如何降级属于命令层的策略
// （flk 的取向是"设置文件有问题也不让命令失败，只回退默认级别并告警"，见 cmd/root.go 的 prepareCommand）
func FromLevelText(level string) (*Config, error) {
	if strings.TrimSpace(level) == "" {
		return DefaultConfig(), nil
	}

	parsed, err := LogLevelFromString(level)
	if err != nil {
		return nil, err
	}
	config := DefaultConfig()
	applyLevel(config, parsed)
	return config, nil
}

// ApplyVerbose 根据 -v 的累计次数覆盖日志分层，同时保留 Writer 等非分层配置
// count 小于等于零时使用 Warn，等于一时使用带时间的 Info，大于等于二时使用同时带时间和业务调用方的 Debug
// 返回配置副本而不是修改传入值，便于命令层先读取设置文件里的级别，再安全地应用命令行最高优先级覆盖
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
// 该函数只服务于设置文件与 verbose 两种策略入口；直接构造 Config 时仍可独立控制 ShowTime 与 ShowSource
func applyLevel(config *Config, level slog.Level) {
	config.Level = level
	config.ShowTime = level <= slog.LevelInfo
	config.ShowSource = level <= slog.LevelDebug
}
