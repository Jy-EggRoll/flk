package output

import "testing"

// TestTruncateString_Short 短字符串原样返回
func TestTruncateString_Short(t *testing.T) {
	if got := truncateString("abc", 10); got != "abc" {
		t.Fatalf("短字符串应原样返回: got %s", got)
	}
}

// TestTruncateString_Long 长字符串被截断并带省略号
func TestTruncateString_Long(t *testing.T) {
	got := truncateString("abcdefghij", 5)
	if got != "ab..." {
		t.Fatalf("截断不符预期: got %s", got)
	}
}

// TestTruncateString_TinyMaxLen 当 maxLen 小于 3 时不应越界 panic，且原样返回
func TestTruncateString_TinyMaxLen(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("truncateString 在 maxLen<3 时不应 panic: %v", r)
		}
	}()
	if got := truncateString("abcdef", 2); got != "abcdef" {
		t.Fatalf("maxLen<3 应原样返回: got %s", got)
	}
	if got := truncateString("abcdef", 0); got != "abcdef" {
		t.Fatalf("maxLen=0 应原样返回: got %s", got)
	}
}

// TestCalcColWidth_Minimum 终端极窄时列宽应有下限，避免负数宽度引发截断越界
func TestCalcColWidth_Minimum(t *testing.T) {
	if w := calcColWidth(10); w < 10 {
		t.Fatalf("calcColWidth 下限应为 10，实际: %d", w)
	}
	if w := calcColWidth(0); w < 10 {
		t.Fatalf("calcColWidth(0) 下限应为 10，实际: %d", w)
	}
	if w := calcColWidth(200); w <= 0 {
		t.Fatalf("calcColWidth(200) 应为正数，实际: %d", w)
	}
}
