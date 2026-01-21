package logger // 声明当前代码所属的包名为 logger
import (       // 导入代码依赖的外部包列表
	"os" // 导入 os 包，用于与操作系统交互，核心功能是读取环境变量

	"github.com/pterm/pterm" // 导入 pterm 第三方日志库，用于定义日志级别常量和日志相关操作
)

// 从字符串解析日志级别
func LogLevelFromString(levelStr string) pterm.LogLevel { // 定义函数，入参为字符串类型的日志级别标识，返回 pterm 库定义的 LogLevel 类型
	switch levelStr { // 基于输入的日志级别字符串进行分支匹配
	case "trace": // 匹配到字符串 "trace" 时
		return pterm.LogLevelTrace // 返回 pterm 库中 Trace 级别的日志级别常量
	case "debug": // 匹配到字符串 "debug" 时
		return pterm.LogLevelDebug // 返回 pterm 库中 Debug 级别的日志级别常量
	case "warn": // 匹配到字符串 "warn" 时
		return pterm.LogLevelWarn // 返回 pterm 库中 Warn 级别的日志级别常量
	case "error": // 匹配到字符串 "error" 时
		return pterm.LogLevelError // 返回 pterm 库中 Error 级别的日志级别常量
	default: // 未匹配到上述任何字符串时执行默认分支
		return pterm.LogLevelInfo // 返回 pterm 库中 Info 级别的日志级别常量（作为默认值），匹配到 info 时也返回 info 级别
	}
}

// 从环境变量加载配置
func FromEnv() *Config { // 定义函数，无入参，返回指向 Config 结构体的指针（Config 结构体需在代码其他位置定义）
	config := DefaultConfig() // 调用 DefaultConfig 函数获取默认配置实例，并赋值给 config 变量

	// 从环境变量读取日志级别
	if levelStr := os.Getenv("FLK_LOG_LEVEL"); levelStr != "" { // 读取环境变量 FLK_LOG_LEVEL 的值并赋值给 levelStr，仅当值非空时执行后续逻辑
		config.Level = LogLevelFromString(levelStr) // 将字符串日志级别解析为 pterm.LogLevel 类型，并赋值给配置实例的 Level 字段
	}

	// 从环境变量读取文件输出配置
	if fileOutput := os.Getenv("FLK_LOG_FILE_OUTPUT"); fileOutput == "true" { // 读取环境变量 FLK_LOG_FILE_OUTPUT 的值，仅当值为 "true" 时执行后续逻辑
		config.FileOutput = true // 将配置实例的 FileOutput 字段设为 true，表示启用日志文件输出功能
	}

	if filePath := os.Getenv("FLK_LOG_FILE_PATH"); filePath != "" { // 读取环境变量 FLK_LOG_FILE_PATH 的值并赋值给 filePath，仅当值非空时执行后续逻辑
		config.FilePath = filePath // 将读取到的文件路径赋值给配置实例的 FilePath 字段，指定日志文件的存储路径
	}

	return config // 返回加载了环境变量配置的 Config 结构体指针，这实现了环境变量比约定配置有更高的优先级
}
