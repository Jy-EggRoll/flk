package logger

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"sync/atomic"
	"time"
)

// globalLogger 通过原子指针发布完整构造的日志实例
// 包加载时会放入默认实例，logger 函数仍保留 nil 回退，确保测试重置或异常初始化顺序下调用便捷函数也不会 panic
var globalLogger atomic.Pointer[slog.Logger]

func init() {
	globalLogger.Store(newLogger(DefaultConfig()))
}

// Init 使用给定配置替换包内日志实例，但不会修改 slog.Default
// config 为 nil 时采用默认配置；Writer 为 nil 时回退到 os.Stderr，保证不完整配置也具有确定且安全的输出目标
func Init(config *Config) {
	if config == nil {
		config = DefaultConfig()
	}

	resolved := *config
	if resolved.Writer == nil {
		resolved.Writer = os.Stderr
	}
	globalLogger.Store(newLogger(&resolved))
}

// newLogger 根据已解析配置创建标准库 TextHandler
// TextHandler 原生支持键值对和 slog.Attr；关闭时间时用 ReplaceAttr 移除内建 time 字段，调用方字段则由 AddSource 控制
func newLogger(config *Config) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:     config.Level,
		AddSource: config.ShowSource,
	}
	if !config.ShowTime {
		opts.ReplaceAttr = func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		}
	}
	return slog.New(slog.NewTextHandler(config.Writer, opts))
}

// logger 返回当前日志实例，并在极端情况下以默认配置原子补建实例
// CompareAndSwap 避免多个并发调用各自覆盖已经发布的实例，同时让读取路径只承担一次原子加载成本
func logger() *slog.Logger {
	if current := globalLogger.Load(); current != nil {
		return current
	}

	fallback := newLogger(DefaultConfig())
	if globalLogger.CompareAndSwap(nil, fallback) {
		return fallback
	}
	return globalLogger.Load()
}

// log 生成带正确业务调用点的 slog.Record，并沿用 slog 对结构化参数的解析规则
// runtime.Callers 跳过自身、log 和 Debug/Info/Warn/Error 包装层，因此 AddSource 输出的是调用 logger 包的业务代码而非 logger.go
func log(level slog.Level, message string, args ...any) {
	current := logger()
	ctx := context.Background()
	if !current.Enabled(ctx, level) {
		return
	}

	var pcs [1]uintptr
	runtime.Callers(3, pcs[:])
	record := slog.NewRecord(time.Now(), level, message, pcs[0])
	record.Add(args...)
	_ = current.Handler().Handle(ctx, record)
}

// Debug 记录调试级别日志；默认 Warn 配置会过滤该级别，verbose 两次或 Debug 环境配置会开启输出和业务调用方
func Debug(message string, args ...any) {
	log(slog.LevelDebug, message, args...)
}

// Info 记录信息级别日志，并完整保留键值对或 slog.Attr 形式的结构化参数
func Info(message string, args ...any) {
	log(slog.LevelInfo, message, args...)
}

// Warn 记录警告级别日志；这是默认配置允许输出的最低级别
func Warn(message string, args ...any) {
	log(slog.LevelWarn, message, args...)
}

// Error 记录错误级别日志；进程退出由命令层显式处理，日志包不再提供会中断控制流的 Fatal
func Error(message string, args ...any) {
	log(slog.LevelError, message, args...)
}
