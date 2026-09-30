package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

	return runCLIWithEnv(t, t.TempDir(), arguments...)
}

// runCLIWithEnv 与 runCLI 一致，但由调用方指定子进程的 HOME。
//
// 需要它的场景：回收站等副作用按 HOME 解析，而回收站用 os.Rename 移动文件，
// 因此「待移动的文件」与「回收站」必须落在同一个 HOME 下；调用方需要自行控制该目录
func runCLIWithEnv(t *testing.T, childHome string, arguments ...string) cliResult {
	t.Helper()

	helperArguments := append([]string{"-test.run=^TestCLIHelperProcess$", "--"}, arguments...)
	command := exec.Command(os.Args[0], helperArguments...)
	command.Env = append(os.Environ(),
		cliHelperEnv+"=1",
		"FLK_LOG_LEVEL=",
		"HOME="+childHome,
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
			if testCase.wantLog != strings.Contains(result.stderr, "Check complete") {
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
	if strings.Count(result.stderr, "Failed to initialize the store") != 1 {
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
		{name: "version flag", arguments: []string{"--version"}, contains: "Version:"},
		{name: "version command", arguments: []string{"version"}, contains: "Build time:"},
		{name: "verbose and version", arguments: []string{"-vv", "--version"}, contains: "Platform:"},
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
			if strings.Contains(result.stdout, "Welcome to flk") || strings.Contains(result.stderr, "Welcome to flk") {
				t.Fatalf("辅助命令不应输出欢迎语，stdout=%q stderr=%q", result.stdout, result.stderr)
			}
		})
	}
}

