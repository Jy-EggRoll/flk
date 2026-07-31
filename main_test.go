package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	flkcmd "github.com/jy-eggroll/flk/cmd"
)

const cliHelperEnv = "FLK_CLI_HELPER_PROCESS"

// TestCLIHelperProcess 在独立测试进程中执行真实 Cobra 命令树
// 每个契约用例都使用全新的进程，避免 Cobra flag、全局 store、logger 和 pterm writer 在多次 Execute 之间相互污染
func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv(cliHelperEnv) != "1" {
		return
	}

	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		os.Exit(2)
	}

	os.Args = append([]string{"flk"}, os.Args[separator+1:]...)
	flkcmd.SetWindowsAdminChecker(nil)
	os.Exit(flkcmd.Execute())
}

// cliResult 保存一次真实 CLI 子进程的三个可观察契约：stdout、stderr 和退出码
type cliResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// runCLI 在隔离的 HOME 和明确的日志环境中执行命令，返回完整输出而不让预期的非零退出直接终止测试
func runCLI(t *testing.T, arguments ...string) cliResult {
	t.Helper()

	helperArguments := append([]string{"-test.run=^TestCLIHelperProcess$", "--"}, arguments...)
	command := exec.Command(os.Args[0], helperArguments...)
	command.Env = append(os.Environ(),
		cliHelperEnv+"=1",
		"FLK_LOG_LEVEL=",
		"HOME="+t.TempDir(),
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	exitCode := 0
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("执行 CLI 子进程失败: %v", err)
		}
		exitCode = exitError.ExitCode()
	}

	return cliResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

// TestCLIJSONOutputContract 验证普通和详细日志模式都不会污染机器可读 stdout
func TestCLIJSONOutputContract(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")

	for _, testCase := range []struct {
		name      string
		arguments []string
		wantLog   bool
	}{
		{name: "默认级别", arguments: []string{"check", "--output", "json", "--store-path", storePath}},
		{name: "Info 级别", arguments: []string{"-v", "check", "--output", "json", "--store-path", storePath}, wantLog: true},
		{name: "Debug 级别", arguments: []string{"-vv", "check", "--output", "json", "--store-path", storePath}, wantLog: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := runCLI(t, testCase.arguments...)
			if result.exitCode != 0 {
				t.Fatalf("退出码 = %d，stderr = %q", result.exitCode, result.stderr)
			}
			if !json.Valid([]byte(result.stdout)) {
				t.Fatalf("stdout 不是合法 JSON: %q", result.stdout)
			}

			var records []map[string]any
			if err := json.Unmarshal([]byte(result.stdout), &records); err != nil {
				t.Fatalf("解析检查结果失败: %v", err)
			}
			if len(records) != 0 {
				t.Fatalf("空 store 应返回 []，实际为 %#v", records)
			}
			if testCase.wantLog != strings.Contains(result.stderr, "检查完成") {
				t.Fatalf("stderr 日志状态不符，wantLog=%v，stderr=%q", testCase.wantLog, result.stderr)
			}
		})
	}
}

// TestCLIRenderedErrorContract 验证结构化失败只写一次 JSON，并通过非零退出码表达失败
func TestCLIRenderedErrorContract(t *testing.T) {
	tempDir := t.TempDir()
	result := runCLI(t,
		"create", "copy",
		"--src", filepath.Join(tempDir, "missing-source"),
		"--dst", filepath.Join(tempDir, "target"),
		"--store-path", filepath.Join(tempDir, "store.json"),
		"--output", "json",
		"--smart",
		"--force",
	)

	if result.exitCode != 1 {
		t.Fatalf("业务失败退出码 = %d，stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	if !json.Valid([]byte(result.stdout)) {
		t.Fatalf("失败 stdout 不是合法 JSON: %q", result.stdout)
	}
	var createResult struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &createResult); err != nil {
		t.Fatalf("解析创建失败结果失败: %v", err)
	}
	if createResult.Success || createResult.Error == "" {
		t.Fatalf("创建失败结果不完整: %#v", createResult)
	}
	if result.stderr != "" {
		t.Fatalf("已渲染错误不应由根层重复输出，stderr=%q", result.stderr)
	}
}

// TestCLIStoreInitializationFailure 验证损坏 store 会在任何业务输出前失败，且 Cobra 不再附加整段 Usage
func TestCLIStoreInitializationFailure(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(storePath, []byte("{broken"), 0o600); err != nil {
		t.Fatalf("写入损坏 store 失败: %v", err)
	}

	result := runCLI(t, "check", "--output", "json", "--store-path", storePath)
	if result.exitCode != 1 {
		t.Fatalf("损坏 store 退出码 = %d", result.exitCode)
	}
	if result.stdout != "" {
		t.Fatalf("初始化失败前不应输出业务结果，stdout=%q", result.stdout)
	}
	if strings.Count(result.stderr, "初始化存储失败") != 1 {
		t.Fatalf("初始化错误应恰好输出一次，stderr=%q", result.stderr)
	}
	if strings.Contains(result.stderr, "Usage:") {
		t.Fatalf("运行时错误不应附带 Usage，stderr=%q", result.stderr)
	}
}

// TestCLIAuxiliaryCommands 验证帮助、补全和版本输出不会被欢迎语或存储初始化副作用污染
func TestCLIAuxiliaryCommands(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		contains  string
	}{
		{name: "help", arguments: []string{"--help"}, contains: "Usage:"},
		{name: "completion", arguments: []string{"completion", "bash"}, contains: "bash completion"},
		{name: "version flag", arguments: []string{"--version"}, contains: "版本:"},
		{name: "version command", arguments: []string{"version"}, contains: "构建时间:"},
		{name: "verbose and version", arguments: []string{"-vv", "--version"}, contains: "平台:"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result := runCLI(t, testCase.arguments...)
			if result.exitCode != 0 {
				t.Fatalf("退出码 = %d，stderr=%q", result.exitCode, result.stderr)
			}
			if !strings.Contains(result.stdout, testCase.contains) {
				t.Fatalf("stdout 缺少 %q: %q", testCase.contains, result.stdout)
			}
			if strings.Contains(result.stdout, "欢迎使用 flk") || strings.Contains(result.stderr, "欢迎使用 flk") {
				t.Fatalf("辅助命令不应输出欢迎语，stdout=%q stderr=%q", result.stdout, result.stderr)
			}
		})
	}
}

// TestCLIRejectsUnsupportedJSON 验证没有结构化契约的命令会在执行前拒绝 JSON，而不是向 stdout 输出普通文本
func TestCLIRejectsUnsupportedJSON(t *testing.T) {
	result := runCLI(t, "version", "--output", "json")
	if result.exitCode != 1 {
		t.Fatalf("退出码 = %d", result.exitCode)
	}
	if result.stdout != "" {
		t.Fatalf("不支持 JSON 时 stdout 必须为空: %q", result.stdout)
	}
	if strings.Count(result.stderr, "不支持 JSON 输出") != 1 {
		t.Fatalf("错误应恰好输出一次: %q", result.stderr)
	}
}
