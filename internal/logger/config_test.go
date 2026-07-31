package logger

import (
	"bytes"
	"log/slog"
	"os"
	"testing"
)

// TestDefaultConfig 验证无显式配置时的稳定基线，尤其确保日志默认进入 stderr 而不会污染 stdout 的业务输出
func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()
	if config.Level != slog.LevelWarn {
		t.Fatalf("默认级别 = %v，期望 %v", config.Level, slog.LevelWarn)
	}
	if config.Writer != os.Stderr {
		t.Fatalf("默认 Writer = %T，期望当前 os.Stderr", config.Writer)
	}
	if config.ShowTime {
		t.Fatal("默认 Warn 层不应显示时间")
	}
	if config.ShowSource {
		t.Fatal("默认 Warn 层不应显示调用方")
	}
}

// TestLogLevelFromString 分别覆盖大小写、首尾空白和全部受支持级别，防止环境变量解析与 slog 级别值发生偏差
func TestLogLevelFromString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  slog.Level
	}{
		{name: "debug 小写", input: "debug", want: slog.LevelDebug},
		{name: "info 大写和空白", input: "  INFO\t", want: slog.LevelInfo},
		{name: "warn 混合大小写", input: "WaRn", want: slog.LevelWarn},
		{name: "error 换行", input: "\nERROR\r", want: slog.LevelError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := LogLevelFromString(test.input)
			if err != nil {
				t.Fatalf("LogLevelFromString(%q) 返回错误: %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("LogLevelFromString(%q) = %v，期望 %v", test.input, got, test.want)
			}
		})
	}

	for _, invalid := range []string{"trace", "verbose", "   "} {
		t.Run("拒绝非法值_"+invalid, func(t *testing.T) {
			if _, err := LogLevelFromString(invalid); err == nil {
				t.Fatalf("LogLevelFromString(%q) 未返回错误", invalid)
			}
		})
	}
}

// TestFromEnv 验证环境配置的默认行为、规范化解析和 Info/Debug 展示层次，确保命令层可直接初始化返回的配置
func TestFromEnv(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantLevel  slog.Level
		wantTime   bool
		wantSource bool
	}{
		{name: "空值沿用 Warn", value: "", wantLevel: slog.LevelWarn},
		{name: "Warn 不增加上下文", value: " WARN ", wantLevel: slog.LevelWarn},
		{name: "Info 增加时间", value: " info ", wantLevel: slog.LevelInfo, wantTime: true},
		{name: "Debug 增加时间和调用方", value: "DeBuG", wantLevel: slog.LevelDebug, wantTime: true, wantSource: true},
		{name: "Error 保持精简", value: "error", wantLevel: slog.LevelError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(logLevelEnv, test.value)
			config, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv() 返回错误: %v", err)
			}
			if config.Level != test.wantLevel || config.ShowTime != test.wantTime || config.ShowSource != test.wantSource {
				t.Fatalf("FromEnv() = {Level:%v ShowTime:%t ShowSource:%t}，期望 {%v %t %t}", config.Level, config.ShowTime, config.ShowSource, test.wantLevel, test.wantTime, test.wantSource)
			}
			if config.Writer != os.Stderr {
				t.Fatalf("FromEnv() Writer = %T，期望 os.Stderr", config.Writer)
			}
		})
	}
}

// TestFromEnvRejectsInvalidLevel 确保错误环境值不会静默降级，同时保留变量名以便命令层给出可定位的问题信息
func TestFromEnvRejectsInvalidLevel(t *testing.T) {
	t.Setenv(logLevelEnv, "notice")
	config, err := FromEnv()
	if err == nil {
		t.Fatal("FromEnv() 对非法日志级别未返回错误")
	}
	if config != nil {
		t.Fatalf("FromEnv() 出错时返回了非 nil 配置: %#v", config)
	}
}

// TestApplyVerbose 验证 verbose 累计次数对级别和展示字段的完整覆盖，并确认输入配置不会被原地修改
func TestApplyVerbose(t *testing.T) {
	writer := &bytes.Buffer{}
	base := &Config{
		Level:      slog.LevelError,
		Writer:     writer,
		ShowTime:   true,
		ShowSource: true,
	}
	tests := []struct {
		name       string
		count      int
		wantLevel  slog.Level
		wantTime   bool
		wantSource bool
	}{
		{name: "负数按默认层处理", count: -1, wantLevel: slog.LevelWarn},
		{name: "零次为 Warn", count: 0, wantLevel: slog.LevelWarn},
		{name: "一次为 Info", count: 1, wantLevel: slog.LevelInfo, wantTime: true},
		{name: "两次为 Debug", count: 2, wantLevel: slog.LevelDebug, wantTime: true, wantSource: true},
		{name: "更多次数仍为 Debug", count: 5, wantLevel: slog.LevelDebug, wantTime: true, wantSource: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ApplyVerbose(base, test.count)
			if got.Level != test.wantLevel || got.ShowTime != test.wantTime || got.ShowSource != test.wantSource {
				t.Fatalf("ApplyVerbose(_, %d) = {Level:%v ShowTime:%t ShowSource:%t}，期望 {%v %t %t}", test.count, got.Level, got.ShowTime, got.ShowSource, test.wantLevel, test.wantTime, test.wantSource)
			}
			if got.Writer != writer {
				t.Fatal("ApplyVerbose 丢失了调用方注入的 Writer")
			}
		})
	}

	if base.Level != slog.LevelError || !base.ShowTime || !base.ShowSource {
		t.Fatalf("ApplyVerbose 修改了输入配置: %#v", base)
	}
}