// TestCLIHelpLanguageFlagOrder 从真实子进程验证「--help/-h 无论出现在 --lang/-l 之前还是之后，语言都必须生效」
//
// 回归背景：pflag 对未定义的 --help/-h 会立即中断解析并返回 ErrHelp，
// 导致 `flk serve --help --lang zh-CN` 里的 --lang 被丢弃、帮助回退英文，
// 也就是「--help 写在 --lang 之前」才失效、若把 --lang 挪到前面即可绕过；修复见 cmd/lang.go 的占位 flag
//
// 为什么用「不同顺序之间互相比对」而不是断言某句中文译文：
// 译文由 internal/locales 维护、会随翻译迭代改动，把用例钉在具体措辞上会随翻译一起变红；
// 这里真正要守住的是「同一语言下帮助参数的位置不改变输出」，以及与英文基线必须不同，
// 两者都不依赖具体措辞，因此翻译怎么改都不会误报
//
// 同时它也守住了「语言确实生效」这一点：若 --lang 被丢弃而回退默认英文，
// zh-CN 的输出会与 --lang en 完全相同，用例立即失败
//
// 根命令的场景同样必须覆盖，而它由另一处修复兜底：cobra 的 Find/stripFlags 在默认 help flag
// 尚未注册时会把 --help/-h 误判成「需要取值的 flag」并吞掉下一个参数，随后根命令专属的
// legacyArgs 直接报 unknown command；cmd/root.go 在执行前提前注册该 flag 即为修此问题
// 两类缺陷的表现都是「帮助参数的位置改变结果」，因此放在同一个用例里逐条比对
func TestCLIHelpLanguageFlagOrder(t *testing.T) {
	tests := []struct {
		name      string
		reference []string
		reordered []string
	}{
		{
			name:      "serve 长参数",
			reference: []string{"serve", "--lang", "zh-CN", "--help"},
			reordered: []string{"serve", "--help", "--lang", "zh-CN"},
		},
		{
			name:      "serve 短参数",
			reference: []string{"serve", "-l", "zh-CN", "-h"},
			reordered: []string{"serve", "-h", "-l", "zh-CN"},
		},
		{
			name:      "check 叶子命令",
			reference: []string{"check", "--lang", "zh-CN", "--help"},
			reordered: []string{"check", "--help", "--lang", "zh-CN"},
		},
		{
			name:      "根命令长参数",
			reference: []string{"--lang", "zh-CN", "--help"},
			reordered: []string{"--help", "--lang", "zh-CN"},
		},
		{
			name:      "根命令短参数",
			reference: []string{"-l", "zh-CN", "-h"},
			reordered: []string{"-h", "-l", "zh-CN"},
		},
		{
			name:      "根命令等号写法",
			reference: []string{"--lang=zh-CN", "--help"},
			reordered: []string{"--help", "--lang=zh-CN"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			reference := runCLI(t, testCase.reference...)
			reordered := runCLI(t, testCase.reordered...)

			if reference.exitCode != 0 || reordered.exitCode != 0 {
				t.Fatalf("帮助应正常退出，reference=%d reordered=%d，stderr=%q %q",
					reference.exitCode, reordered.exitCode, reference.stderr, reordered.stderr)
			}
			if !strings.Contains(reference.stdout, "Usage:") || !strings.Contains(reordered.stdout, "Usage:") {
				t.Fatalf("帮助输出缺少 Usage，reference=%q reordered=%q", reference.stdout, reordered.stdout)
			}
			if reordered.stdout != reference.stdout {
				t.Fatalf("--help 位置改变了帮助内容，说明语言取值依赖参数顺序\nreference=%q\nreordered=%q",
					reference.stdout, reordered.stdout)
			}
		})
	}

	// 语言有效性基线：显式 --lang en 与显式 --lang zh-CN 必须产出不同内容，
	// 否则上面的「两种顺序一致」可能只是双双回退英文而假通过
	english := runCLI(t, "serve", "--help", "--lang", "en")
	chinese := runCLI(t, "serve", "--help", "--lang", "zh-CN")
	if english.stdout == chinese.stdout {
		t.Fatalf("--lang en 与 --lang zh-CN 输出相同，语言未真正生效: %q", chinese.stdout)
	}

	// 根命令不带 --lang 时必须照旧正常出帮助：退出码为 0、有 Usage、stderr 干净
	plainHelp := runCLI(t, "--help")
	if plainHelp.exitCode != 0 || plainHelp.stderr != "" || !strings.Contains(plainHelp.stdout, "Usage:") {
		t.Fatalf("不带 --lang 的根帮助行为被改变: exit=%d stderr=%q stdout=%q",
			plainHelp.exitCode, plainHelp.stderr, plainHelp.stdout)
	}

	// --help 后面跟着子命令名时，cobra 必须把它当子命令，而不是把子命令名当成 --help 的取值吞掉：
	// 修改前这里打印的是根帮助，修改后才与 `flk help serve` 完全一致
	flagHelp := runCLI(t, "--help", "serve", "--lang", "en")
	commandHelp := runCLI(t, "help", "serve", "--lang", "en")
	if flagHelp.exitCode != 0 || flagHelp.stdout != commandHelp.stdout {
		t.Fatalf("`flk --help serve` 应与 `flk help serve` 等价: exit=%d\nflag=%q\ncommand=%q",
			flagHelp.exitCode, flagHelp.stdout, commandHelp.stdout)
	}
}

// helpFlagLine 从一行帮助里剥出「flag 名 → 说明」
//
// 帮助行的结构固定为「缩进 + 名称(可带类型) + 两个以上空格 + 说明」，
// 例如 "  -l, --lang string        输出语言（如 en、zh-CN）；默认取 language 设置"，
// 因此按「两个以上空格」切分即可稳定取到说明，不必理解各列的对齐规则
// 正则要求出现 -- 前缀，因此 Usage/Aliases/Available Commands 等段落不会误命中
var helpFlagLine = regexp.MustCompile(`^\s+(?:-\w,\s+)?--([\w.-]+)(?:\s+\w+)?\s{2,}(.+)$`)

// parseHelpFlagUsages 把一份帮助输出解析成「flag 名 → 说明」，Local Flags 与 Global Flags 一并纳入
func parseHelpFlagUsages(output string) map[string]string {
	usages := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		match := helpFlagLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		usages["--"+match[1]] = strings.TrimSpace(match[2])
	}
	return usages
}

