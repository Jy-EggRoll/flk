package shared

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
)

// protectedBackupPath 是备份口围栏的受保护路径用例
type protectedBackupPath struct {
	name string
	path string
}

// protectedBackupPaths 构造当前平台上的受保护路径用例（根目录本身与家目录的几种等价写法）
// 这些路径只用于验证「函数在触碰文件系统之前就拒绝」，测试绝不对它们做任何复制、删除或移动
func protectedBackupPaths(t *testing.T) []protectedBackupPath {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}
	home = filepath.Clean(home)
	sep := string(os.PathSeparator)

	// 根目录现场推导（Unix 为 "/"，Windows 为 "C:\" 之类的盘符根），不硬编码任何平台路径
	root := filepath.VolumeName(home) + sep

	return []protectedBackupPath{
		{"根目录", root},
		{"家目录", home},
		{"家目录带尾部斜杠", home + sep},
	}
}

// TestHandleTargetBackupRejectsProtectedPaths 验证备份口在 os.Stat 与备份确认之前就拦下受保护路径，
// 且对 SourcePath 与 TargetPath 双方生效
//
// 为什么要拦双方：备份是把 TargetPath 复制到 SourcePath。source 位于 target 内部时会自引用无限复制
// （-r 指向家目录内的仓库、-f 指向家目录时会把磁盘撑满），把家目录当 source 同样荒谬；
// 统一规则是「/ 与 ~ 本身不得作为 flk 的操作对象（主路径或副路径）」
//
// 测试的安全设计（红线：绝不对 / 或家目录执行任何复制、删除、移动）：
//   - 前置断言先用 pathutil.IsProtectedPath 确认用例路径确实命中围栏口径，未命中就直接失败退出，
//     不进入被测函数——被测函数对同一字符串调用的是同一个函数，因此这个前置断言是忠实的前置条件
//   - 非受保护的另一侧一律指向临时目录里不存在的路径：受保护的是 source 时，target 不存在会让函数
//     即使在围栏失效的情况下也走「无需备份」的提前返回；受保护的是 target 时 target 必然存在，
//     此时函数会停在交互确认上——测试进程的标准输入不是终端，prompt.Confirm 直接报错而不会执行复制
func TestHandleTargetBackupRejectsProtectedPaths(t *testing.T) {
	cases := protectedBackupPaths(t)
	base := t.TempDir()

	for _, tc := range cases {
		for _, field := range []string{"SourcePath", "TargetPath"} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, field), func(t *testing.T) {
				if !pathutil.IsProtectedPath(tc.path) {
					t.Fatalf("前置断言失败：pathutil.IsProtectedPath(%q) = false，拒绝执行 HandleTargetBackup 以免造成破坏", tc.path)
				}

				// 非受保护的一侧指向不存在的临时路径：target 不存在时函数必然提前返回，不产生任何副作用
				opts := BackupOptions{
					SourceLabel: "real",
					TargetLabel: "fake",
				}
				if field == "SourcePath" {
					opts.SourcePath = tc.path
					opts.TargetPath = filepath.Join(base, "missing-target")
				} else {
					opts.SourcePath = filepath.Join(base, "missing-source")
					opts.TargetPath = tc.path
				}

				result, err := HandleTargetBackup(opts)
				if !errors.Is(err, safeop.ErrProtectedPath) {
					t.Fatalf("返回错误 = %v，期望 %v", err, safeop.ErrProtectedPath)
				}
				if result.BackedUp {
					t.Fatal("受保护路径不应执行任何备份")
				}
				if err.Error() == "" {
					t.Fatal("受保护路径错误必须带有可展示的文案")
				}

				// 拦截必须发生在任何文件系统操作之前：受保护路径必须原封不动
				if _, statErr := os.Lstat(tc.path); statErr != nil {
					t.Fatalf("受保护路径 %q 被复制、删除或移动: %v", tc.path, statErr)
				}
			})
		}
	}
}

