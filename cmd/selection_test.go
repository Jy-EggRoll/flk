package cmd

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/jy-eggroll/flk/internal/output"
)

// 本文件覆盖组4 从 fix/unlink 抽取出来的两个纯函数：
//   - parseSelectionIndices：交互编号解析（含非法项跳过与警告）
//   - filterCheckResults：按有效性过滤检查结果
//
// 约定（与 cmd/record_test.go、cmd/copy_overlap_guard_test.go 同一套）：
//   - 两个函数都是纯函数，不读全局 store、不落盘、不产生文件系统副作用，
//     因此这里不需要 t.TempDir()，也不需要备份/还原任何全局状态
//   - 唯一的「外部状态」是 l10n 的渲染能力：它是进程级全局，是否已 Init 取决于同包其它用例
//     （copy_overlap_guard_test.go 会 Init），本文件刻意不依赖渲染结果，断言口径见 selectionWarningCount
//   - 警告输出写进局部 bytes.Buffer，绝不落到真实 stdout/stderr，避免污染测试输出

// selectionWarningCount 统计缓冲区里 "Invalid number" 出现的次数，即「打印了几条非法编号警告」
//
// 为什么可以用固定片段计数：l10n.T 在 Init 未被调用时按设计直接返回源串，
// 此时整条消息是未渲染的 "Invalid number {{.Part}}"；Init 之后是 "Invalid number abc"，
// 两种情况下前缀片段 "Invalid number" 都必然原样出现，因此计数不受渲染状态影响，也就不会出现
// 「用例结果取决于同包其它用例是否先调用了 l10n.Init」这种顺序依赖
//
// 为什么不断言渲染后的具体编号文本：那会强制本文件先调用 l10n.Init（进程级全局），
// 把测试的成败绑到 l10n 的初始化时机上，而这里真正要守住的是「非法项的条数与跳过行为」，
// 编号原文由共享函数用 map[string]any{"Part": part} 原样透传，重构前后未做任何改动
func selectionWarningCount(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "Invalid number")
}

// TestParseSelectionIndices 锁定编号解析的全部边界
// 三处调用点（fix 的 d 删除分支、fix 的普通修复分支、unlink 的解除分支）共用本函数，
// 因此这里的每条用例同时代表三个命令的行为，且必须与重构前三处内联循环逐字等价：
// 非法项（非数字 / 0 / 负数 / 超上限）只警告并跳过自身，合法项按输入顺序减一保留，
// 全部非法或空输入返回 nil（不是空切片）
func TestParseSelectionIndices(t *testing.T) {
	tests := []struct {
		name string
		// input 是已由调用方去掉命令前缀（如 `d`）的原始输入串
		input string
		// count 是当前可选集合的长度，等价于重构前的 len(invalidResults) / len(validResults)
		count        int
		want         []int
		wantWarnings int
	}{
		{
			name:  "正常多编号按输入顺序返回 0 基索引",
			input: "1 3 5",
			count: 5,
			want:  []int{0, 2, 4},
		},
		{
			name:  "单个编号",
			input: "2",
			count: 3,
			want:  []int{1},
		},
		{
			name:         "非数字整项被跳过并警告",
			input:        "abc",
			count:        3,
			want:         nil,
			wantWarnings: 1,
		},
		{
			name:         "0 越下界（用户编号从 1 开始）被跳过并警告",
			input:        "0",
			count:        3,
			want:         nil,
			wantWarnings: 1,
		},
		{
			name:         "负数被跳过并警告",
			input:        "-1",
			count:        3,
			want:         nil,
			wantWarnings: 1,
		},
		{
			name:         "超出上限被跳过并警告",
			input:        "4",
			count:        3,
			want:         nil,
			wantWarnings: 1,
		},
		{
			name:  "空输入不警告且返回 nil",
			input: "",
			count: 3,
			want:  nil,
		},
		{
			name:  "仅空白输入不警告且返回 nil",
			input: "   \t ",
			count: 3,
			want:  nil,
		},
		{
			name: "混合合法与非法：非法项各自警告，合法项按顺序保留",
			// 非法项共 3 个：abc（非数字）、0（下界）、4（超上限，count=3）
			input:        "1 abc 3 0 4",
			count:        3,
			want:         []int{0, 2},
			wantWarnings: 3,
		},
		{
			name:  "乱序输入保留输入顺序而不是排序",
			input: "3 1",
			count: 3,
			want:  []int{2, 0},
		},
		{
			name:  "多余空白不影响解析",
			input: "  1   2  ",
			count: 3,
			want:  []int{0, 1},
		},
		{
			name: "可选集合为空时任何编号都越界",
			// count=0 对应「交互循环里结果集已空」的边界：调用方在 len(indices)==0 时 continue，
			// 与重构前 idx > len(结果集) 的判定一致，不会出现越界下标
			input:        "1",
			count:        0,
			want:         nil,
			wantWarnings: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			got := parseSelectionIndices(tt.input, tt.count, &buf)

			// DeepEqual 同时固定内容、顺序与 nil 形态：全部非法时必须是 nil 而不是 []int{}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseSelectionIndices(%q, %d) = %#v, 期望 %#v", tt.input, tt.count, got, tt.want)
			}

			if n := selectionWarningCount(&buf); n != tt.wantWarnings {
				t.Fatalf("parseSelectionIndices(%q, %d) 打印了 %d 条非法编号警告, 期望 %d 条（输出=%q）",
					tt.input, tt.count, n, tt.wantWarnings, buf.String())
			}
		})
	}
}

