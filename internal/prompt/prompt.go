// Package prompt 统一 flk 的交互确认入口，并提供全局非交互模式。
//
// 背景（为什么需要本包）：
//
//	此前各处的确认直接调用 pterm.DefaultInteractiveConfirm，而 pterm 会打开 /dev/tty
//	读取按键。当 flk 被脚本、CI、编辑器任务或其它非交互进程调用时，并不存在控制终端，
//	进程会永久阻塞在等待输入上（表现为“卡死”），而且因为读的是 /dev/tty 而不是 stdin，
//	连 `yes | flk ...`、`printf 'y\n' | flk ...` 这类管道喂答案也无法解开。
//
// 为解决该问题，本项目把所有确认收敛到本包，并引入全局 --yes 开关，统一语义为：
//   - 启用 --yes：任何确认都直接返回“同意”，完全不触碰终端，供自动化使用
//   - 未启用 --yes 且 stdin 不是终端：立即返回错误并提示改用 --yes，绝不阻塞
//   - 其余情况：才走 pterm 的真实交互，defaultValue 作为直接回车时的默认选择
//
// 为何用 stdin 而不是 /dev/tty 作为“是否可交互”的判据：脚本重定向 stdin 是最常见的
// 非交互信号，且升级器既有实现就采用该判据，保持一致可避免同一环境下两套结论互相打架。
// 调用方无需自行判断环境，直接调用 Confirm 即可得到“要么确定答案、要么明确错误”的结果。
package prompt

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
	"golang.org/x/term"
)

// assumeYes 记录是否处于全局 --yes 非交互模式。
// 由根生命周期在命令执行前写入，读取点可能位于下载等其它 goroutine，
// 因此使用原子布尔而非普通 bool，避免数据竞争
var assumeYes atomic.Bool

// stdinIsTerminal 是“标准输入是否为终端”的判定实现。
// 抽成包级变量是为了让测试注入确定结论，从而在无终端环境下稳定覆盖两条分支
var stdinIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// ErrNonInteractive 标识“因环境不可交互且未启用 --yes 而无法完成确认”。
// 单独作为哨兵错误，便于调用方用 errors.Is 区分“环境问题”与“用户取消”“业务失败”
var ErrNonInteractive = errors.New("flk: confirmation requires an interactive terminal or --yes")

// Configure 设置全局是否处于 --yes 非交互模式，由根生命周期统一调用。
// 必须在任何业务命令执行前完成，之后本包状态只读，可被并发读取
func Configure(yes bool) {
	assumeYes.Store(yes)
}

// AssumeYes 返回当前是否启用了 --yes。
// 供 fix/unlink 这类需要在进入交互循环之前就决定“是否批量执行”的命令使用
func AssumeYes() bool {
	return assumeYes.Load()
}

// Interactive 判断当前是否仍需要且能够与用户交互。
// 只有在未启用 --yes 且 stdin 是终端时才为真；fix/unlink 用它决定是进入交互循环，
// 还是直接以 --all 的方式批量处理，或以明确错误拒绝而非挂起
func Interactive() bool {
	return !assumeYes.Load() && stdinIsTerminal()
}

// Confirm 是全局统一的确认入口，返回用户（或 --yes）的决定。
//
// 语义优先级：
//  1. --yes：直接返回 true，不读取任何输入
//  2. 非终端：返回包装了 ErrNonInteractive 的错误，文案提示改用 --yes，绝不阻塞
//  3. 终端：调用 pterm 交互确认，defaultValue 为直接回车的默认值
//
// 这里不把非终端情况当作“取消”：取消是用户看过后主动拒绝，属于正常业务分支；
// 而环境不具备交互能力属于配置错误，必须让调用方以失败结束并给出可操作的建议
func Confirm(question string, defaultValue bool) (bool, error) {
	if assumeYes.Load() {
		return true, nil
	}
	if !stdinIsTerminal() {
		return false, fmt.Errorf("%s: %w",
			l10n.T("Cannot interact with the user (stdin is not a terminal); rerun with --yes to assume yes", nil),
			ErrNonInteractive,
		)
	}
	return pterm.DefaultInteractiveConfirm.WithDefaultValue(defaultValue).Show(question)
}
