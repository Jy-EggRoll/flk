package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteCleansTempOnFailure 验证落位失败时会清掉临时文件
//
// 手段是把目标路径做成一个非空目录：rename 到目录上必然失败，从而走到清理分支。
// 这一条必须单独守——失败路径上的临时文件不会被任何成功用例发现，
// 而它恰恰是用户最容易在"磁盘满了/权限不对"之后撞见的东西
func TestWriteCleansTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "flk-store.json")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatalf("准备非空目录失败: %v", err)
	}

	if err := Write(target, []byte("{}\n")); err == nil {
		t.Fatal("目标是非空目录时写入应当失败")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "flk-store.json" {
			t.Fatalf("失败后残留了临时文件 %q", e.Name())
		}
	}
}

// TestWriteLeavesNoTempFile 验证成功写入后不留临时文件
//
// 原子写会先建同目录临时文件，用例锁住"临时文件已随 rename 消失"这一可见后果：
// 若 rename 没发生（例如误写成复制），用户目录里就会攒下一堆 .tmp
func TestWriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "flk-store.json")

	if err := Write(target, []byte("{}\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "flk-store.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("写入后目录内容 = %v，期望只有 flk-store.json", names)
	}
}

// TestWritePreservesFileMode 验证落位后沿用目标原有的权限位
//
// 临时文件由 CreateTemp 建成 0600，若不显式对齐，用户原本 0644 的文件写一次就被收窄，
// 之后别的程序读不到、而用户不知道为什么
func TestWritePreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "flk-store.json")
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatalf("调整权限失败: %v", err)
	}

	if err := Write(target, []byte(`{"a":1}`+"\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("权限位 = %o，期望 600（临时文件默认 600，落位前必须对齐原文件）", got)
	}
}

// TestWriteFollowsSymlink 守住「目标是符号链接时，写入必须落在链接指向的真实文件上」
//
// 这是本项目明确支持的用法：用户把设置文件或清单文件链进自己的配置仓库。
// 若落位用的是 rename 到链接路径，链接会被换成普通文件，用户的仓库与该入口从此脱钩，
// 直到下次同步才发现两边各写各的——这类缺陷不会报错，只会静默丢同步
func TestWriteFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "repo", "flk-store.json")
	linkPath := filepath.Join(dir, "flk-store.json")
	if err := os.MkdirAll(filepath.Dir(realPath), 0o755); err != nil {
		t.Fatalf("准备仓库目录失败: %v", err)
	}
	if err := os.WriteFile(realPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("准备真实文件失败: %v", err)
	}
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("创建符号链接失败: %v", err)
	}

	if err := Write(linkPath, []byte(`{"keep":"link"}`+"\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat 失败: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("链接本身被替换成了普通文件，用户放进仓库的意图被推翻")
	}
	content, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("读取真实文件失败: %v", err)
	}
	if string(content) != `{"keep":"link"}`+"\n" {
		t.Errorf("真实文件内容 = %q，期望写入落在链接指向的真实文件上", content)
	}
}

// TestWriteFollowsDanglingSymlink 守住「链接的目标还不存在时，写入仍不得把链接变成普通文件」
//
// 与上一条的区别在链接形态：那里目标已存在（EvalSymlinks 能解开），
// 这里目标尚未创建——正是"刚把文件链进仓库、仓库侧那份还没写"的时刻，
// 此时 EvalSymlinks 会报错，实现必须退回到手工沿链接走一步，而不是把链接写坏
func TestWriteFollowsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "repo", "flk-store.json")
	linkPath := filepath.Join(dir, "flk-store.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("创建断链失败: %v", err)
	}

	if err := Write(linkPath, []byte(`{"fresh":true}`+"\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat 失败: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("链接本身被替换成了普通文件，用户放进仓库的意图被推翻")
	}
	content, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("目标文件未被创建: %v", err)
	}
	if string(content) != `{"fresh":true}`+"\n" {
		t.Errorf("真实文件内容 = %q，期望首次写入落在链接指向的位置", content)
	}
}