// withLanguage 复制一份参数并把 --lang 追加在末尾，避免调用方切片被 append 意外改写
func withLanguage(arguments []string, lang string) []string {
	return append(append([]string{}, arguments...), "--lang", lang)
}

// TestCLIHelpTranslatesPersistentFlags 守住「用 PersistentFlags 声明的 flag 说明也必须翻译」
//
// 回归背景：localizeTree 原先只遍历 cmd.Flags()，而语言在 cobra 解析之前就已确定，
// 此刻持久化 flag 还没被合并进 Flags()；于是 flk 的全部全局 flag（--lang/--output/--yes…）
// 与 serve 的 --host/-p 在 zh-CN 下恒为英文，帮助里出现整段中英混排的 Global Flags
//
// 断言用差分口径：把中英文帮助各解析成「flag 名 → 说明」，逐个要求 zh-CN 的说明与英文不同
// 既不依赖任何具体译文措辞，又能同时覆盖 Local Flags 与 Global Flags 两个段落
// 唯一豁免是 cobra 自动生成的 --help（"help for <命令>"）：该串不在语言文件里，
// 且在本地化之后才由 cobra 创建，属于已知且有意保留的英文条目
func TestCLIHelpTranslatesPersistentFlags(t *testing.T) {
	tests := []struct {
		name            string
		arguments       []string
		wantGlobalFlags bool
	}{
		{name: "根命令", arguments: []string{"--help"}},
		{name: "check 叶子命令", arguments: []string{"check", "--help"}, wantGlobalFlags: true},
		// 改造前这里用的是 serve config 子命令；该子命令已并入 serve，
		// 而 serve 的 flag 也从 PersistentFlags 改成普通 Flags，恰好让本条用例同时覆盖两种声明方式
		{name: "serve 命令", arguments: []string{"serve", "--help"}, wantGlobalFlags: true},
		{name: "create copy 子命令", arguments: []string{"create", "copy", "--help"}, wantGlobalFlags: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			englishOutput := runCLI(t, withLanguage(testCase.arguments, "en")...).stdout
			chineseOutput := runCLI(t, withLanguage(testCase.arguments, "zh-CN")...).stdout

			// 段落存在性自检：子命令的帮助必须有 Global Flags 段，
			// 否则解析器可能整段漏读，下面「逐 flag 比对」就会在空集上假通过
			if testCase.wantGlobalFlags && !strings.Contains(chineseOutput, "Global Flags:") {
				t.Fatalf("帮助缺少 Global Flags 段，持久化 flag 未被纳入解析: %q", chineseOutput)
			}

			english := parseHelpFlagUsages(englishOutput)
			chinese := parseHelpFlagUsages(chineseOutput)
			if len(english) == 0 || len(chinese) == 0 {
				t.Fatalf("帮助里没有解析到任何 flag: en=%d zh=%d", len(english), len(chinese))
			}
			if len(english) != len(chinese) {
				t.Fatalf("中英文帮助解析出的 flag 数量不一致: en=%d zh=%d", len(english), len(chinese))
			}

			for name, chineseUsage := range chinese {
				if name == "--help" {
					continue
				}
				englishUsage, ok := english[name]
				if !ok {
					t.Fatalf("flag %s 只出现在 zh-CN 帮助里，en 帮助缺少它", name)
				}
				if chineseUsage == englishUsage {
					t.Fatalf("flag %s 的说明在 zh-CN 下仍是英文源串: %q", name, chineseUsage)
				}
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
	if strings.Count(result.stderr, "does not support JSON output") != 1 {
		t.Fatalf("错误应恰好输出一次: %q", result.stderr)
	}
}

// TestCLICreateNonInteractiveConfirmation 验证 create 在非终端下的两种契约：
//   - 未加 --yes：备份确认无法进行时必须快速失败并给出提示，而不是挂起等待 /dev/tty
//   - 加了 --yes：自动同意备份并完成建链，整个过程无需任何输入
//
// runCLI 的子进程没有控制终端（stdin 被重定向），正好复现脚本/CI 环境；
// 若实现回退到直接调用 pterm 交互，该用例会永久阻塞并最终超时失败，从而守住回归
func TestCLICreateNonInteractiveConfirmation(t *testing.T) {
	// Windows 创建符号链接默认需要管理员权限或开发者模式，跳过以保持用例稳定；
	// 非交互确认逻辑本身已由本用例的失败分支与 prompt 包单测覆盖
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建符号链接需要额外权限，跳过端到端建链断言")
	}

	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.txt")
	fakePath := filepath.Join(dir, "fake.txt")
	storePath := filepath.Join(dir, "store.json")

	if err := os.WriteFile(realPath, []byte("real-content"), 0o644); err != nil {
		t.Fatalf("写入 real 文件失败: %v", err)
	}
	if err := os.WriteFile(fakePath, []byte("fake-content"), 0o644); err != nil {
		t.Fatalf("写入 fake 文件失败: %v", err)
	}

	// 非交互且未 --yes：real 与 fake 同时存在会触发备份确认，应当明确失败
	failure := runCLI(t, "create", "symlink",
		"--real", realPath, "--fake", fakePath, "--store-path", storePath)
	if failure.exitCode != 1 {
		t.Fatalf("未启用 --yes 时应失败，退出码 = %d，stdout=%q stderr=%q", failure.exitCode, failure.stdout, failure.stderr)
	}
	// create 的失败结果由命令层渲染到 stdout（标记为已渲染后根层不再重复），
	// 因此断言合并两个流，只关心提示语确实出现且命令快速失败
	combined := failure.stdout + failure.stderr
	if !strings.Contains(combined, "Cannot interact with the user") {
		t.Fatalf("应提示无法交互，stdout=%q stderr=%q", failure.stdout, failure.stderr)
	}
	if _, err := os.Lstat(fakePath); err != nil {
		t.Fatalf("失败路径不应改动 fake 文件: %v", err)
	}
	if info, err := os.Lstat(fakePath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("失败路径不应把 fake 变成符号链接")
	}

	// 启用 --yes：自动备份并建链，fake 最终应是指向 real 的符号链接
	success := runCLI(t, "create", "symlink",
		"--real", realPath, "--fake", fakePath, "--store-path", storePath, "--yes")
	if success.exitCode != 0 {
		t.Fatalf("--yes 应成功，退出码 = %d，stdout=%q stderr=%q", success.exitCode, success.stdout, success.stderr)
	}
	target, err := os.Readlink(fakePath)
	if err != nil {
		t.Fatalf("fake 应为符号链接: %v", err)
	}
	if target != realPath {
		t.Fatalf("符号链接目标 = %q，期望 %q", target, realPath)
	}
	backedUp, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("读取 real 文件失败: %v", err)
	}
	if string(backedUp) != "fake-content" {
		t.Fatalf("备份后 real 内容 = %q，期望 %q", backedUp, "fake-content")
	}
}

// TestCLICreateRejectsInvalidDevice 验证 create 系列命令在 --device 含保留字符时快速失败
//
// 为什么必须守这条契约：设备名会作为 store 的 key 参与设备过滤，逗号与空格是记录分隔语义中的保留字符
// 一旦非法设备名被接受，它会先写进 store 再在后续过滤中表现出难以定位的错乱，因此在任何日志与
// 文件系统操作之前就要拒绝
//
// 断言全部基于真实子进程输出：退出码非零、默认英文文案恰好出现一次（失败结果由命令层渲染并标记，
// 根层不得重复打印）、且非法输入不产生任何文件系统副作用
func TestCLICreateRejectsInvalidDevice(t *testing.T) {
	// 无 --lang 与语言环境变量时 l10n 回退到默认英文文案，与其它 CLI 契约用例的假设一致
	const wantMessage = "Device name must not contain commas or spaces"

	for _, testCase := range []struct {
		name       string
		deviceName string
	}{
		{name: "含逗号", deviceName: "a,b"},
		{name: "含空格", deviceName: "a b"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			fakePath := filepath.Join(dir, "fake.txt")
			result := runCLI(t, "create", "symlink",
				"--real", filepath.Join(dir, "real.txt"),
				"--fake", fakePath,
				"--device", testCase.deviceName,
				"--store-path", filepath.Join(dir, "store.json"),
			)

			if result.exitCode == 0 {
				t.Fatalf("非法设备名 %q 应导致非零退出，stdout=%q stderr=%q", testCase.deviceName, result.stdout, result.stderr)
			}
			// create 的失败结果写入 stdout 并被标记为已渲染，根层不再重复输出，因此合并两个流后断言次数
			combined := result.stdout + result.stderr
			if count := strings.Count(combined, wantMessage); count != 1 {
				t.Fatalf("失败文案应恰好出现一次，实际 %d 次，stdout=%q stderr=%q", count, result.stdout, result.stderr)
			}
			if _, err := os.Lstat(fakePath); err == nil {
				t.Fatal("设备名校验失败时不应创建 fake，说明校验发生在文件系统操作之后")
			}
		})
	}
}

