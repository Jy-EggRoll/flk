package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// 这些用例覆盖「原地改名交接」的三条路径：成功、第二步失败后回滚、第一步就失败。
// 交接本身是平台无关的（用普通文件即可复现），真正依赖 Windows 的只有
// 「能否给正在运行的映像改名」这一点，而那一点无法在非 Windows 上验证——
// 因此这里锁住的是顺序与回滚语义，不是平台能力

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("准备文件 %s 失败: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(b)
}

// TestSwapExecutableSuccess 验证交接成功后：原名下是新版本、旧文件被挪到 oldPath、暂存文件不再存在
func TestSwapExecutableSuccess(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk.exe")
	oldPath := execPath + ".old"
	staged := filepath.Join(dir, ".upgrade-1")

	writeFile(t, execPath, "旧版本")
	writeFile(t, staged, "新版本")

	if err := swapExecutable(staged, execPath, oldPath); err != nil {
		t.Fatalf("交接失败: %v", err)
	}

	if got := readFile(t, execPath); got != "新版本" {
		t.Errorf("原名下内容 = %q，期望新版本", got)
	}
	if got := readFile(t, oldPath); got != "旧版本" {
		t.Errorf("oldPath 内容 = %q，期望旧版本", got)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Error("暂存文件应当已被改名占用，不该还留在原地")
	}
}

// TestSwapExecutableRollsBack 验证第二步失败时把旧文件改回原名
//
// 这是本方案最要紧的一条：失败的升级绝不能把程序弄丢。
// 手段是让第二步注定失败——传一个不存在的暂存路径，此时第一步已经成功（原名已让出）
func TestSwapExecutableRollsBack(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk.exe")
	oldPath := execPath + ".old"
	staged := filepath.Join(dir, ".upgrade-missing")

	writeFile(t, execPath, "旧版本")

	err := swapExecutable(staged, execPath, oldPath)
	if err == nil {
		t.Fatal("暂存文件不存在时交接应当失败")
	}

	if got := readFile(t, execPath); got != "旧版本" {
		t.Errorf("回滚后原名下内容 = %q，期望旧版本", got)
	}
	if _, statErr := os.Stat(oldPath); !os.IsNotExist(statErr) {
		t.Error("回滚后不应再留下 oldPath")
	}
}

// TestSwapExecutableFailsWhenExecutableMissing 验证第一步失败时如实报错且不产生任何改名副作用
func TestSwapExecutableFailsWhenExecutableMissing(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk.exe")
	staged := filepath.Join(dir, ".upgrade-1")

	writeFile(t, staged, "新版本")

	if err := swapExecutable(staged, execPath, execPath+".old"); err == nil {
		t.Fatal("原名不存在时交接应当失败")
	}
	if got := readFile(t, staged); got != "新版本" {
		t.Errorf("失败后暂存文件应保持原样，实际内容 = %q", got)
	}
}