// TestHandleTargetBackupAllowsOrdinaryPaths 验证围栏不误伤合法用法：
// 普通临时目录与家目录的子项都不是「受保护路径本身」，应照旧走原有分支
// 两侧都指向不存在的路径，因此用例只覆盖「提前返回」这一无副作用分支
func TestHandleTargetBackupAllowsOrdinaryPaths(t *testing.T) {
	base := t.TempDir()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}
	// 家目录的子项刻意选择一个几乎不可能存在、且绝不创建的名字，保证函数只走到 os.Stat 的提前返回
	homeChild := filepath.Join(home, ".flk-backup-fence-test-missing")

	allowed := []struct {
		name   string
		source string
		target string
	}{
		{"普通临时目录", filepath.Join(base, "real"), filepath.Join(base, "fake")},
		{"家目录的子项", homeChild, homeChild + "-target"},
	}

	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			result, err := HandleTargetBackup(BackupOptions{
				SourcePath:  tc.source,
				TargetPath:  tc.target,
				SourceLabel: "real",
				TargetLabel: "fake",
			})
			if err != nil {
				t.Fatalf("合法路径不应被围栏拦截: %v", err)
			}
			if result.BackedUp {
				t.Fatal("target 不存在时不应执行备份")
			}
		})
	}
}

// writeProbe 在临时目录内的指定路径写入探针文件，用于事后核对「未发生文件系统变更」
// 所有探针一律落在 t.TempDir 之内，绝不触碰真实家目录、根目录或任何真实用户数据
func writeProbe(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建探针父目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入探针 %q 失败: %v", path, err)
	}
}

// readProbe 读取探针文件内容，用于核对文件未被截断、清空或覆盖
func readProbe(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取探针 %q 失败（可能已被复制、删除或移动）: %v", path, err)
	}
	return string(content)
}

// TestHandleTargetBackupRejectsSourceInsideTarget 验证「source 位于 target 内部」时备份被拒绝，
// 且拒绝发生在触碰文件系统之前
//
// 这是本次修复的核心灾难点：pathutil.Copy(target -> source) 会让源目录在复制过程中不断长大
// （把 target 的内容复制进 target 内部的 source，而 source 又是 target 子树的一部分），
// 形成自引用无限递归直到磁盘写满。典型触发是 flk create symlink -r ~/repo/sub -f ~/repo
//
// 测试的安全设计（红线：只在 t.TempDir 内构造数据）：
//   - target 与其内部的 source 全部位于临时目录，且用例只验证「拒绝」而不是真去复制
//   - 探针文件用于证明拒绝是无副作用的：target 内的探针内容不变、且没有被复制进 source、
//     source 自身也没有被创建或改写
func TestHandleTargetBackupRejectsSourceInsideTarget(t *testing.T) {
	base := t.TempDir()
	sep := string(os.PathSeparator)

	cases := []struct {
		name string
		// sourceOf 依据该子用例的 target 推导 source，ret 为 true 时预先创建 source 与其探针
		sourceOf func(target string) string
		makeSrc  bool
	}{
		{"直接子目录（source 不存在）", func(target string) string {
			return filepath.Join(target, "sub")
		}, false},
		{"深层子目录（source 不存在）", func(target string) string {
			return filepath.Join(target, "a", "b", "sub")
		}, false},
		{"已存在的子目录", func(target string) string {
			return filepath.Join(target, "sub")
		}, true},
		// 刻意用字符串拼接而不是 filepath.Join：Join 会顺手 Clean 掉 ".."，
		// 这里要验证的是「写法上绕一圈也逃不掉归一化」，必须把非最简形态原样交给被测函数
		{"带 .. 绕行仍位于内部", func(target string) string {
			return target + sep + "sub" + sep + ".." + sep + "sub"
		}, false},
		{"带 ./ 与尾部分隔符", func(target string) string {
			return target + sep + "." + sep + "sub" + sep
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 每个子用例独立的 target，避免前一个用例的探针影响后一个的判断
			target := filepath.Join(base, strings.ReplaceAll(tc.name, "/", "_"), "fake")
			source := tc.sourceOf(target)
			if err := os.MkdirAll(target, 0755); err != nil {
				t.Fatalf("创建 target 失败: %v", err)
			}
			writeProbe(t, filepath.Join(target, "probe.txt"), "target-data")
			if tc.makeSrc {
				writeProbe(t, filepath.Join(source, "own.txt"), "source-data")
			}

			result, err := HandleTargetBackup(BackupOptions{
				SourcePath:  source,
				TargetPath:  target,
				Smart:       true, // 不进入交互确认，让错误只可能来自围栏
				Force:       true,
				SourceLabel: "real",
				TargetLabel: "fake",
				Output:      io.Discard,
			})

			// 哨兵已上移到 pathutil：备份口与复制层守卫表达的是同一件事
			// （备份调用的就是 pathutil.Copy(target, source)，拦下的是同一对路径的同一个谓词），
			// 因此断言的对象是 pathutil.ErrCopyPathOverlap，不再是已删除的 shared.ErrBackupPathOverlap
			if !errors.Is(err, pathutil.ErrCopyPathOverlap) {
				t.Fatalf("返回错误 = %v，期望 %v", err, pathutil.ErrCopyPathOverlap)
			}
			// 语义不同就不复用哨兵：这是「两个路径的关系非法」而不是「路径本身受保护」
			if errors.Is(err, safeop.ErrProtectedPath) {
				t.Fatalf("路径重叠错误不应被识别为 ErrProtectedPath: %v", err)
			}
			if result.BackedUp {
				t.Fatal("路径重叠时不应执行任何备份")
			}
			if err.Error() == "" {
				t.Fatal("路径重叠错误必须带有可展示的文案")
			}

			// 无副作用的核对：target 探针未被截断或覆盖，且没有被复制进 source
			if got := readProbe(t, filepath.Join(target, "probe.txt")); got != "target-data" {
				t.Fatalf("target 探针被改写: got %q, want %q", got, "target-data")
			}
			if _, statErr := os.Lstat(filepath.Join(source, "probe.txt")); statErr == nil {
				t.Fatalf("target 的内容被复制进了 source：%s", filepath.Join(source, "probe.txt"))
			}
			if !tc.makeSrc {
				if _, statErr := os.Lstat(source); statErr == nil {
					t.Fatalf("source 不应被创建: %s", source)
				}
			} else if got := readProbe(t, filepath.Join(source, "own.txt")); got != "source-data" {
				t.Fatalf("source 探针被改写: got %q, want %q", got, "source-data")
			}
		})
	}
}

