package logger // 声明当前代码所属的包名为 logger
import (       // 导入代码依赖的外部包列表
	"log/slog" // 导入 Go 标准库的 slog 包，用于实现结构化日志记录功能
	"os"       // 导入 os 包，用于操作系统交互（如程序退出、文件操作）

	"github.com/pterm/pterm" // 导入 pterm 第三方库，提供美观的终端日志输出及与 slog 适配的处理器
)

var ( // 声明包级别的全局变量组
	globalLogger *slog.Logger  // 声明全局的 slog.Logger 类型指针，作为应用核心日志实例
	ptermLogger  *pterm.Logger // 声明全局的 pterm.Logger 类型指针，用于配置 pterm 日志行为
)

// Config 日志配置
type Config struct { // 定义日志配置结构体，封装所有日志相关的可配置项
	Level      pterm.LogLevel // 日志级别，使用 pterm 库定义的 LogLevel 枚举类型
	ShowCaller bool           // 是否显示日志调用方信息（包含文件路径、行号等）
	ShowTime   bool           // 是否在日志中显示时间戳
	TimeFormat string         // 时间戳的格式化字符串，遵循 Go 语言的时间格式化规则
	// 文件输出配置（预留）
	FileOutput bool   // 是否启用日志文件输出功能（预留配置项，暂未实现完整逻辑）
	FilePath   string // 日志文件的存储路径（预留配置项，暂未实现完整逻辑）
}

// DefaultConfig 默认配置
func DefaultConfig() *Config { // 定义函数，无入参，返回指向 Config 结构体的指针，用于生成默认日志配置
	return &Config{ // 创建 Config 结构体实例并返回其指针，初始化各字段为默认值
		Level:      pterm.LogLevelTrace,  // 默认日志级别设为 Trace（最低级别，输出所有日志）
		ShowCaller: true,                 // 默认显示日志调用方信息
		ShowTime:   true,                 // 默认显示日志时间戳
		TimeFormat: "01-02 15:04:05.000", // 默认时间格式，包含月-日 时:分:秒.毫秒
		FileOutput: false,                // 默认关闭文件输出功能
		FilePath:   "flk.log",            // 默认日志文件路径为当前目录下的 flk.log 文件
	}
}

// Init 初始化全局 logger
func Init(config *Config) { // 定义初始化函数，入参为 Config 结构体指针，无返回值，用于初始化全局日志实例
	if config == nil { // 检查入参配置是否为空指针
		config = DefaultConfig() // 若配置为空，则使用默认配置初始化
	}

	// 正确的配置方式：分步骤配置 PTerm logger
	ptermLogger = pterm.DefaultLogger. // 获取 pterm 库的默认 Logger 实例作为配置基础
						WithLevel(config.Level).       // 设置日志级别为配置项中指定的 Level 值
						WithCaller(config.ShowCaller). // 设置是否显示调用方信息为配置项中指定的 ShowCaller 值
						WithCallerOffset(4).           // 设置调用方信息的栈偏移量为 4，这将显示正确的源代码行号
						WithTime(config.ShowTime)      // 设置是否显示时间戳为配置项中指定的 ShowTime 值（WithTime 方法接收布尔值）

	// 如果需要自定义时间格式，使用 WithTimeFormat
	if config.TimeFormat != "" { // 检查配置项中的时间格式字符串是否非空
		ptermLogger = ptermLogger.WithTimeFormat(config.TimeFormat) // 为 ptermLogger 设置自定义的时间格式化字符串
	}

	// 创建 slog handler
	handler := pterm.NewSlogHandler(ptermLogger) // 使用 ptermLogger 作为底层，创建适配 slog 库的 Handler 实例

	// TODO: 文件输出实现
	// if config.FileOutput {
	//     file, err := os.OpenFile(config.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	//     if err == nil {
	//         fileHandler := slog.NewJSONHandler(file, &slog.HandlerOptions{
	//             Level: slog.LevelDebug,
	//         })
	//         handler = slog.MultiHandler(handler, fileHandler)
	//     }
	// }

	globalLogger = slog.New(handler) // 使用创建好的 handler 初始化 slog.Logger 实例，并赋值给全局变量
	slog.SetDefault(globalLogger)    // 将全局 slog.Logger 实例设为 Go 标准库 slog 的默认日志实例
}

// 便捷函数包装
func Debug(msg string, args ...any) { // 定义 Debug 级别日志的便捷函数，入参为日志消息字符串和可变键值对参数
	globalLogger.Debug(msg, args...) // 调用全局 slog.Logger 实例的 Debug 方法输出日志
}

func Info(msg string, args ...any) { // 定义 Info 级别日志的便捷函数，入参为日志消息字符串和可变键值对参数
	globalLogger.Info(msg, args...) // 调用全局 slog.Logger 实例的 Info 方法输出日志
}

func Warn(msg string, args ...any) { // 定义 Warn 级别日志的便捷函数，入参为日志消息字符串和可变键值对参数
	globalLogger.Warn(msg, args...) // 调用全局 slog.Logger 实例的 Warn 方法输出日志
}

func Error(msg string, args ...any) { // 定义 Error 级别日志的便捷函数，入参为日志消息字符串和可变键值对参数
	globalLogger.Error(msg, args...) // 调用全局 slog.Logger 实例的 Error 方法输出日志
}

func Fatal(msg string, args ...any) { // 定义 Fatal 级别日志的便捷函数，入参为日志消息字符串和可变键值对参数
	globalLogger.Error(msg, args...) // 调用全局 slog.Logger 实例的 Error 方法输出日志（slog 无 Fatal 方法，复用 Error）
	os.Exit(1)                       // 输出日志后立即退出程序，退出码 1 表示程序异常退出
}

// 设置日志级别
func SetLevel(level pterm.LogLevel) { // 定义动态修改日志级别的函数，入参为 pterm.LogLevel 类型的级别值
	ptermLogger.Level = level // 直接修改全局 ptermLogger 的 Level 字段，动态调整日志级别
}
