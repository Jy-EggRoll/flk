package logger

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestLevelFiltering 验证标准 slog handler 的级别过滤边界，确保低于阈值的记录不会写入注入的 Writer
func TestLevelFiltering(t *testing.T) {
	var output bytes.Buffer
	Init(&Config{Level: slog.LevelWarn, Writer: &output})

	Debug("被过滤的 debug")
	Info("被过滤的 info")
	Warn("保留的 warn")
	Error("保留的 error")

	text := output.String()
	if strings.Contains(text, "被过滤") {
		t.Fatalf("低于 Warn 的日志进入输出: %s", text)
	}
	if !strings.Contains(text, "level=WARN") || !strings.Contains(text, "保留的 warn") {
		t.Fatalf("Warn 日志缺失: %s", text)
	}
	if !strings.Contains(text, "level=ERROR") || !strings.Contains(text, "保留的 error") {
		t.Fatalf("Error 日志缺失: %s", text)
	}
}

// TestInjectedWriterAndStructuredArguments 同时验证 Writer 注入、键值对和 slog.Attr，防止包装函数破坏 slog 的结构化参数语义
func TestInjectedWriterAndStructuredArguments(t *testing.T) {
	var output bytes.Buffer
	Init(&Config{Level: slog.LevelInfo, Writer: &output})

	Info("复制完成", "src", "a.txt", slog.String("dst", "b.txt"), "count", 2)

	text := output.String()
	for _, fragment := range []string{"msg=复制完成", "src=a.txt", "dst=b.txt", "count=2"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("结构化输出缺少 %q: %s", fragment, text)
		}
	}
}

// TestVerboseOutputLayers 验证三层 verbose 配置最终呈现：Warn 精简、Info 带时间、Debug 再增加源码位置
func TestVerboseOutputLayers(t *testing.T) {
	tests := []struct {
		name       string
		count      int
		write      func(string, ...any)
		wantTime   bool
		wantSource bool
	}{
		{name: "Warn 精简层", count: 0, write: Warn},
		{name: "Info 时间层", count: 1, write: Info, wantTime: true},
		{name: "Debug 调用方层", count: 2, write: Debug, wantTime: true, wantSource: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			config := ApplyVerbose(&Config{Writer: &output}, test.count)
			Init(config)
			test.write("分层日志")

			text := output.String()
			if strings.Contains(text, "time=") != test.wantTime {
				t.Fatalf("time 字段状态错误，输出: %s", text)
			}
			if strings.Contains(text, "source=") != test.wantSource {
				t.Fatalf("source 字段状态错误，输出: %s", text)
			}
		})
	}
}

// writeDebugFromBusiness 模拟 logger 包之外的业务包装点，并返回 Debug 调用所在的精确源码位置供断言使用
func writeDebugFromBusiness() (string, int) {
	_, file, line, _ := runtime.Caller(0)
	Debug("检查业务调用方")
	return file, line + 1
}

// TestDebugReportsBusinessCaller 验证源码字段跳过 logger.Debug 和内部 log 包装层，精确指向实际业务调用代码
func TestDebugReportsBusinessCaller(t *testing.T) {
	var output bytes.Buffer
	Init(&Config{
		Level:      slog.LevelDebug,
		Writer:     &output,
		ShowSource: true,
	})

	file, line := writeDebugFromBusiness()
	want := fmt.Sprintf("source=%s:%d", file, line)
	if text := output.String(); !strings.Contains(text, want) {
		t.Fatalf("调用方错误，期望包含 %q，实际输出: %s", want, text)
	}
}

// TestCallsRemainSafeWithoutPublishedInstance 模拟包级实例意外为空的初始化前状态，确保所有便捷调用至少能安全获取回退实例
func TestCallsRemainSafeWithoutPublishedInstance(t *testing.T) {
	previous := globalLogger.Load()
	globalLogger.Store(nil)
	t.Cleanup(func() {
		globalLogger.Store(previous)
	})

	Debug("初始化前的 debug")
	if globalLogger.Load() == nil {
		t.Fatal("便捷调用后未发布安全的默认日志实例")
	}
}

// TestInitDoesNotReplaceSlogDefault 防止 logger.Init 影响进程内其他库使用的 slog 默认实例
func TestInitDoesNotReplaceSlogDefault(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})

	var defaultOutput bytes.Buffer
	customDefault := slog.New(slog.NewTextHandler(&defaultOutput, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(customDefault)

	var packageOutput bytes.Buffer
	Init(&Config{Level: slog.LevelInfo, Writer: &packageOutput})
	slog.Info("标准库默认日志")
	Info("包内日志")

	if !strings.Contains(defaultOutput.String(), "标准库默认日志") {
		t.Fatalf("slog 默认实例被替换: %s", defaultOutput.String())
	}
	if strings.Contains(defaultOutput.String(), "包内日志") {
		t.Fatalf("包内日志错误地写入 slog 默认实例: %s", defaultOutput.String())
	}
	if !strings.Contains(packageOutput.String(), "包内日志") {
		t.Fatalf("包内日志未写入注入 Writer: %s", packageOutput.String())
	}
}

// TestNilWriterFallsBackToStderr 验证不完整配置不仅不会 panic，而且确实把可见日志写入标准错误流
func TestNilWriterFallsBackToStderr(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建 stderr 捕获管道失败: %v", err)
	}
	previousStderr := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() {
		os.Stderr = previousStderr
		_ = reader.Close()
		_ = writer.Close()
	})

	Init(&Config{Level: slog.LevelError})
	Error("nil Writer 回退日志")
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 stderr 写端失败: %v", err)
	}
	captured, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("读取 stderr 失败: %v", err)
	}
	if !strings.Contains(string(captured), "nil Writer 回退日志") {
		t.Fatalf("stderr 未收到日志: %s", captured)
	}
}
