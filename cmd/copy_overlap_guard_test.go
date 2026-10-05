package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/flk/internal/locales"
	"github.com/jy-eggroll/flk/internal/pathutil"
)

// 本文件覆盖组4 之外的两处命令层「自引用复制」前置判断：
//   - cmd/unlink.go 的 replaceWithReal：先 safeop.Delete(derived) 再 pathutil.Copy(actualSource, derived)
//   - cmd/fix.go 的 backfillSourceIfMissing：pathutil.Copy(derived, source)
//
// 为什么命令层要单独测一遍（底层 pathutil.Copy 已有守卫）：
// 底层守卫只能保证「不会真的复制」，拦不住「状态已经被改坏」——unlink 是先删除派生位置再复制，
// 若只靠底层报错，此时派生位置上的链接/数据已经进了回收站或被永久删除；
// 因此本文件的断言重点不是「有没有报错」，而是「报错时文件系统分毫未动」
//
// 测试安全红线（与 cmd/record_test.go 同一约定）：
//   - 所有数据都在 t.TempDir() 内构造，绝不触碰真实家目录或真实配置
//   - 不设置任何全局开关（noTrash 保持默认），用例只验证「拒绝」这一条提前返回路径
//   - 文件名与用例名刻意带 copy/collision 语义，避免与同包其它测试文件重名

// copyGuardWriteFile 在指定路径写入探针文件，路径不存在时先建好父目录
// 之所以自己写而不是复用生产代码：测试探针必须完全独立于被测实现，避免「用被测代码验证被测代码」
func copyGuardWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建父目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入探针失败: %v", err)
	}
}

// copyGuardReadFile 读取探针内容，读取失败直接判定为用例失败
func copyGuardReadFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取探针失败: %v", err)
	}
	return string(content)
}

// copyGuardDirNames 返回目录下的条目名列表，用于核对「没有多出垃圾目录」
func copyGuardDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestReplaceWithRealRejectsSelfReference 验证 unlink 的前置判断：
// 派生位置位于权威源内部、或与权威源是同一路径时直接报错返回，
// 并且「先删除再复制」这个顺序里的删除动作一次都没有发生
//
// 可达路径（真实可复现）：flk create symlink -r ~/repo -f ~/repo/self 之后执行 flk unlink，
// 修复前 CopyDir 会边复制边把目标写进源里，每层多一级同名子目录，直到路径超长报错，
// 期间在用户源目录里留下大量垃圾目录
func TestReplaceWithRealRejectsSelfReference(t *testing.T) {
	t.Run("派生位置位于权威源内部且链接未被删除", func(t *testing.T) {
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		copyGuardWriteFile(t, filepath.Join(repo, "probe.txt"), "keep-me")

		// 真实场景里派生位置本身是一条指向权威源的符号链接（create symlink 的产物），
		// 把它建成真的链接而不是留空，才能证明「前置判断没有走到 safeop.Delete」：
		// 链接还在，就说明删除动作确实没有发生
		derived := filepath.Join(repo, "self")
		if err := os.Symlink(repo, derived); err != nil {
			t.Fatalf("创建指向权威源的符号链接失败: %v", err)
		}

		err := replaceWithReal(repo, derived, "real", "fake", true, false, io.Discard)
		if err == nil {
			t.Fatal("派生位置位于权威源内部时必须拒绝")
		}
		if !errors.Is(err, pathutil.ErrCopyPathOverlap) {
			t.Fatalf("错误 = %v，期望可被 errors.Is 判定为 %v", err, pathutil.ErrCopyPathOverlap)
		}

		// 链接必须原样存在（证明 safeop.Delete 没有被执行）
		link, linkErr := os.Readlink(derived)
		if linkErr != nil {
			t.Fatalf("派生位置的链接被删除或改写: %v", linkErr)
		}
		if link != repo {
			t.Fatalf("派生位置链接目标被改写: got %s, want %s", link, repo)
		}

		// 源目录内容必须分毫未动
		names := copyGuardDirNames(t, repo)
		if len(names) != 2 {
			t.Fatalf("源目录条目 = %v，期望只有 probe.txt 与 self", names)
		}
		if got := copyGuardReadFile(t, filepath.Join(repo, "probe.txt")); got != "keep-me" {
			t.Fatalf("源探针被改写: got %q, want %q", got, "keep-me")
		}
	})

	t.Run("深层子目录路径不创建任何中间目录", func(t *testing.T) {
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		copyGuardWriteFile(t, filepath.Join(repo, "probe.txt"), "keep-me")

		// 目标连中间层都不存在：守卫必须在任何文件系统操作之前，连父目录都不能被创建
		derived := filepath.Join(repo, "a", "b", "self")
		err := replaceWithReal(repo, derived, "real", "fake", true, false, io.Discard)
		if !errors.Is(err, pathutil.ErrCopyPathOverlap) {
			t.Fatalf("错误 = %v，期望可被 errors.Is 判定为 %v", err, pathutil.ErrCopyPathOverlap)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "a")); statErr == nil {
			t.Fatal("拒绝时不应创建目标的任何中间目录")
		}
		if got := copyGuardReadFile(t, filepath.Join(repo, "probe.txt")); got != "keep-me" {
			t.Fatalf("源探针被改写: got %q, want %q", got, "keep-me")
		}
	})

	t.Run("派生位置与权威源是同一路径时拒绝且目录完好", func(t *testing.T) {
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		copyGuardWriteFile(t, filepath.Join(repo, "probe.txt"), "keep-me")

		// 同一路径是最危险的形态：修复前会先把 repo 整个删除再复制，源数据直接消失
		err := replaceWithReal(repo, repo, "real", "fake", true, false, io.Discard)
		if !errors.Is(err, pathutil.ErrCopyPathOverlap) {
			t.Fatalf("错误 = %v，期望可被 errors.Is 判定为 %v", err, pathutil.ErrCopyPathOverlap)
		}
		if got := copyGuardReadFile(t, filepath.Join(repo, "probe.txt")); got != "keep-me" {
			t.Fatalf("源探针被改写（目录可能已被删除）: got %q, want %q", got, "keep-me")
		}
	})
}

