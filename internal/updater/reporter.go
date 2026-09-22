package updater

// Reporter 是升级器与用户之间的唯一界面
// 升级器内部不引用任何终端库，所有状态展示、进度绘制与交互确认都通过该接口上抛，
// 由宿主程序决定用彩色终端、纯文本还是完全静默的方式来呈现，
// 这也让升级逻辑可以在无终端的测试环境中被完整驱动
type Reporter interface {
	// Info 展示过程性状态，例如当前阶段、下载模式与版本对照
	Info(format string, args ...any)

	// Warn 展示需要用户知晓但不应中断流程的问题
	// 与返回错误区分开：警告意味着流程仍会继续，用户只是需要了解信息可能不完整
	Warn(format string, args ...any)

	// Success 展示最终的成功结论
	Success(format string, args ...any)

	// Confirm 提出一个问题并等待用户决定，返回 false 表示用户拒绝
	// 返回错误意味着无法完成询问（如终端不可交互），调用方应据此走保守分支而不是默认同意
	Confirm(question string) (bool, error)

	// Progress 开启一次进度展示，返回值用于汇报进度
	// 调用方必须在使用完毕后调用 Done 结束，实现应保证 Done 之后不再有输出残留
	Progress(label string) Progress
}

// Progress 表示一次可绘制进度的任务
// 与 Reporter 分离是为了让下载层只关心已传输的字节数，
// 而不承担换行、清除、暂停恢复等任何渲染细节
type Progress interface {
	// Update 汇报当前进度，total 小于等于 0 表示总长未知，此时实现可以只记录而不绘制百分比
	Update(done, total int64)

	// Done 结束本次进度展示
	Done()
}

// DiscardReporter 返回一个完全静默的 Reporter
// 面向无人值守场景（后台服务、定时任务、CI）：不产生任何输出，且在确认点一律同意，
// 使升级流程无需人工介入即可走完；需要用户知情或需要用户裁决的场景不应使用它
func DiscardReporter() Reporter {
	return discardReporter{}
}

type discardReporter struct{}

func (discardReporter) Info(string, ...any) {}

func (discardReporter) Warn(string, ...any) {}

func (discardReporter) Success(string, ...any) {}

func (discardReporter) Confirm(string) (bool, error) { return true, nil }

func (discardReporter) Progress(string) Progress { return discardProgress{} }

type discardProgress struct{}

func (discardProgress) Update(int64, int64) {}

func (discardProgress) Done() {}