// TestCLIBatchCommandsAreNonInteractive 验证 fix/unlink 的批量模式（--all）在无终端环境下
// 不再逐项弹确认，可完整跑完；这是 --yes 之外对“非交互能力”的重要补齐
func TestCLIBatchCommandsAreNonInteractive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建符号链接需要额外权限，跳过端到端断言")
	}

	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.txt")
	fakePath := filepath.Join(dir, "fake.txt")
	storePath := filepath.Join(dir, "store.json")

	if err := os.WriteFile(realPath, []byte("REAL"), 0o644); err != nil {
		t.Fatalf("写入 real 文件失败: %v", err)
	}
	if err := os.WriteFile(fakePath, []byte("FAKE"), 0o644); err != nil {
		t.Fatalf("写入 fake 文件失败: %v", err)
	}

	// 先用 --yes 建立一条有效记录
	if created := runCLI(t, "create", "symlink", "--real", realPath, "--fake", fakePath, "--store-path", storePath, "--yes"); created.exitCode != 0 {
		t.Fatalf("创建记录失败: exit=%d stdout=%q stderr=%q", created.exitCode, created.stdout, created.stderr)
	}

	// fix --all：破坏链接后批量修复，不允许进入交互
	if err := os.Remove(fakePath); err != nil {
		t.Fatalf("删除 fake 以制造无效记录失败: %v", err)
	}
	fixed := runCLI(t, "fix", "--all", "--store-path", storePath)
	if fixed.exitCode != 0 {
		t.Fatalf("fix --all 应成功: exit=%d stdout=%q stderr=%q", fixed.exitCode, fixed.stdout, fixed.stderr)
	}
	if target, err := os.Readlink(fakePath); err != nil || target != realPath {
		t.Fatalf("fix --all 未恢复符号链接: target=%q err=%v", target, err)
	}

	// unlink --all：仅批量、不带 --force/--yes，也应在无终端下完成还原
	unlinked := runCLI(t, "unlink", "--all", "--store-path", storePath)
	if unlinked.exitCode != 0 {
		t.Fatalf("unlink --all 应成功: exit=%d stdout=%q stderr=%q", unlinked.exitCode, unlinked.stdout, unlinked.stderr)
	}
	if info, err := os.Lstat(fakePath); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("unlink --all 后 fake 应为真实文件: info=%v err=%v", info, err)
	}
}

