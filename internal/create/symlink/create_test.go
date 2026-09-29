package symlink

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
)

// 本文件为 internal/create/symlink 的单元测试，逐条固化 Create 的既有行为边界
// 隔离红线：所有用例一律在 t.TempDir() 内构造，绝不对真实家目录、根目录、~/.config/flk 或任何真实用户数据
// 做创建、删除、移动；真实家目录只作为「受保护路径围栏」的输入被读取判定，且该用例带两重保险

// forceRemoveOpts 构造「跳过交互确认 + 真实删除」的删除选项
//
// Force 置位是为了让「副路径已存在」的覆盖分支无需任何终端交互就能走到删除
// NoTrash 置位是测试的隔离要求：默认策略会把目标移入真实回收站（~/.local/share/flk/trash），
// 那属于测试红线之外的副作用，会让反复跑测试残留垃圾，因此测试一律走真实删除
// Output 固定指向内存缓冲区，既避免删除计划污染测试 stdout，也让「计划是否写出」可以被断言
func forceRemoveOpts(buffer *bytes.Buffer) safeop.RemoveOptions {
	return safeop.RemoveOptions{Force: true, NoTrash: true, Output: buffer}
}

// mustWriteFile 写入测试文件，失败直接终止用例
// 之所以统一走这个辅助函数：所有用例都需要「造一个必然存在的主路径」，重复的 WriteFile 会让每个用例更啰嗦
func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("创建测试文件 %q 失败: %v", path, err)
	}
}

// mustReadLink 读取符号链接目标，失败直接终止用例
func mustReadLink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("读取符号链接 %q 失败: %v", path, err)
	}
	return target
}

// mustAbs 取绝对路径，失败直接终止用例
// 被测实现内部用 filepath.Abs 计算链接目标，断言必须用同一口径，否则 macOS 的 /var 与 /private/var
// 这类软链接会把「实现正确」误报成断言失败
func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("解析 %q 的绝对路径失败: %v", path, err)
	}
	return abs
}

// requireSymlinkSupport 在临时目录内探测当前环境能否创建符号链接
// Windows 未开启开发者模式时 os.Symlink 会返回权限错误，此时跳过需要真实建链的用例，
// 而不是把环境限制误报成实现缺陷；只依赖错误返回的「围栏」用例不受影响，仍会照常执行
func requireSymlinkSupport(t *testing.T) {
	t.Helper()
	probeDir := t.TempDir()
	probeTarget := filepath.Join(probeDir, "probe-target.txt")
	mustWriteFile(t, probeTarget, "probe")
	if err := os.Symlink(probeTarget, filepath.Join(probeDir, "probe-link")); err != nil {
		t.Skipf("当前环境无法创建符号链接，跳过依赖建链的用例: %v", err)
	}
}

// protectedPathCase 是受保护路径用例：name 用于子测试命名，path 是被保护目标本身的等价写法
type protectedPathCase struct {
	name string
	path string
}

// protectedPathCases 构造家目录本身的几种等价写法（尾部斜杠、./ 归一化形式）
// 这些路径只用于验证「实现会在触碰文件系统之前拒绝」，测试绝不对它们做任何创建、删除或移动
func protectedPathCases(t *testing.T) []protectedPathCase {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}
	home = filepath.Clean(home)
	sep := string(os.PathSeparator)

	return []protectedPathCase{
		{"家目录本身", home},
		{"家目录带尾部斜杠", home + sep},
		{"家目录的 ./ 归一化形式", filepath.Join(home, ".")},
	}
}