// TestBackfillSourceIfMissingRejectsSelfReference 验证 fix 回填路径的前置判断：
// 权威副本 source 位于派生位置 derived 内部时直接报错返回，且不产生任何文件系统变更
//
// 该方向的损害与 unlink 相反但同族：CopyDir 会先 MkdirAll(source) 再 ReadDir(derived)，
// 读到自己刚建出来的 source 并递归下钻，边复制边把目标写进源里，直到路径超长或磁盘写满
func TestBackfillSourceIfMissingRejectsSelfReference(t *testing.T) {
	base := t.TempDir()
	derived := filepath.Join(base, "derived")
	copyGuardWriteFile(t, filepath.Join(derived, "probe.txt"), "keep-me")

	// source 必须缺失，否则函数会在入口的 os.Stat 处直接返回「无需回填」
	source := filepath.Join(derived, "inner")

	err := backfillSourceIfMissing(source, derived, "prim", "seco")
	if err == nil {
		t.Fatal("source 位于 derived 内部时必须拒绝")
	}
	if !errors.Is(err, pathutil.ErrCopyPathOverlap) {
		t.Fatalf("错误 = %v，期望可被 errors.Is 判定为 %v", err, pathutil.ErrCopyPathOverlap)
	}

	// source 绝不能被创建（修复前它会被 MkdirAll 建出来，随后被读回 derived 形成递归）
	if _, statErr := os.Lstat(source); statErr == nil {
		t.Fatalf("拒绝时不应创建 source: %s", source)
	}
	// derived 必须仍是原来的样子：只有探针一个条目，没有被复制进来的垃圾
	names := copyGuardDirNames(t, derived)
	if len(names) != 1 || names[0] != "probe.txt" {
		t.Fatalf("derived 目录被污染，条目 = %v，期望只有 probe.txt", names)
	}
	if got := copyGuardReadFile(t, filepath.Join(derived, "probe.txt")); got != "keep-me" {
		t.Fatalf("derived 探针被改写: got %q, want %q", got, "keep-me")
	}
}