// isCrossDeviceError 判断错误是否为「跨文件系统 rename 不支持」
// 用于识别 overlayfs 等环境下 os.Rename 返回的 EXDEV
func isCrossDeviceError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cross-device link")
}

// unsupportedTrashRenameEnvironment 探测当前环境能否把文件 os.Rename 进临时目录下的回收站。
//
// 为什么需要探测：回收站固定位于 $HOME/.local/share/flk/trash，而 t.TempDir() 在
// overlayfs 等环境里会与新建的子目录处于不同的文件系统，导致 MoveToTrash 必然返回
// 「invalid cross-device link」。这是环境限制（回收站实现刻意不做跨设备回退），
// 不是 --no-trash 开关的行为差异，因此用例应在断言「是否进了回收站」之前先跳过。
//
// 返回 true 表示环境不支持，应当跳过回收站断言
func unsupportedTrashRenameEnvironment(t *testing.T) bool {
	t.Helper()

	base := t.TempDir()
	trashDir := filepath.Join(base, ".local", "share", "flk", "trash", "probe")
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		return true
	}

	probe := filepath.Join(base, "probe.txt")
	if err := os.WriteFile(probe, []byte("probe"), 0o644); err != nil {
		return true
	}
	return isCrossDeviceError(os.Rename(probe, filepath.Join(trashDir, "probe.txt")))
}