// TestCreateReturnsErrorWhenRealPathMissing 固化「主路径不存在时直接上抛 os.Stat 错误」
// 防的回归：主路径不存在却先动了副路径——用户的既有目标被删掉，链接又建不出来，等于凭空丢失文件
func TestCreateReturnsErrorWhenRealPathMissing(t *testing.T) {
	base := t.TempDir()
	realPath := filepath.Join(base, "missing-real.txt")
	fakePath := filepath.Join(base, "fake-link")

	var buffer bytes.Buffer
	err := Create(realPath, fakePath, forceRemoveOpts(&buffer))
	if err == nil {
		t.Fatal("主路径不存在时应返回错误，实际返回 nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("返回错误 = %v，期望可被 errors.Is(err, os.ErrNotExist) 识别", err)
	}

	if _, statErr := os.Lstat(fakePath); !os.IsNotExist(statErr) {
		t.Fatalf("主路径不存在时不应创建副路径，Lstat(%q) 错误 = %v，期望 os.ErrNotExist", fakePath, statErr)
	}
	if buffer.Len() != 0 {
		t.Fatalf("主路径不存在时不应产生删除计划，实际输出: %q", buffer.String())
	}
}

// TestCreateCreatesSymlinkWithAbsoluteTarget 固化「副路径不存在时建链成功」且链接目标是主路径的绝对路径
// 副路径刻意放在还不存在的多级子目录下，顺带覆盖 pathutil.EnsureDirExists 的 MkdirAll 分支
// 防的回归：写相对路径或原样写入用户给的相对主路径——链接一旦跟着工作目录漂移就会指错位置
func TestCreateCreatesSymlinkWithAbsoluteTarget(t *testing.T) {
	requireSymlinkSupport(t)

	base := t.TempDir()
	realPath := filepath.Join(base, "real.txt")
	mustWriteFile(t, realPath, "真实内容")

	fakePath := filepath.Join(base, "nested", "deep", "fake-link")

	var buffer bytes.Buffer
	if err := Create(realPath, fakePath, forceRemoveOpts(&buffer)); err != nil {
		t.Fatalf("创建符号链接失败: %v", err)
	}

	got := mustReadLink(t, fakePath)
	want := mustAbs(t, realPath)
	if got != want {
		t.Fatalf("符号链接目标 = %q，期望主路径的绝对路径 %q", got, want)
	}

	// 链接必须真的可用：跟随后应命中主路径同一份数据
	followed, err := os.Stat(fakePath)
	if err != nil {
		t.Fatalf("跟随符号链接 %q 失败: %v", fakePath, err)
	}
	realInfo, err := os.Stat(realPath)
	if err != nil {
		t.Fatalf("读取主路径信息失败: %v", err)
	}
	if !os.SameFile(followed, realInfo) {
		t.Fatal("符号链接跟随后的文件与主路径不是同一份数据")
	}
}

// TestCreateReplacesExistingTargetWithForce 固化 Force 分支：旧目标被删除、新链接建立
// 防的回归有两条：
//   - 旧链接没被删掉，os.Symlink 直接报 EEXIST，--force 形同虚设
//   - 删除旧链接时跟随到链接背后，把用户真实数据一起删掉（os.RemoveAll 对链接只删自身，这是必须保住的语义）
func TestCreateReplacesExistingTargetWithForce(t *testing.T) {
	requireSymlinkSupport(t)

	base := t.TempDir()
	realPath := filepath.Join(base, "real.txt")
	mustWriteFile(t, realPath, "新内容")

	staleTarget := filepath.Join(base, "stale-target.txt")
	mustWriteFile(t, staleTarget, "旧链接指向的真实数据")

	fakePath := filepath.Join(base, "fake-link")
	if err := os.Symlink(staleTarget, fakePath); err != nil {
		t.Fatalf("创建待覆盖的旧链接失败: %v", err)
	}

	var buffer bytes.Buffer
	if err := Create(realPath, fakePath, forceRemoveOpts(&buffer)); err != nil {
		t.Fatalf("Force 覆盖已存在的副路径失败: %v", err)
	}

	got := mustReadLink(t, fakePath)
	want := mustAbs(t, realPath)
	if got != want {
		t.Fatalf("覆盖后符号链接目标 = %q，期望 %q", got, want)
	}

	if _, err := os.Stat(staleTarget); err != nil {
		t.Fatalf("旧链接指向的真实文件被误删: %v", err)
	}

	// Force 只跳过确认，删除计划本身仍必须写出到调用方注入的 writer
	absFake := mustAbs(t, fakePath)
	if !strings.Contains(buffer.String(), absFake) {
		t.Fatalf("删除计划未写入注入的 writer，实际输出: %q，期望包含 %q", buffer.String(), absFake)
	}
}

// TestCreateCancellationKeepsOldTarget 固化副路径已存在且用户拒绝确认时的语义
// 注入恒返回 false 的 Confirm 并保持 Force: false，完全避开终端交互，结论稳定可复现
// 防的回归：把「用户拒绝」当成「删除失败后继续建链」，旧目标被保留却仍尝试建链并报 EEXIST，
// 用户看到的是莫名其妙的失败，而不是明确的「已取消」
func TestCreateCancellationKeepsOldTarget(t *testing.T) {
	requireSymlinkSupport(t)

	base := t.TempDir()
	realPath := filepath.Join(base, "real.txt")
	mustWriteFile(t, realPath, "新内容")

	// 副路径用一个普通文件而不是链接，顺带覆盖「已存在的普通文件被拒绝删除后必须原样保留」
	fakePath := filepath.Join(base, "fake-link")
	mustWriteFile(t, fakePath, "旧内容")

	var buffer bytes.Buffer
	err := Create(realPath, fakePath, safeop.RemoveOptions{
		Force:   false,
		NoTrash: true,
		Output:  &buffer,
		Confirm: func() (bool, error) {
			return false, nil
		},
	})
	if !errors.Is(err, safeop.ErrOperationCancelled) {
		t.Fatalf("返回错误 = %v，期望 %v", err, safeop.ErrOperationCancelled)
	}

	content, readErr := os.ReadFile(fakePath)
	if readErr != nil {
		t.Fatalf("取消后旧目标应原样保留，读取失败: %v", readErr)
	}
	if string(content) != "旧内容" {
		t.Fatalf("取消后旧目标内容 = %q，期望 %q", string(content), "旧内容")
	}

	info, err := os.Lstat(fakePath)
	if err != nil {
		t.Fatalf("取消后读取旧目标信息失败: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("取消后不应建立新的符号链接")
	}
}

// TestCreateRebuildsWhenParentIsFile 固化 pathutil.ExistsButNotDirectoryError 分支：
// 副路径的父路径是一个普通文件时，先删除该文件、重建中间目录，再建链
// 防的回归：父路径是文件时直接把 ExistsButNotDirectoryError 上抛——用户明明给了 --force，
// 却得到一个无法自助解决的错误，还得手动去删那个挡路的文件
func TestCreateRebuildsWhenParentIsFile(t *testing.T) {
	requireSymlinkSupport(t)

	base := t.TempDir()
	realPath := filepath.Join(base, "real.txt")
	mustWriteFile(t, realPath, "真实内容")

	// 挡路的普通文件：它同时是副路径的父路径
	blocker := filepath.Join(base, "blocker")
	mustWriteFile(t, blocker, "挡路的普通文件")

	fakePath := filepath.Join(blocker, "child-link")

	var buffer bytes.Buffer
	if err := Create(realPath, fakePath, forceRemoveOpts(&buffer)); err != nil {
		t.Fatalf("父路径为普通文件时应删除并重建目录，实际返回错误: %v", err)
	}

	// 断言口径：实现删除挡路文件后会执行 EnsureDirExists 的重试，由 MkdirAll 在同一个路径上重建目录，
	// 因此这里不能断言「路径不存在」，而要断言「原来的普通文件已被目录取代」——这才是可观测的删除证据
	blockerInfo, err := os.Lstat(blocker)
	if err != nil {
		t.Fatalf("挡路文件被删除后应在同一路径重建出目录，Lstat(%q) 错误 = %v，期望 nil", blocker, err)
	}
	if !blockerInfo.IsDir() {
		t.Fatalf("挡路路径 %q 应为重建出的目录，实际模式 = %v", blocker, blockerInfo.Mode())
	}

	parentInfo, err := os.Stat(filepath.Dir(fakePath))
	if err != nil {
		t.Fatalf("重建后的中间目录应存在: %v", err)
	}
	if !parentInfo.IsDir() {
		t.Fatalf("中间路径 %q 应为目录，实际模式 = %v", filepath.Dir(fakePath), parentInfo.Mode())
	}

	got := mustReadLink(t, fakePath)
	want := mustAbs(t, realPath)
	if got != want {
		t.Fatalf("重建目录后符号链接目标 = %q，期望 %q", got, want)
	}
}

// TestCreateRejectsProtectedFakePath 回归保护「受保护路径围栏」：
// 副路径为当前用户家目录本身时必须返回可被 errors.Is(err, safeop.ErrProtectedPath) 识别的错误
//
// 两重保险（红线：绝不允许真的删除或移动家目录）：
//   - 前置断言先用 pathutil.IsProtectedPath 确认用例路径确实命中围栏口径，未命中就直接失败退出，
//     不进入被测函数——被测函数对同一字符串调用的是同一个函数，因此这是忠实的前置条件
//   - RemoveOptions.Confirm 恒返回 false，即使围栏失效，最坏结果也只是「用户取消」而不会删除家目录
//
// 防的回归：覆盖已存在的副路径时绕过围栏，把家目录整体移入回收站或真实删除——对用户而言等同于家目录瞬间消失
func TestCreateRejectsProtectedFakePath(t *testing.T) {
	base := t.TempDir()

	// 主路径必须真实存在，否则实现会在 os.Stat 处提前返回，永远走不到围栏
	realPath := filepath.Join(base, "real.txt")
	mustWriteFile(t, realPath, "真实内容")

	for _, tc := range protectedPathCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			if !pathutil.IsProtectedPath(tc.path) {
				t.Fatalf("前置断言失败：pathutil.IsProtectedPath(%q) = false，拒绝执行 Create 以免造成不可逆破坏", tc.path)
			}

			var buffer bytes.Buffer
			confirmCalled := false
			err := Create(realPath, tc.path, safeop.RemoveOptions{
				Force:   false,
				NoTrash: true,
				Output:  &buffer,
				Confirm: func() (bool, error) {
					confirmCalled = true
					return false, nil
				},
			})

			if !errors.Is(err, safeop.ErrProtectedPath) {
				t.Fatalf("Create(%q, %q) 返回错误 = %v，期望 %v", realPath, tc.path, err, safeop.ErrProtectedPath)
			}
			if errors.Is(err, safeop.ErrOperationCancelled) {
				t.Fatal("受保护路径必须返回 ErrProtectedPath，而不是表达为「用户取消」")
			}
			if confirmCalled {
				t.Fatal("受保护路径不应进入确认流程")
			}
			if buffer.Len() != 0 {
				t.Fatalf("受保护路径不应打印删除计划，实际输出: %q", buffer.String())
			}

			// 家目录必须毫发无损：仍然存在且仍是目录
			info, statErr := os.Lstat(tc.path)
			if statErr != nil {
				t.Fatalf("受保护路径 %q 被删除或移动: %v", tc.path, statErr)
			}
			if !info.IsDir() {
				t.Fatalf("受保护路径 %q 应仍是目录，实际模式 = %v", tc.path, info.Mode())
			}

			if _, statErr := os.Stat(realPath); statErr != nil {
				t.Fatalf("主路径不应被围栏用例影响: %v", statErr)
			}
		})
	}
}
