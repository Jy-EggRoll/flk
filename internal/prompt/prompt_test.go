package prompt

import (
	"errors"
	"testing"
)

// withStdinTerminal 注入确定的“stdin 是否终端”结论，并在用例结束后恢复，
// 使测试不依赖真实运行环境（从终端执行 go test 时 os.Stdin 可能确实是终端）
func withStdinTerminal(t *testing.T, isTerminal bool) {
	t.Helper()
	previous := stdinIsTerminal
	stdinIsTerminal = func() bool { return isTerminal }
	t.Cleanup(func() {
		stdinIsTerminal = previous
		Configure(false)
	})
}

// TestConfirmAssumeYesBypassesTerminal 验证 --yes 模式下即便没有终端也直接同意，不再触碰交互
func TestConfirmAssumeYesBypassesTerminal(t *testing.T) {
	Configure(true)
	withStdinTerminal(t, false)

	confirmed, err := Confirm("确认吗", false)
	if err != nil {
		t.Fatalf("--yes 模式不应返回错误: %v", err)
	}
	if !confirmed {
		t.Fatal("--yes 模式应返回同意")
	}
}

// TestConfirmNonInteractiveReturnsSentinel 验证未启用 --yes 且非终端时返回哨兵错误而非阻塞
func TestConfirmNonInteractiveReturnsSentinel(t *testing.T) {
	Configure(false)
	withStdinTerminal(t, false)

	confirmed, err := Confirm("确认吗", true)
	if confirmed {
		t.Fatal("不可交互时不应返回同意")
	}
	if !errors.Is(err, ErrNonInteractive) {
		t.Fatalf("错误 = %v，期望包装 %v", err, ErrNonInteractive)
	}
}

// TestInteractiveReflectsAssumeYesAndTerminal 验证 Interactive 同时受 --yes 与终端状态影响
func TestInteractiveReflectsAssumeYesAndTerminal(t *testing.T) {
	withStdinTerminal(t, true)
	Configure(false)
	if !Interactive() {
		t.Fatal("终端且未启用 --yes 时应可交互")
	}

	Configure(true)
	if Interactive() {
		t.Fatal("启用 --yes 后不应再认为需要交互")
	}

	Configure(false)
	withStdinTerminal(t, false)
	if Interactive() {
		t.Fatal("非终端时不应认为可交互")
	}
}
