package safeop

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// errRemoveWrite 是删除计划测试使用的固定写错误，便于 errors.Is 验证错误未被包装或吞掉
var errRemoveWrite = errors.New("删除计划写入失败")

// removeFailingWriter 模拟不可写的输出目标，任何删除计划内容都无法落盘
type removeFailingWriter struct{}

func (removeFailingWriter) Write([]byte) (int, error) {
	return 0, errRemoveWrite
}

// sliceWriter 的底层切片不可比较，用于验证实现不会通过 io.Writer 接口相等比较引发 panic
type sliceWriter []byte

func (sliceWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

// TestRemoveWithConfirmWritesSortedPlanToBuffer 验证任意 RemoveOptions.Output 都能收到标题和完整计划，且目录树顺序稳定
func TestRemoveWithConfirmWritesSortedPlanToBuffer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "target")
	for _, relativePath := range []string{
		filepath.Join("z-dir", "z.txt"),
		filepath.Join("a-dir", "b.txt"),
		filepath.Join("a-dir", "a.txt"),
	} {
		fullPath := filepath.Join(root, relativePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("创建测试目录失败: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(relativePath), 0o644); err != nil {
			t.Fatalf("创建测试文件失败: %v", err)
		}
	}

	expectedPaths, err := PlanRemove(root)
	if err != nil {
		t.Fatalf("生成预期删除计划失败: %v", err)
	}

	var buffer bytes.Buffer
	_, err = RemoveWithConfirm(root, RemoveOptions{
		Output: &buffer,
		Confirm: func() (bool, error) {
			return false, nil
		},
	})
	if !errors.Is(err, ErrOperationCancelled) {
		t.Fatalf("返回错误 = %v，期望 %v", err, ErrOperationCancelled)
	}

	lines := strings.Split(strings.TrimSuffix(buffer.String(), "\n"), "\n")
	if len(lines) == 0 || lines[0] != "以下位置会在执行过程中被移至回收站:" {
		t.Fatalf("删除计划标题异常: %q", buffer.String())
	}
	if got := lines[1:]; !reflect.DeepEqual(got, expectedPaths) {
		t.Fatalf("删除计划路径\n实际: %#v\n期望: %#v", got, expectedPaths)
	}
	if _, statErr := os.Stat(root); statErr != nil {
		t.Fatalf("取消后目标目录应保持不变: %v", statErr)
	}
}

// TestRemoveWithConfirmNilOutputUsesStdout 验证未设置 Output 时明确回退到当前 os.Stdout，而不是静默丢弃计划
func TestRemoveWithConfirmNilOutputUsesStdout(t *testing.T) {
	target := filepath.Join(t.TempDir(), "stdout.txt")
	if err := os.WriteFile(target, []byte("保留"), 0o644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建 stdout 捕获管道失败: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = originalStdout
		_ = reader.Close()
		_ = writer.Close()
	})

	_, err = RemoveWithConfirm(target, RemoveOptions{
		Confirm: func() (bool, error) {
			return false, nil
		},
	})
	if !errors.Is(err, ErrOperationCancelled) {
		t.Fatalf("返回错误 = %v，期望 %v", err, ErrOperationCancelled)
	}
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatalf("关闭 stdout 捕获写端失败: %v", closeErr)
	}
	captured, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatalf("读取 stdout 输出失败: %v", readErr)
	}
	if !strings.Contains(string(captured), "以下位置会在执行过程中被移至回收站:") || !strings.Contains(string(captured), target) {
		t.Fatalf("nil Output 未写入 stdout: %q", captured)
	}
}

// TestRemoveWithConfirmPropagatesWriterError 验证计划写失败时不进入确认，也不会把目标移入回收站
func TestRemoveWithConfirmPropagatesWriterError(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(target, []byte("保留"), 0o644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	confirmCalled := false
	paths, err := RemoveWithConfirm(target, RemoveOptions{
		Output: removeFailingWriter{},
		Confirm: func() (bool, error) {
			confirmCalled = true
			return true, nil
		},
	})
	if !errors.Is(err, errRemoveWrite) {
		t.Fatalf("返回错误 = %v，期望 %v", err, errRemoveWrite)
	}
	if paths != nil {
		t.Fatalf("写入失败时返回路径 = %#v，期望 nil", paths)
	}
	if confirmCalled {
		t.Fatal("删除计划尚未成功写出时不应询问确认")
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("写入失败后目标文件应保持不变: %v", statErr)
	}
}

// TestRemoveWithConfirmCancellation 验证拒绝确认仍返回既有取消错误，并且已展示计划但未移动目标
func TestRemoveWithConfirmCancellation(t *testing.T) {
	target := filepath.Join(t.TempDir(), "cancel.txt")
	if err := os.WriteFile(target, []byte("取消删除"), 0o644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	var buffer bytes.Buffer
	paths, err := RemoveWithConfirm(target, RemoveOptions{
		Output: &buffer,
		Confirm: func() (bool, error) {
			return false, nil
		},
	})
	if !errors.Is(err, ErrOperationCancelled) {
		t.Fatalf("返回错误 = %v，期望 %v", err, ErrOperationCancelled)
	}
	if paths != nil {
		t.Fatalf("取消时返回路径 = %#v，期望 nil", paths)
	}
	absoluteTarget, absErr := filepath.Abs(target)
	if absErr != nil {
		t.Fatalf("解析目标绝对路径失败: %v", absErr)
	}
	if !strings.Contains(buffer.String(), absoluteTarget) {
		t.Fatalf("取消前应已输出目标路径，实际输出: %q", buffer.String())
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("取消后目标文件应保持不变: %v", statErr)
	}
}

// TestPrintDeletePlanAcceptsNonComparableWriter 验证底层类型不可比较的合法 io.Writer 也能接收计划且不会 panic
func TestPrintDeletePlanAcceptsNonComparableWriter(t *testing.T) {
	if err := printDeletePlan(sliceWriter{}, []string{"/a"}); err != nil {
		t.Fatalf("不可比较 writer 输出失败: %v", err)
	}
}

// TestPrintDeletePlanPropagatesPartialWriterError 覆盖标题成功、路径失败的场景，确认循环中的写错误同样会返回
func TestPrintDeletePlanPropagatesPartialWriterError(t *testing.T) {
	writer := &failAfterOneWrite{err: errRemoveWrite}
	if err := printDeletePlan(writer, []string{"/a", "/b"}); !errors.Is(err, errRemoveWrite) {
		t.Fatalf("返回错误 = %v，期望 %v", err, errRemoveWrite)
	}
}

// failAfterOneWrite 允许标题写入成功，从第二次写入起失败，用于覆盖路径逐行输出分支
type failAfterOneWrite struct {
	writes int
	err    error
}

func (w *failAfterOneWrite) Write(data []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, w.err
	}
	return io.Discard.Write(data)
}