// TestHandleTargetBackupRejectsSamePath 验证 source 与 target 为同一路径时备份被拒绝
//
// 为什么要一并拦：同一条路径既作主又作副时，pathutil.CopyFile 的 os.Create(dst) 会先把该文件截断，
// 再从同一个文件读取并写回，结果是内容被清零（自读自写毁坏数据）；目录场景则等价于
// 「把目录复制进它自己」。这种用法几乎不可能有合法含义，属同类危险
//
// 用例覆盖几种等价写法（尾部斜杠、./、.. 绕行），证明归一化没有被绕过；
// 探针文件用于证明拒绝时文件内容分毫未动
func TestHandleTargetBackupRejectsSamePath(t *testing.T) {
	base := t.TempDir()
	sep := string(os.PathSeparator)

	dir := filepath.Join(base, "same-dir")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	writeProbe(t, filepath.Join(dir, "probe.txt"), "keep-me")

	file := filepath.Join(base, "same-file.txt")
	writeProbe(t, file, "keep-me-too")

	missing := filepath.Join(base, "missing-same")

	cases := []struct {
		name   string
		source string
		target string
		probe  string // 事后核对的探针路径，为空表示无需核对（目标本就不存在）
	}{
		{"同一目录", dir, dir, filepath.Join(dir, "probe.txt")},
		{"同一目录（尾部分隔符）", dir + sep, dir, filepath.Join(dir, "probe.txt")},
		{"同一目录（./ 形式）", filepath.Join(dir, "."), dir, filepath.Join(dir, "probe.txt")},
		// 非最简写法，验证归一化确实生效
		{"同一目录（.. 绕行形式）", dir + sep + "sub" + sep + "..", dir, filepath.Join(dir, "probe.txt")},
		{"同一文件", file, file, file},
		{"同一文件（.. 绕行形式）", base + sep + "same-file.txt" + sep + ".." + sep + "same-file.txt", file, file},
		// 路径尚不存在也拒绝：与受保护路径围栏同一口径，「拒绝」不依赖任何文件系统状态
		{"同一不存在路径", missing, missing, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := HandleTargetBackup(BackupOptions{
				SourcePath:  tc.source,
				TargetPath:  tc.target,
				Smart:       true,
				Force:       true,
				SourceLabel: "real",
				TargetLabel: "fake",
				Output:      io.Discard,
			})

			// 哨兵已上移到 pathutil（旧 shared.ErrBackupPathOverlap 已删除），断言对象随之更新
			if !errors.Is(err, pathutil.ErrCopyPathOverlap) {
				t.Fatalf("返回错误 = %v，期望 %v", err, pathutil.ErrCopyPathOverlap)
			}
			if errors.Is(err, safeop.ErrProtectedPath) {
				t.Fatalf("同一路径错误不应被识别为 ErrProtectedPath: %v", err)
			}
			if result.BackedUp {
				t.Fatal("同一路径时不应执行任何备份")
			}
			if tc.probe != "" {
				if got := readProbe(t, tc.probe); got != "keep-me" && got != "keep-me-too" {
					t.Fatalf("探针内容被改写（自读自写毁坏数据的形态）: got %q", got)
				}
			}
		})
	}
}