// trashContainsFile 在给定回收站根目录下递归查找指定文件名
// 回收站按「时间戳/原绝对路径」存放，目录层级不确定，因此只能递归搜索
func trashContainsFile(t *testing.T, trashRoot, name string) bool {
	t.Helper()

	found := false
	walkErr := filepath.WalkDir(trashRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			// 回收站可能尚未创建，视为未找到而不是用例失败
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !entry.IsDir() && entry.Name() == name {
			found = true
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历回收站失败: %v", walkErr)
	}
	return found
}

// TestCLICreateNoTrashSwitch 验证全局 --no-trash 开关的两种可观察结果：
//   - 默认（不传开关）：覆盖 fake 时仍沿用历史行为，旧文件被移入回收站，可恢复
//   - 传 --no-trash：旧文件被真实删除，回收站内不再留有副本
//
// 两条分支都必须保持 fake 最终是指向 real 的符号链接，即开关只改变「旧数据去哪」，
// 不改变建链结果本身
func TestCLICreateNoTrashSwitch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建符号链接需要额外权限，跳过端到端建链断言")
	}

	// 每个子用例都使用独立的临时 HOME，回收站随之落在各自 HOME 下，互不干扰
	for _, testCase := range []struct {
		name         string
		extraArgs    []string
		wantInTrash  bool
		fileName     string
		trashDirName string
	}{
		{
			name:         "默认移入回收站",
			extraArgs:    nil,
			wantInTrash:  true,
			fileName:     "default-fake.txt",
			trashDirName: "default-fake.txt",
		},
		{
			name:         "no-trash 真实删除",
			extraArgs:    []string{"--no-trash"},
			wantInTrash:  false,
			fileName:     "notrash-fake.txt",
			trashDirName: "notrash-fake.txt",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// store 放在独立临时目录即可；但被移动的文件必须与子进程 HOME 同处一个文件系统，
			// 因为回收站位于 HOME 下，而回收站用 os.Rename 移动文件，跨文件系统会直接失败
			storeDir := t.TempDir()
			storePath := filepath.Join(storeDir, "store.json")

			// childHome 既是子进程的 HOME（回收站落在这里），也是测试文件的所在目录，
			// 从而保证「待移动文件」与「回收站」处于同一文件系统
			childHome := t.TempDir()
			realPath := filepath.Join(childHome, "real.txt")
			fakePath := filepath.Join(childHome, testCase.fileName)
			if err := os.WriteFile(realPath, []byte("REAL"), 0o644); err != nil {
				t.Fatalf("写入 real 文件失败: %v", err)
			}
			if err := os.WriteFile(fakePath, []byte("FAKE"), 0o644); err != nil {
				t.Fatalf("写入 fake 文件失败: %v", err)
			}

			arguments := append([]string{"create", "symlink",
				"--real", realPath,
				"--fake", fakePath,
				"--store-path", storePath,
				"--yes",
			}, testCase.extraArgs...)
			result := runCLIWithEnv(t, childHome, arguments...)

			// 回收站断言依赖「能把文件 rename 进 HOME 下的回收站」这一环境能力。
			// overlayfs 等环境会直接返回 EXDEV，此时默认策略必然失败，属于环境限制；
			// 真实删除分支不依赖该能力，必须照常通过，因此只跳过默认策略的用例
			if testCase.wantInTrash && unsupportedTrashRenameEnvironment(t) {
				t.Skip("当前环境无法把文件 rename 进临时回收站（overlayfs EXDEV），跳过回收站断言")
			}

			if result.exitCode != 0 {
				t.Fatalf("退出码 = %d，stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
			}

			// 无论哪种删除策略，建链结果都必须一致
			target, err := os.Readlink(fakePath)
			if err != nil {
				t.Fatalf("fake 应为符号链接: %v", err)
			}
			if target != realPath {
				t.Fatalf("符号链接目标 = %q，期望 %q", target, realPath)
			}

			// 真实删除后回收站根目录可能根本不存在，trashContainsFile 会把它当作未找到
			trashRoot := filepath.Join(childHome, ".local", "share", "flk", "trash")
			if got := trashContainsFile(t, trashRoot, testCase.trashDirName); got != testCase.wantInTrash {
				t.Fatalf("回收站中存在 %q = %v，期望 %v", testCase.trashDirName, got, testCase.wantInTrash)
			}

			// 删除计划文案必须与所选策略一致，避免用户被误导
			combined := result.stdout + result.stderr
			if testCase.wantInTrash && !strings.Contains(combined, "will be moved to the trash") {
				t.Fatalf("默认策略应展示回收站文案，stdout=%q stderr=%q", result.stdout, result.stderr)
			}
			if !testCase.wantInTrash && !strings.Contains(combined, "will be permanently deleted") {
				t.Fatalf("--no-trash 应展示真实删除文案，stdout=%q stderr=%q", result.stdout, result.stderr)
			}
		})
	}
}