// TestCopyOverlapGuardMessageDirection 固定拒绝文案的「方向」，防止再次写反
//
// 起因：文案曾经写成 "Refusing to copy {{.Dst}} into {{.Src}}"，而命中的形态是
// IsSubPath(src, dst)（dst 位于 src 内部）、复制方向是 src -> dst，于是用户看到的是
// "copy <目标> into <源>" 这种与事实相反的方向描述——拦截结论没错，但用户会照着一个
// 反方向的提示去改命令。修复后模板固定为 "copy {{.Src}} into {{.Dst}}"
//
// 断言手法：用 l10n.T 现场渲染期望文案，而不是硬编码整句英文
//   - 与占位符映射强相关：模板若再次把两个占位符写反，渲染出的字符串必然与实现产出的不同，
//     用例立刻失败（这正是真正要守住的约束）
//   - 之所以必须先 l10n.Init：T 在 Init 未被调用时按设计直接返回源串，模板占位符不会渲染
//     （见 pkg/l10n/l10n.go 的 T 实现），那样两边都是「未渲染的同一个模板」，
//     方向写反与否根本看不出来。Init 用 en 加载的是 //go:embed 进 locales 包的语言文件，
//     不依赖磁盘上的文件，也不读任何环境变量
//   - Init 会落位进程级的 localizer，作用域覆盖本测试二进制；本包其它用例（record_test.go 等）
//     不依赖 l10n 的渲染结果，因此不会被这一步影响
//
// 同时反向断言「写反的那条文案」绝不能出现，避免出现「两句都命中」的模糊情况
func TestCopyOverlapGuardMessageDirection(t *testing.T) {
	// 语言固定为 en：这里要验证的是占位符到路径的映射方向，不是多语言能力，
	// 固定语言才能让「期望文案」与「实现文案」在同一个 localizer 下可比
	if err := l10n.Init(locales.Default, locales.Options()); err != nil {
		t.Fatalf("初始化 l10n 失败: %v", err)
	}

	const template = "Refusing to copy {{.Src}} into {{.Dst}}: the destination path is inside the source path, so copying would keep growing into itself"

	t.Run("unlink：权威源 -> 派生位置", func(t *testing.T) {
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		copyGuardWriteFile(t, filepath.Join(repo, "probe.txt"), "keep-me")

		derived := filepath.Join(repo, "self")
		if err := os.Symlink(repo, derived); err != nil {
			t.Fatalf("创建符号链接失败: %v", err)
		}

		err := replaceWithReal(repo, derived, "real", "fake", true, false, io.Discard)
		if err == nil {
			t.Fatal("派生位置位于权威源内部时必须拒绝")
		}

		// 期望方向：把权威源 repo 复制进它的子目录 derived
		want := l10n.T(template, map[string]any{"Src": repo, "Dst": derived})
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("文案方向不正确:\n got = %s\nwant 包含 = %s", err.Error(), want)
		}
		// 反向（占位符写反）的文案绝不允许出现
		wrong := l10n.T(template, map[string]any{"Src": derived, "Dst": repo})
		if strings.Contains(err.Error(), wrong) {
			t.Fatalf("文案把源和目标写反了: %s", err.Error())
		}
	})

	t.Run("fix 回填：派生位置 -> 权威源", func(t *testing.T) {
		base := t.TempDir()
		derived := filepath.Join(base, "derived")
		copyGuardWriteFile(t, filepath.Join(derived, "probe.txt"), "keep-me")

		source := filepath.Join(derived, "inner")
		err := backfillSourceIfMissing(source, derived, "prim", "seco")
		if err == nil {
			t.Fatal("source 位于 derived 内部时必须拒绝")
		}

		// 期望方向：回填是 derived -> source，因此「源」是 derived、「目标」是 source
		want := l10n.T(template, map[string]any{"Src": derived, "Dst": source})
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("文案方向不正确:\n got = %s\nwant 包含 = %s", err.Error(), want)
		}
		wrong := l10n.T(template, map[string]any{"Src": source, "Dst": derived})
		if strings.Contains(err.Error(), wrong) {
			t.Fatalf("文案把源和目标写反了: %s", err.Error())
		}
	})

	t.Run("同一路径：文案与方向无关，但必须带上该路径", func(t *testing.T) {
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		copyGuardWriteFile(t, filepath.Join(repo, "probe.txt"), "keep-me")

		err := replaceWithReal(repo, repo, "real", "fake", true, false, io.Discard)
		if err == nil {
			t.Fatal("同一路径时必须拒绝")
		}
		want := l10n.T("Refusing to copy {{.Dst}} onto itself: the source and destination paths are the same", map[string]any{"Dst": repo})
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("同一路径文案不正确:\n got = %s\nwant 包含 = %s", err.Error(), want)
		}
	})
}
