package updater

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// recordingReporter 是一个可断言的 Reporter 实现，并可控制确认点的结果
// 升级器与终端完全解耦，使得整条升级链路都能在无终端环境中被驱动与观测
type recordingReporter struct {
	mu sync.Mutex

	infos      []string
	warnings   []string
	successes  []string
	questions  []string
	progresses []*recordingProgress

	// events 按发生顺序记录各类调用，用于断言输出时序
	// 例如汇总信息必须出现在进度结束之后，否则两段文字会挤在同一行
	events []string

	// confirmAnswer 与 confirmErr 模拟用户在确认点的选择，以及询问本身失败的情形
	confirmAnswer bool
	confirmErr    error
}

// newRecordingReporter 返回一个默认同意所有确认请求的记录器
func newRecordingReporter() *recordingReporter {
	return &recordingReporter{confirmAnswer: true}
}

func (r *recordingReporter) Info(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.infos = append(r.infos, fmt.Sprintf(format, args...))
	r.events = append(r.events, "info:"+fmt.Sprintf(format, args...))
}

func (r *recordingReporter) Warn(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
	r.events = append(r.events, "warn:"+fmt.Sprintf(format, args...))
}

func (r *recordingReporter) Success(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.successes = append(r.successes, fmt.Sprintf(format, args...))
	r.events = append(r.events, "success:"+fmt.Sprintf(format, args...))
}

// record 追加一条事件，供进度对象在被结束时回调
func (r *recordingReporter) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

// recordedEvents 返回事件序列副本
func (r *recordingReporter) recordedEvents() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

// indexOfEvent 返回第一个包含指定子串的事件下标，未找到时返回 -1
func (r *recordingReporter) indexOfEvent(substring string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index, event := range r.events {
		if strings.Contains(event, substring) {
			return index
		}
	}
	return -1
}

// sawWarning 判断是否输出过包含指定子串的警告
func (r *recordingReporter) sawWarning(substring string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, warning := range r.warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}

func (r *recordingReporter) Confirm(question string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.questions = append(r.questions, question)
	return r.confirmAnswer, r.confirmErr
}

func (r *recordingReporter) Progress(label string) Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	progress := &recordingProgress{reporter: r}
	r.progresses = append(r.progresses, progress)
	r.events = append(r.events, "progress-start:"+label)
	return progress
}

// askedQuestions 返回被询问过的问题副本，避免调用方在无锁状态下读取内部切片
func (r *recordingReporter) askedQuestions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.questions...)
}

// sawInfo 判断是否输出过包含指定子串的过程信息
func (r *recordingReporter) sawInfo(substring string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, info := range r.infos {
		if strings.Contains(info, substring) {
			return true
		}
	}
	return false
}

// firstProgress 返回最近一次开启的进度对象，未开启过时返回 nil
func (r *recordingReporter) firstProgress() *recordingProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progresses) == 0 {
		return nil
	}
	return r.progresses[0]
}

// recordingProgress 只统计被汇报的进度与结束调用，不参与任何渲染
type recordingProgress struct {
	reporter *recordingReporter
	updates  atomic.Int64
	done     atomic.Bool
}

func (p *recordingProgress) Update(int64, int64) { p.updates.Add(1) }

func (p *recordingProgress) Done() {
	p.done.Store(true)
	p.reporter.record("progress-end")
}
