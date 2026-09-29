package hardlink

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
)

// 本文件为 internal/create/hardlink 的单元测试，覆盖 Create 的既有行为边界以及 Windows 跨卷预检
// 硬链接的关键语义是「副路径与主路径是同一份 inode」，所有成功用例都以 os.SameFile 作为最终判据
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
func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("创建测试文件 %q 失败: %v", path, err)
	}
}

// mustAbs 取绝对路径，失败直接终止用例，断言与实现的路径口径保持一致
func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("解析 %q 的绝对路径失败: %v", path, err)
	}
	return abs
}

// assertSameFile 断言两个路径指向同一份 inode，这是硬链接创建成功的唯一可靠判据
// 只比对内容是不够的：一份普通复制的内容也与主路径相同，但硬链接必须是同一份数据
func assertSameFile(t *testing.T, primPath, secoPath string) {
	t.Helper()

	primInfo, err := os.Stat(primPath)
	if err != nil {
		t.Fatalf("读取主路径 %q 信息失败: %v", primPath, err)
	}
	secoInfo, err := os.Stat(secoPath)
	if err != nil {
		t.Fatalf("读取副路径 %q 信息失败: %v", secoPath, err)
	}
	if !os.SameFile(primInfo, secoInfo) {
		t.Fatalf("主路径 %q 与副路径 %q 不是同一份 inode，os.SameFile = false，期望 true", primPath, secoPath)
	}
}

