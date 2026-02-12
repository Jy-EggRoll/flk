// Package logger 提供日志记录功能
// 主要功能包括：
// 1. 将日志写入文件
// 2. 自动轮转（当文件超过指定大小时）
// 3. 线程安全
// 4. 支持不同日志级别
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// LogLevel 日志级别
type LogLevel int

const (
	// DEBUG 调试信息
	DEBUG LogLevel = iota
	// INFO 一般信息
	INFO
	// WARN 警告信息
	WARN
	// ERROR 错误信息
	ERROR
)

// String 返回日志级别的字符串表示
func (l LogLevel) String() string {
	switch l {
	case DEBUG:
		return "调试"
	case INFO:
		return "信息"
	case WARN:
		return "警告"
	case ERROR:
		return "错误"
	default:
		return "未知"
	}
}

// Logger 日志记录器
type Logger struct {
	logPath string     // 日志文件路径
	maxSize int64      // 最大文件大小（字节）
	level   LogLevel   // 最低日志级别
	mu      sync.Mutex // 互斥锁
	file    *os.File   // 当前日志文件
}

// NewLogger 创建一个新的日志记录器
// logPath: 日志文件路径（如果为空，使用默认路径）
// maxSize: 最大文件大小（字节），0 表示使用默认值 5MB
// level: 最低日志级别
func NewLogger(logPath string, maxSize int64, level LogLevel) (*Logger, error) {
	// 如果未指定路径，使用默认路径
	if logPath == "" {
		var err error
		logPath, err = getDefaultLogPath()
		if err != nil {
			return nil, fmt.Errorf("获取默认日志路径失败：%w", err)
		}
	}

	// 如果未指定大小，使用默认值 5MB
	if maxSize <= 0 {
		maxSize = 5 * 1024 * 1024 // 5MB
	}

	logger := &Logger{
		logPath: logPath,
		maxSize: maxSize,
		level:   level,
	}

	return logger, nil
}

// getDefaultLogPath 获取默认日志路径
// Linux/MacOS: ~/.log/flk/logs/flk.log
// Windows: %APPDATA%\flk\logs\flk.log
func getDefaultLogPath() (string, error) {
	var baseDir string

	if runtime.GOOS == "windows" {
		// Windows: 使用 APPDATA
		baseDir = os.Getenv("APPDATA")
		if baseDir == "" {
			return "", fmt.Errorf("无法获取“APPDATA”环境变量")
		}
	} else {
		// Linux/MacOS: 使用 ~/.log
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法获取用户主目录：%w", err)
		}
		baseDir = filepath.Join(home, ".log")
	}

	// 拼接完整路径
	logDir := filepath.Join(baseDir, "flk", "logs")
	logFile := filepath.Join(logDir, "flk.log")

	return logFile, nil
}

// openLogFile 打开日志文件（如果不存在则创建）
func (l *Logger) openLogFile() error {
	// 确保日志目录存在
	logDir := filepath.Dir(l.logPath)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("创建日志目录失败：%w", err)
	}

	// 以追加模式打开文件
	file, err := os.OpenFile(l.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开日志文件失败：%w", err)
	}

	l.file = file
	return nil
}

// closeLogFile 关闭日志文件
func (l *Logger) closeLogFile() error {
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

// rotate 轮转日志文件
// 将当前日志文件重命名为带时间戳的备份文件，然后创建新的日志文件
func (l *Logger) rotate() error {
	// 关闭当前文件
	if err := l.closeLogFile(); err != nil {
		return err
	}

	// 生成备份文件名（带时间戳）
	timestamp := time.Now().Format("20060102-150405")
	backupPath := l.logPath + "." + timestamp

	// 重命名当前日志文件
	if err := os.Rename(l.logPath, backupPath); err != nil {
		return fmt.Errorf("重命名日志文件失败：%w", err)
	}

	// 创建新的日志文件
	return l.openLogFile()
}

// checkSize 检查文件大小，如果超过限制则轮转
func (l *Logger) checkSize() error {
	if l.file == nil {
		return nil
	}

	info, err := l.file.Stat()
	if err != nil {
		return fmt.Errorf("获取文件信息失败：%w", err)
	}

	if info.Size() >= l.maxSize {
		return l.rotate()
	}

	return nil
}

// log 写入日志（内部方法）
func (l *Logger) log(level LogLevel, format string, args ...interface{}) error {
	// 检查日志级别
	if level < l.level {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// 如果文件未打开，先打开
	if l.file == nil {
		if err := l.openLogFile(); err != nil {
			return err
		}
	}

	// 检查文件大小
	if err := l.checkSize(); err != nil {
		return err
	}

	// 格式化日志消息
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	logLine := fmt.Sprintf("【%s】【%s】%s\n", timestamp, level.String(), message)

	// 写入文件
	if _, err := l.file.WriteString(logLine); err != nil {
		return fmt.Errorf("写入日志失败：%w", err)
	}

	// 立即刷新到磁盘
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("刷新日志失败：%w", err)
	}

	return nil
}

// Debug 写入 DEBUG 级别日志
func (l *Logger) Debug(format string, args ...interface{}) error {
	return l.log(DEBUG, format, args...)
}

// Info 写入 INFO 级别日志
func (l *Logger) Info(format string, args ...interface{}) error {
	return l.log(INFO, format, args...)
}

// Warn 写入 WARN 级别日志
func (l *Logger) Warn(format string, args ...interface{}) error {
	return l.log(WARN, format, args...)
}

// Error 写入 ERROR 级别日志
func (l *Logger) Error(format string, args ...interface{}) error {
	return l.log(ERROR, format, args...)
}

// Close 关闭日志记录器
// 应该在程序退出前调用
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closeLogFile()
}

// GetLogPath 返回日志文件路径
func (l *Logger) GetLogPath() string {
	return l.logPath
}