// TestCLIUnlinkNoTrashRemovesLinkPermanently 验证 --no-trash 同样作用于 unlink：
// 解除链接时旧的符号链接被真实删除，回收站内不再留有副本，而派生位置被替换为真实文件
//
// unlink 此前绕过 safeop 直接调用回收站，本用例守住「删除策略已收口到 safeop」这一约定
func TestCLIUnlinkNoTrashRemovesLinkPermanently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建符号链接需要额外权限，跳过端到端建链断言")
	}

	storeDir := t.TempDir()
	storePath := filepath.Join(storeDir, "store.json")
	childHome := t.TempDir()
	realPath := filepath.Join(childHome, "real.txt")
	fakePath := filepath.Join(childHome, "unlink-fake.txt")

	if err := os.WriteFile(realPath, []byte("REAL-CONTENT"), 0o644); err != nil {
		t.Fatalf("写入 real 文件失败: %v", err)
	}
	// fake 不存在时 create 只会建立符号链接并登记记录，不触发备份分支
	if created := runCLIWithEnv(t, childHome, "create", "symlink",
		"--real", realPath, "--fake", fakePath, "--store-path", storePath, "--yes"); created.exitCode != 0 {
		t.Fatalf("登记链接失败: exit=%d stdout=%q stderr=%q", created.exitCode, created.stdout, created.stderr)
	}
	if target, err := os.Readlink(fakePath); err != nil || target != realPath {
		t.Fatalf("前置条件失败，fake 应为指向 real 的符号链接: target=%q err=%v", target, err)
	}

	result := runCLIWithEnv(t, childHome, "unlink", "--all", "--no-trash", "--store-path", storePath)
	if result.exitCode != 0 {
		t.Fatalf("unlink --no-trash 应成功: exit=%d stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}

	// 派生位置必须变成独立真实文件，且不再是指向 real 的符号链接
	info, err := os.Lstat(fakePath)
	if err != nil {
		t.Fatalf("unlink 后 fake 应存在: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("unlink 后 fake 不应再是符号链接")
	}
	content, err := os.ReadFile(fakePath)
	if err != nil {
		t.Fatalf("读取 unlink 后的 fake 失败: %v", err)
	}
	if string(content) != "REAL-CONTENT" {
		t.Fatalf("unlink 后 fake 内容 = %q，期望 %q", content, "REAL-CONTENT")
	}

	// 真实删除策略下，被移除的旧链接不应出现在回收站
	trashRoot := filepath.Join(childHome, ".local", "share", "flk", "trash")
	if trashContainsFile(t, trashRoot, filepath.Base(fakePath)) {
		t.Fatalf("--no-trash 下旧链接不应进入回收站: %s", trashRoot)
	}
}
