package safeop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/jy-eggroll/flk/internal/trash"
	"github.com/pterm/pterm"
)

// ErrOperationCancelled 表示用户看过删除计划后主动拒绝执行，调用方可据此区分取消与实际失败
var ErrOperationCancelled = errors.New("操作已取消")

// ConfirmFunc 抽象删除确认动作，便于命令行交互和测试分别提供实现
type ConfirmFunc func() (bool, error)

// RemoveOptions 控制删除计划展示、确认方式以及是否跳过确认
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
}

// PlanRemove 计算将被整体移入回收站的路径列表，不执行任何文件系统变更
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

// RemoveWithConfirm 先把稳定排序后的删除计划完整写入 Output，再按选项确认并把目标移入回收站
// 输出失败会立即返回且不会询问确认或触碰目标，确认取消及回收站处理逻辑保持原有语义
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

	// 必须在确认和回收站操作之前完成计划输出；若 writer 失败，传播原始错误并保证尚未产生文件系统副作用
	if err := printDeletePlan(out, paths); err != nil {
		return nil, err
	}

	if !opts.Force {
		confirm := opts.Confirm
		if confirm == nil {
			if opts.ConfirmMessage != "" {
				msg := opts.ConfirmMessage
				def := opts.ConfirmDefault
				confirm = func() (bool, error) {
					return pterm.DefaultInteractiveConfirm.WithDefaultValue(def).Show(msg)
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

	// 移入回收站替代真实删除，所有数据都可恢复，无需安全校验
	if err := trash.MoveToTrash(path); err != nil {
		return nil, err
	}
	return paths, nil
}

// printDeletePlan 把标题和每一条路径写入任意 writer，并在首次写失败时立即返回该错误
// 标准输出沿用 pterm 的警告和红色样式；缓冲区、文件、管道等非终端 writer 使用无 ANSI 控制符的相同文案
func printDeletePlan(out io.Writer, paths []string) error {
	const heading = "以下位置会在执行过程中被移至回收站:"

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

// defaultConfirm 使用原有的否定默认值和确认文案，避免未显式传入 Confirm 时改变交互安全边界
func defaultConfirm() (bool, error) {
	return pterm.DefaultInteractiveConfirm.WithDefaultValue(false).Show("您是否确认")
}