// requireHardlinkSupport 在临时目录内探测当前环境能否创建硬链接
// 某些文件系统（如部分网络盘、FAT）不支持硬链接，此时跳过依赖建链的用例，
// 而不是把环境限制误报成实现缺陷；只依赖错误返回的用例不受影响
func requireHardlinkSupport(t *testing.T) {
	t.Helper()
	probeDir := t.TempDir()
	probePrim := filepath.Join(probeDir, "probe-prim.txt")
	mustWriteFile(t, probePrim, "probe")
	if err := os.Link(probePrim, filepath.Join(probeDir, "probe-seco.txt")); err != nil {
		t.Skipf("当前环境无法创建硬链接，跳过依赖建链的用例: %v", err)
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

// TestCreateReturnsErrorWhenPrimPathMissing 固化「主路径不存在时直接上抛 os.Stat 错误」
// 防的回归：主路径不存在却先删了副路径——用户的既有文件凭空消失，硬链接又建不出来
func TestCreateReturnsErrorWhenPrimPathMissing(t *testing.T) {
	base := t.TempDir()
	primPath := filepath.Join(base, "missing-prim.txt")
	secoPath := filepath.Join(base, "seco.txt")

	var buffer bytes.Buffer
	err := Create(primPath, secoPath, forceRemoveOpts(&buffer))
	if err == nil {
		t.Fatal("主路径不存在时应返回错误，实际返回 nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("返回错误 = %v，期望可被 errors.Is(err, os.ErrNotExist) 识别", err)
	}

	if _, statErr := os.Lstat(secoPath); !os.IsNotExist(statErr) {
		t.Fatalf("主路径不存在时不应创建副路径，Lstat(%q) 错误 = %v，期望 os.ErrNotExist", secoPath, statErr)
	}
	if buffer.Len() != 0 {
		t.Fatalf("主路径不存在时不应产生删除计划，实际输出: %q", buffer.String())
	}
}

// TestCreateCreatesHardlink 固化「副路径不存在时建链成功」，以 os.SameFile 为最终判据
// 副路径刻意放在还不存在的多级子目录下，顺带覆盖 pathutil.EnsureDirExists 的 MkdirAll 分支
// 防的回归：把硬链接退化成复制（内容对了却不是同一份 inode），后续任何一方写入都不会同步到另一方
func TestCreateCreatesHardlink(t *testing.T) {
	requireHardlinkSupport(t)

	base := t.TempDir()
	primPath := filepath.Join(base, "prim.txt")
	mustWriteFile(t, primPath, "同一份 inode")

	secoPath := filepath.Join(base, "nested", "deep", "seco.txt")

	var buffer bytes.Buffer
	if err := Create(primPath, secoPath, forceRemoveOpts(&buffer)); err != nil {
		t.Fatalf("创建硬链接失败: %v", err)
	}

	assertSameFile(t, primPath, secoPath)

	// 硬链接的语义证据：从副路径写入，主路径必须立刻看到同一份数据
	if err := os.WriteFile(secoPath, []byte("副路径写入"), 0o644); err != nil {
		t.Fatalf("向副路径写入失败: %v", err)
	}
	content, err := os.ReadFile(primPath)
	if err != nil {
		t.Fatalf("读取主路径失败: %v", err)
	}
	if string(content) != "副路径写入" {
		t.Fatalf("通过副路径写入后主路径内容 = %q，期望 %q", string(content), "副路径写入")
	}
}

// TestCreateReplacesExistingTargetWithForce 固化 Force 分支：旧副路径被删除、新硬链接建立
// 防的回归：旧文件没被删掉，os.Link 直接报 EEXIST，--force 形同虚设
func TestCreateReplacesExistingTargetWithForce(t *testing.T) {
	requireHardlinkSupport(t)

	base := t.TempDir()
	primPath := filepath.Join(base, "prim.txt")
	mustWriteFile(t, primPath, "主路径内容")

	secoPath := filepath.Join(base, "seco.txt")
	mustWriteFile(t, secoPath, "旧的副路径内容")

	var buffer bytes.Buffer
	if err := Create(primPath, secoPath, forceRemoveOpts(&buffer)); err != nil {
		t.Fatalf("Force 覆盖已存在的副路径失败: %v", err)
	}

	assertSameFile(t, primPath, secoPath)

	content, err := os.ReadFile(secoPath)
	if err != nil {
		t.Fatalf("读取覆盖后的副路径失败: %v", err)
	}
	if string(content) != "主路径内容" {
		t.Fatalf("覆盖后副路径内容 = %q，期望 %q", string(content), "主路径内容")
	}

	// Force 只跳过确认，删除计划本身仍必须写出到调用方注入的 writer
	absSeco := mustAbs(t, secoPath)
	if !strings.Contains(buffer.String(), absSeco) {
		t.Fatalf("删除计划未写入注入的 writer，实际输出: %q，期望包含 %q", buffer.String(), absSeco)
	}
}

// TestCreateCancellationKeepsOldTarget 固化副路径已存在且用户拒绝确认时的语义
// 注入恒返回 false 的 Confirm 并保持 Force: false，完全避开终端交互，结论稳定可复现
// 防的回归：把「用户拒绝」当成「删除失败后继续建链」，旧目标被保留却仍尝试建链并报 EEXIST，
// 用户看到的是莫名其妙的失败，而不是明确的「已取消」
func TestCreateCancellationKeepsOldTarget(t *testing.T) {
	requireHardlinkSupport(t)

	base := t.TempDir()
	primPath := filepath.Join(base, "prim.txt")
	mustWriteFile(t, primPath, "主路径内容")

	secoPath := filepath.Join(base, "seco.txt")
	mustWriteFile(t, secoPath, "旧的副路径内容")

	var buffer bytes.Buffer
	err := Create(primPath, secoPath, safeop.RemoveOptions{
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

	content, readErr := os.ReadFile(secoPath)
	if readErr != nil {
		t.Fatalf("取消后旧目标应原样保留，读取失败: %v", readErr)
	}
	if string(content) != "旧的副路径内容" {
		t.Fatalf("取消后旧目标内容 = %q，期望 %q", string(content), "旧的副路径内容")
	}

	primInfo, err := os.Stat(primPath)
	if err != nil {
		t.Fatalf("读取主路径信息失败: %v", err)
	}
	secoInfo, err := os.Stat(secoPath)
	if err != nil {
		t.Fatalf("读取副路径信息失败: %v", err)
	}
	if os.SameFile(primInfo, secoInfo) {
		t.Fatal("取消后不应建立硬链接，主副路径仍是同一份 inode")
	}
}

// TestCreateRebuildsWhenParentIsFile 固化 pathutil.ExistsButNotDirectoryError 分支：
// 副路径的父路径是一个普通文件时，先删除该文件、重建中间目录，再建链
// 防的回归：父路径是文件时直接把 ExistsButNotDirectoryError 上抛——用户明明给了 --force，
// 却得到一个无法自助解决的错误，还得手动去删那个挡路的文件
func TestCreateRebuildsWhenParentIsFile(t *testing.T) {
	requireHardlinkSupport(t)

	base := t.TempDir()
	primPath := filepath.Join(base, "prim.txt")
	mustWriteFile(t, primPath, "主路径内容")

	// 挡路的普通文件：它同时是副路径的父路径
	blocker := filepath.Join(base, "blocker")
	mustWriteFile(t, blocker, "挡路的普通文件")

	secoPath := filepath.Join(blocker, "child.txt")

	var buffer bytes.Buffer
	if err := Create(primPath, secoPath, forceRemoveOpts(&buffer)); err != nil {
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

	parentInfo, err := os.Stat(filepath.Dir(secoPath))
	if err != nil {
		t.Fatalf("重建后的中间目录应存在: %v", err)
	}
	if !parentInfo.IsDir() {
		t.Fatalf("中间路径 %q 应为目录，实际模式 = %v", filepath.Dir(secoPath), parentInfo.Mode())
	}

	assertSameFile(t, primPath, secoPath)
}

// TestCreateRejectsProtectedSecoPath 回归保护「受保护路径围栏」：
// 副路径为当前用户家目录本身时必须返回可被 errors.Is(err, safeop.ErrProtectedPath) 识别的错误
//
// 两重保险（红线：绝不允许真的删除或移动家目录）：
//   - 前置断言先用 pathutil.IsProtectedPath 确认用例路径确实命中围栏口径，未命中就直接失败退出，
//     不进入被测函数——被测函数对同一字符串调用的是同一个函数，因此这是忠实的前置条件
//   - RemoveOptions.Confirm 恒返回 false，即使围栏失效，最坏结果也只是「用户取消」而不会删除家目录
//
// 防的回归：覆盖已存在的副路径时绕过围栏，把家目录整体移入回收站或真实删除——对用户而言等同于家目录瞬间消失
func TestCreateRejectsProtectedSecoPath(t *testing.T) {
	base := t.TempDir()

	// 主路径必须真实存在，否则实现会在 os.Stat 处提前返回，永远走不到围栏
	primPath := filepath.Join(base, "prim.txt")
	mustWriteFile(t, primPath, "主路径内容")

	for _, tc := range protectedPathCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			if !pathutil.IsProtectedPath(tc.path) {
				t.Fatalf("前置断言失败：pathutil.IsProtectedPath(%q) = false，拒绝执行 Create 以免造成不可逆破坏", tc.path)
			}

			var buffer bytes.Buffer
			confirmCalled := false
			err := Create(primPath, tc.path, safeop.RemoveOptions{
				Force:   false,
				NoTrash: true,
				Output:  &buffer,
				Confirm: func() (bool, error) {
					confirmCalled = true
					return false, nil
				},
			})

			if !errors.Is(err, safeop.ErrProtectedPath) {
				t.Fatalf("Create(%q, %q) 返回错误 = %v，期望 %v", primPath, tc.path, err, safeop.ErrProtectedPath)
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

			if _, statErr := os.Stat(primPath); statErr != nil {
				t.Fatalf("主路径不应被围栏用例影响: %v", statErr)
			}
		})
	}
}

// TestCreateRejectsCrossVolumeHardlinkOnWindows 只在 Windows 上固化跨卷预检
// hardlink 不能跨卷，提前报错的意义是：避免 --force 先把副路径删掉、再由 os.Link 失败，
// 造成用户既有文件凭空消失——预检必须发生在 os.Lstat(secoPath) 即删除计划之前
//
// 非 Windows 平台该分支（runtime.GOOS == "windows"）不可达，按红线直接跳过，绝不伪造平台
func TestCreateRejectsCrossVolumeHardlinkOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("跨卷预检只在 Windows 的 runtime.GOOS == \"windows\" 分支生效")
	}

	primPath := filepath.Join(t.TempDir(), "prim.txt")
	mustWriteFile(t, primPath, "主路径内容")

	primVol := strings.ToUpper(filepath.VolumeName(primPath))
	if primVol == "" {
		t.Skipf("无法从临时路径 %q 推导盘符，跳过", primPath)
	}

	// 构造一个与主路径不同盘符的副路径：判定只看 VolumeName 字符串，因此目标盘无需真实存在
	secoVol := "Z:"
	if primVol == secoVol {
		secoVol = "Y:"
	}
	secoPath := secoVol + string(os.PathSeparator) + "cross-volume.txt"

	var buffer bytes.Buffer
	err := Create(primPath, secoPath, forceRemoveOpts(&buffer))
	if err == nil {
		t.Fatalf("跨盘副路径 %q（主路径盘符 %q）应返回错误，实际返回 nil", secoPath, primVol)
	}
	if errors.Is(err, safeop.ErrProtectedPath) || errors.Is(err, safeop.ErrOperationCancelled) {
		t.Fatalf("跨盘应返回明确的业务错误，实际 = %v", err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("跨盘预检发生在删除之前，不应打印删除计划，实际输出: %q", buffer.String())
	}
	if _, statErr := os.Lstat(primPath); statErr != nil {
		t.Fatalf("主路径不应被跨盘预检影响: %v", statErr)
	}
}