// TestFilterCheckResults 锁定两个过滤方向与空结果的切片形态
// fix 的 checkAndDisplay 只保留无效记录（keepValid=false）、unlink 的对应闭包只保留有效记录（keepValid=true），
// 两处共用本函数；除了「保留哪些记录」，还必须保持「空结果是非 nil 空切片」这一形态，
// 否则 JSON 模式会从 [] 变成 null（详见函数注释）
func TestFilterCheckResults(t *testing.T) {
	// 用不同路径区分记录身份，便于断言过滤后保留的到底是哪几条
	validA := output.CheckResult{Type: "symlink", Device: "devA", Real: "~/a-real", Fake: "~/a-fake", Valid: true}
	invalidB := output.CheckResult{
		Type: "symlink", Device: "devB", Real: "~/b-real", Fake: "~/b-fake",
		Valid: false, Error: "link missing", ErrorType: "LINK_MISSING",
	}
	validC := output.CheckResult{Type: "copy", Device: "devC", Src: "~/c-src", Dst: "~/c-dst", Valid: true}

	t.Run("keepValid=false 只保留无效记录（fix 方向）且保持原有顺序", func(t *testing.T) {
		got := filterCheckResults([]output.CheckResult{validA, invalidB, validC}, false)
		if !reflect.DeepEqual(got, []output.CheckResult{invalidB}) {
			t.Fatalf("filterCheckResults(..., false) = %#v, 期望只保留 %#v", got, invalidB)
		}
	})

	t.Run("keepValid=true 只保留有效记录（unlink 方向）且保持原有顺序", func(t *testing.T) {
		got := filterCheckResults([]output.CheckResult{validA, invalidB, validC}, true)
		if !reflect.DeepEqual(got, []output.CheckResult{validA, validC}) {
			t.Fatalf("filterCheckResults(..., true) = %#v, 期望保留 %#v", got, []output.CheckResult{validA, validC})
		}
	})

	t.Run("空输入返回非 nil 空切片（JSON 输出 [] 而不是 null）", func(t *testing.T) {
		for _, keepValid := range []bool{true, false} {
			got := filterCheckResults(nil, keepValid)
			if got == nil {
				t.Fatalf("filterCheckResults(nil, %v) = nil, 期望非 nil 空切片（nil 会被 JSON 编码成 null）", keepValid)
			}
			if len(got) != 0 {
				t.Fatalf("filterCheckResults(nil, %v) 长度 = %d, 期望 0", keepValid, len(got))
			}
			// 与重构前 make([]output.CheckResult, 0) 的形态逐字对齐
			if !reflect.DeepEqual(got, []output.CheckResult{}) {
				t.Fatalf("filterCheckResults(nil, %v) = %#v, 期望 []output.CheckResult{}", keepValid, got)
			}
		}
	})

	t.Run("过滤方向相反但互不丢项：两个方向的并集等于原集合", func(t *testing.T) {
		all := []output.CheckResult{validA, invalidB, validC}
		keptValid := filterCheckResults(all, true)
		keptInvalid := filterCheckResults(all, false)
		if len(keptValid)+len(keptInvalid) != len(all) {
			t.Fatalf("两个方向保留数之和 = %d, 期望 %d（每条记录必须恰好落在一侧）",
				len(keptValid)+len(keptInvalid), len(all))
		}
	})
}