// TestHandleTargetBackupBacksUpUnrelatedPaths 验证围栏不误伤合法用法：
// source 与 target 是彼此无关的两个临时路径时，备份照常执行并真的把内容复制过去
// 之前的 TestHandleTargetBackupAllowsOrdinaryPaths 只覆盖「target 不存在」的提前返回分支，
// 本用例补上真正走完 pathutil.Copy 的正向分支，防止围栏把正常备份一起挡掉
func TestHandleTargetBackupBacksUpUnrelatedPaths(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "real")
	target := filepath.Join(base, "fake")
	writeProbe(t, filepath.Join(target, "a.txt"), "A")

	result, err := HandleTargetBackup(BackupOptions{
		SourcePath:  source,
		TargetPath:  target,
		Smart:       true, // 自动同意备份，避免依赖交互
		Force:       true,
		SourceLabel: "real",
		TargetLabel: "fake",
		Output:      io.Discard,
	})
	if err != nil {
		t.Fatalf("无关路径的备份不应失败: %v", err)
	}
	if !result.BackedUp {
		t.Fatal("无关路径且 target 存在时应执行备份")
	}
	if got := readProbe(t, filepath.Join(source, "a.txt")); got != "A" {
		t.Fatalf("备份内容不正确: got %q, want %q", got, "A")
	}
}

// TestHandleTargetBackupAllowsTargetInsideSource 验证围栏的方向性：
// 只有「source 在 target 内部」才是自引用灾难，「target 在 source 内部」是把较小的目录并入较大的路径，
// 属于完全合法的备份用法，必须照常执行
//
// 这条用例是防止把 IsSubPath 的父子参数写反的回归保护：参数写反会让所有
// 「-f 指向 -r 的子目录」的正常用法全部被拒
func TestHandleTargetBackupAllowsTargetInsideSource(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "real")
	target := filepath.Join(source, "fake")
	writeProbe(t, filepath.Join(target, "b.txt"), "B")

	result, err := HandleTargetBackup(BackupOptions{
		SourcePath:  source,
		TargetPath:  target,
		Smart:       true,
		Force:       true,
		SourceLabel: "real",
		TargetLabel: "fake",
		Output:      io.Discard,
	})
	if err != nil {
		t.Fatalf("target 位于 source 内部是合法用法，不应被拦截: %v", err)
	}
	if !result.BackedUp {
		t.Fatal("target 位于 source 内部时应执行备份")
	}
	// 复制确实发生：target 的内容被并入 source，且 target 自身的内容未被破坏
	if got := readProbe(t, filepath.Join(source, "b.txt")); got != "B" {
		t.Fatalf("备份内容不正确: got %q, want %q", got, "B")
	}
	if got := readProbe(t, filepath.Join(target, "b.txt")); got != "B" {
		t.Fatalf("target 内容被破坏: got %q, want %q", got, "B")
	}
}
