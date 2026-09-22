package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jy-eggroll/flk/internal/updater"
	"github.com/pterm/pterm"
	"golang.org/x/term"
)

// ptermReporter 是升级器与终端之间的唯一适配层，用 pterm 实现全部用户可见输出与交互
// 所有内容都写入 writer（命令的 stderr），保证 stdout 只承载业务数据；
// 进度绘制与确认弹窗共享同一个暂停标志，弹窗出现时进度刷新自动让位，
// 这取代了早期用包级全局变量跨 goroutine 协调两套 UI 的做法
type ptermReporter struct {
	writer io.Writer

	// paused 在交互确认期间置位，使仍在下载 goroutine 中运行的进度刷新停止绘制
	// 必须用原子类型：读写分别发生在下载 goroutine 与命令主 goroutine 上
	paused atomic.Bool
}

// Info 输出过程性状态，统一带 pterm 的 Info 前缀以便与业务数据区分
func (r *ptermReporter) Info(format string, args ...any) {
	pterm.Info.WithWriter(r.writer).Printfln(format, args...)
}

// Warn 输出需要用户知晓但不阻断流程的问题
func (r *ptermReporter) Warn(format string, args ...any) {
	pterm.Warning.WithWriter(r.writer).Printfln(format, args...)
}

// Success 输出成功结论
func (r *ptermReporter) Success(format string, args ...any) {
	pterm.Success.WithWriter(r.writer).Printfln(format, args...)
}

// Confirm 在弹出确认前后暂停进度绘制
// 暂停在弹出前完成、恢复在返回后执行，保证问题文本不会被进度条的 \r 覆盖
func (r *ptermReporter) Confirm(question string) (bool, error) {
	// 交互组件在非终端输入下会一直等待按键而永不返回，必须先显式识别并拒绝，
	// 让升级器走"用户未能确认"的保守分支，而不是把命令永久挂起在等待输入上
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, errors.New("当前环境无法与用户交互（标准输入不是终端），请改用 --force 跳过确认")
	}

	r.paused.Store(true)
	defer r.paused.Store(false)

	// 交互组件沿用根生命周期通过 pterm.SetDefaultOutput 设置的 stderr，
	// 与 reporter 自身的 writer 指向同一处，因此无需再次指定输出目标
	return pterm.DefaultInteractiveConfirm.WithDefaultValue(true).Show(question)
}

// Progress 开启一次下载进度展示
func (r *ptermReporter) Progress(label string) updater.Progress {
	return &ptermProgress{reporter: r, label: label}
}

// progressRefreshInterval 是进度重绘的最小间隔
// 每收到一块数据都重绘会让终端闪烁，也会让速率与剩余时间的读数跳动到无法阅读
const progressRefreshInterval = 100 * time.Millisecond

// ptermProgress 用就地覆盖的单行文本展示下载进度、已传输量与剩余时间
// 这里没有使用 pterm 自带的进度条组件：它会自行接管光标与刷新节奏，
// 与确认弹窗同时出现时会争夺同一片终端区域，而此处的绘制完全由 reporter 的暂停标志统一调度
type ptermProgress struct {
	reporter *ptermReporter
	label    string

	// drawn 记录是否真正绘制过：总长未知时不会绘制，结束时也就无需补换行
	drawn atomic.Bool

	// mu 串行化节流字段的读写：直连 goroutine 在被取消的瞬间仍可能刷新一次进度，
	// 而主流程此时可能已在启动新的下载
	mu          sync.Mutex
	startedAt   time.Time
	lastDrawnAt time.Time
}

// Update 汇报下载进度，总长未知或正处于暂停期时直接忽略
func (p *ptermProgress) Update(done, total int64) {
	if total <= 0 || p.reporter.paused.Load() {
		return
	}

	now := time.Now()

	p.mu.Lock()
	if p.startedAt.IsZero() {
		p.startedAt = now
	}
	// 传输完成时必须强制绘制一次：否则受节流影响进度会停在 99% 之类的数字上，
	// 用户会以为下载没有收尾
	if done < total && now.Sub(p.lastDrawnAt) < progressRefreshInterval {
		p.mu.Unlock()
		return
	}
	p.lastDrawnAt = now
	startedAt := p.startedAt
	p.mu.Unlock()

	line := fmt.Sprintf("\r%s: %.1f%% (%s/%s)",
		p.label, float64(done)/float64(total)*100, updater.FormatSize(done), updater.FormatSize(total))

	// 仅在未完成时给出速率与剩余时间，完成时这两项已无意义
	if done < total {
		// 速率取整体平均值而非瞬时值：瞬时值在抖动网络下会让剩余时间剧烈跳动
		if elapsed := now.Sub(startedAt); elapsed > 0 {
			if speed := float64(done) / elapsed.Seconds(); speed > 0 {
				remaining := time.Duration(float64(total-done) / speed * float64(time.Second))
				line += fmt.Sprintf("  %s/s  剩余 %s", updater.FormatSize(int64(speed)), updater.FormatDuration(remaining))
			}
		}
	}

	_, _ = fmt.Fprint(p.reporter.writer, line)
	p.drawn.Store(true)
}

// Done 在绘制过进度后补一个换行，使后续输出从新行开始
func (p *ptermProgress) Done() {
	if p.drawn.Load() {
		_, _ = fmt.Fprintln(p.reporter.writer)
	}
}
