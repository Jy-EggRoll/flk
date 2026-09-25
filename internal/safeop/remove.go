package safeop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/jy-eggroll/flk/internal/prompt"
	"github.com/jy-eggroll/flk/internal/trash"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
)

// ErrOperationCancelled 表示用户看过删除计划后主动拒绝执行，调用方可据此区分取消与实际失败
var ErrOperationCancelled = errors.New("operation cancelled")

// ConfirmFunc 抽象删除确认动作，便于命令行交互和测试分别提供实现
type ConfirmFunc func() (bool, error)

// RemoveOptions 控制删除计划展示、确认方式、删除策略以及是否跳过确认
type RemoveOptions struct {
	// Force 为 true 时展示计划后直接执行，不调用 Confirm
	Force bool
	// Output 接收完整删除计划；nil 明确表示使用 os.Stdout，以保持未注入 writer 时的交互行为
	Output io.Writer
	// Confirm 在非强制模式下执行确认；nil 时使用 pterm 的默认交互确认
	Confirm ConfirmFunc
	// ConfirmMessage 非空时替换默认确认文案，并配合 ConfirmDefault 设置默认选择
	ConfirmMessage string
	// ConfirmDefault 仅在使用自定义 ConfirmMessage 的内置交互确认时生效
	ConfirmDefault bool
	// NoTrash 为 true 时走真实删除而非移入回收站，对应全局 --no-trash
	//
	// 这是本包唯一会永久销毁数据的开关：为 false（默认）时删除只是把目标移入 FLK 回收站，
	// 所有数据都可恢复，因此无需路径安全校验；为 true 时数据不可恢复，删除计划文案、
	// 危险路径高亮和调用方的确认提示都会随之切换到「真实删除」语义
	NoTrash bool
}

// PlanRemove 计算将被整体删除的路径列表，不执行任何文件系统变更
// 普通文件和符号链接只包含自身；真实目录包含整棵目录树，返回值按路径字典序排列以保证展示稳定
func PlanRemove(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return []string{absPath}, nil
	}

	var paths []string
	err = filepath.WalkDir(absPath, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		paths = append(paths, current)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 保证输出稳定，便于测试与用户阅读
	sort.Strings(paths)
	return paths, nil
}

// RemoveWithConfirm 先把稳定排序后的删除计划完整写入 Output，再按选项确认并按策略删除目标
// 策略由 RemoveOptions.NoTrash 决定：默认移入回收站，置位后真实删除
// 输出失败会立即返回且不会询问确认或触碰目标，确认取消及删除策略语义保持原有语义
func RemoveWithConfirm(path string, opts RemoveOptions) ([]string, error) {
	paths, err := PlanRemove(path)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}

	// Output 为 nil 时显式回退到标准输出，避免默认调用方丢失删除计划，同时允许测试和上层命令注入任意 writer
	out := opts.Output
	if out == nil {
		out = os.Stdout
	}

	// 必须在确认和实际删除之前完成计划输出；若 writer 失败，传播原始错误并保证尚未产生文件系统副作用
	if err := printDeletePlan(out, paths, opts.NoTrash); err != nil {
		return nil, err
	}

	if !opts.Force {
		confirm := opts.Confirm
		if confirm == nil {
			if opts.ConfirmMessage != "" {
				msg := opts.ConfirmMessage
				def := opts.ConfirmDefault
				confirm = func() (bool, error) {
					// 统一经 prompt.Confirm 处理 --yes 与非终端：前者直接同意，后者报错而非挂起
					return prompt.Confirm(msg, def)
				}
			} else {
				confirm = defaultConfirm
			}
		}
		ok, err := confirm()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrOperationCancelled
		}
	}

	// 默认把目标移入回收站（假删除，所有数据都可恢复）；NoTrash 置位时才真实删除
	if err := Delete(path, opts.NoTrash); err != nil {
		return nil, err
	}
	return paths, nil
}

// Delete 是本项目唯一的「路径删除实现」，统一决策「假删除」与「真实删除」两条路径
//
// 设计意图（为什么收口到这里）：
//
//	此前回收站调用分散在 safeop 与 cmd/unlink 两处，各自直接调用 trash.MoveToTrash，
//	一旦要引入第二条删除策略，就必须在每个调用点重复判断，容易漏改并造成行为不一致。
//	现在所有需要删除文件的代码（create 系列的覆盖、fix 的重建、unlink 的解除）都必须
//	经由此函数，删除策略只有一个所有者。
//
// 参数语义：
//   - noTrash 为 false（默认）：移入 FLK 回收站，可恢复，与历史行为完全一致
//   - noTrash 为 true：真实删除，不可恢复；目录连同整棵子树一并删除，符号链接只删除链接本身
//     而不会跟随到目标（os.RemoveAll 对符号链接的处理正是如此）
//
// 容错语义：目标不存在时直接返回 nil，让调用方无需再判断 os.IsNotExist。
// trash.MoveToTrash 与 os.RemoveAll 对不存在路径的错误形态在 Windows 上并不统一，
// 由本函数统一吸收可以避免同一段调用方代码在不同平台表现不一致
func Delete(path string, noTrash bool) error {
	// 统一的不存在判断放在策略之前，保证两条路径的容错行为绝对一致
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	// 默认策略：假删除，可恢复
	if !noTrash {
		return trash.MoveToTrash(path)
	}

	// 真实删除策略：os.RemoveAll 对文件、目录树、符号链接均适用，
	// 且对符号链接只删除链接自身，不会误删其指向的真实数据
	return os.RemoveAll(path)
}

// printDeletePlan 把标题和每一条路径写入任意 writer，并在首次写失败时立即返回该错误
// 标准输出沿用 pterm 的警告和红色样式；缓冲区、文件、管道等非终端 writer 使用无 ANSI 控制符的相同文案
// noTrash 决定文案语义：真实删除必须明确写「删除」并高亮为危险操作，不能再说「移入回收站」
func printDeletePlan(out io.Writer, paths []string, noTrash bool) error {
	heading := deletePlanHeading(noTrash)

	// 先断言为 *os.File 再比较指针，避免直接比较含不可比较动态类型的 io.Writer 接口而触发 panic，确保任意 writer 都可使用
	stdout, isStdout := out.(*os.File)
	if isStdout && stdout == os.Stdout {
		// PrefixPrinter.Println 会忽略底层写错误，因此先生成原样式文本，再由 io.WriteString 显式写入并检查错误
		if _, err := io.WriteString(out, pterm.Warning.Sprintln(heading)); err != nil {
			return err
		}
		for _, path := range paths {
			if _, err := fmt.Fprintln(out, pterm.Red(path)); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := fmt.Fprintln(out, heading); err != nil {
		return err
	}
	for _, path := range paths {
		if _, err := fmt.Fprintln(out, path); err != nil {
			return err
		}
	}
	return nil
}

// deletePlanHeading 按删除策略返回计划标题
// 抽成独立函数是为了让「真实删除」与「移入回收站」两种语义的文案只在唯一处定义，
// 避免以后新增调用点时两套文案逐渐跑偏
func deletePlanHeading(noTrash bool) string {
	if noTrash {
		return l10n.T("The following locations will be permanently deleted:", nil)
	}
	return l10n.T("The following locations will be moved to the trash:", nil)
}

// defaultConfirm 使用原有的否定默认值和确认文案，避免未显式传入 Confirm 时改变交互安全边界
// 与自定义文案分支一样经 prompt.Confirm，从而同样受 --yes 与非终端检测约束
func defaultConfirm() (bool, error) {
	return prompt.Confirm(l10n.T("Are you sure?", nil), false)
}
