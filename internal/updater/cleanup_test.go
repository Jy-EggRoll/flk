package updater

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 这些用例锁住残留清理的边界：该删的两类删掉、不该删的一个都不动。
// 清理会真的删除文件，因此断言必须同时覆盖「删了什么」与「留了什么」，
// 只验证前者会让一次误删（例如把用户的文件当成残留）在测试里完全看不出来

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// TestCleanupLeftoversRemovesOld 验证交接产生的 .old 会被清掉
func TestCleanupLeftoversRemovesOld(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk.exe")
	writeFile(t, execPath, "新版本")
	writeFile(t, execPath+oldSuffix, "旧版本")

	CleanupLeftovers(execPath)

	if exists(t, execPath+oldSuffix) {
		t.Error(".old 应当已被清理")
	}
	if !exists(t, execPath) {
		t.Error("可执行文件本身不该被碰")
	}
}

// TestCleanupLeftoversRemovesStaleStaging 验证陈旧的中断下载暂存文件会被清掉
func TestCleanupLeftoversRemovesStaleStaging(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk.exe")
	writeFile(t, execPath, "程序")

	stale := filepath.Join(dir, stagingPrefix+"123456")
	writeFile(t, stale, "半截的下载")
	old := time.Now().Add(-staleStagingMaxAge - time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("调整修改时间失败: %v", err)
	}

	CleanupLeftovers(execPath)

	if exists(t, stale) {
		t.Error("超过阈值未改动的暂存文件应当被清理")
	}
}

// TestCleanupLeftoversKeepsFreshStaging 验证刚写过的暂存文件不会被删
//
// 防的场景：另一个进程正在下载，它的暂存文件会随下载持续写入；
// 此时把它删掉会让那次升级失败，因此清理必须放过「刚刚还被写过」的文件
func TestCleanupLeftoversKeepsFreshStaging(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk.exe")
	writeFile(t, execPath, "程序")

	fresh := filepath.Join(dir, stagingPrefix+"999999")
	writeFile(t, fresh, "正在下载")

	CleanupLeftovers(execPath)

	if !exists(t, fresh) {
		t.Error("正在下载的暂存文件被误删")
	}
}

// TestCleanupLeftoversLeavesOtherFilesAlone 验证只认这两类名字，安装目录里的其它内容一律不动
func TestCleanupLeftoversLeavesOtherFilesAlone(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk.exe")
	writeFile(t, execPath, "程序")

	// 用户自己放在安装目录里的东西，以及一个名字相近但不是暂存文件的文件
	untouched := []string{
		filepath.Join(dir, "flk-store.json"),
		filepath.Join(dir, "upgrade-notes.txt"), // 前缀不带点，不应被当作暂存文件
		filepath.Join(dir, "flk.exe.old.txt"),   // 不是以 .old 结尾
	}
	for _, path := range untouched {
		writeFile(t, path, "用户的东西")
	}
	sub := filepath.Join(dir, ".upgrade-dir") // 同名前缀但它是目录，不该被当成文件删除
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}

	CleanupLeftovers(execPath)

	for _, path := range untouched {
		if !exists(t, path) {
			t.Errorf("不该动 %s", filepath.Base(path))
		}
	}
	if !exists(t, sub) {
		t.Error("同名前缀的目录不该被删除")
	}
}

// TestCleanupLeftoversOnMissingPathsIsHarmless 验证目标不存在时不 panic、不报错
// 绝大多数运行都属于这种情况（从来没有升级过），它必须安静地什么都不做
func TestCleanupLeftoversOnMissingPathsIsHarmless(t *testing.T) {
	CleanupLeftovers(filepath.Join(t.TempDir(), "不存在的目录", "flk.exe"))
}
