package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
