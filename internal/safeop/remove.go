package safeop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/prompt"
	"github.com/jy-eggroll/flk/internal/trash"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
)

// ErrOperationCancelled 表示用户看过删除计划后主动拒绝执行，调用方可据此区分取消与实际失败
var ErrOperationCancelled = errors.New("operation cancelled")

// ErrProtectedPath 表示目标属于「受保护路径」——根目录本身或用户家目录本身，操作被围栏拦下
//
// 与 ErrOperationCancelled 一样，它表示一次「有意的安全拒绝」而不是执行失败，调用方用 errors.Is 判定。
// 之所以做成哨兵而不是让调用方比较错误字符串：文案要能随语言变化、随措辞调整，
// 而判定逻辑必须永远稳定
var ErrProtectedPath = errors.New("protected path")

// protectedPathError 承载被拒绝的具体路径，并把面向用户的文案交给 l10n
//
// 之所以不写成 fmt.Errorf("%w: %s", ErrProtectedPath, 文案)：err.Error() 会被命令层的
// failure() 原样呈现给用户，再拼一层哨兵描述就会出现「protected path: Refusing to ...」
// 这种多余的英文前缀，与项目「所有用户可见文案都走 l10n」的要求不符
type protectedPathError struct {
	// path 是归一化后的绝对路径，用于让错误信息指向唯一、可辨认的目标
	path string
}

func (e *protectedPathError) Error() string {
	// 文案刻意用「operate on」而不是「remove」：删除口与备份口复用同一个错误形态，
	// 而备份是复制/移动语义，用 remove 会在备份场景下误导用户
	return l10n.T("Refusing to operate on the protected path {{.Path}}: it is the root directory or the user home directory", map[string]any{"Path": e.path})
}

// Is 让 errors.Is(err, ErrProtectedPath) 成立
// 判定用的哨兵与展示用的文案因此互不耦合：以后调整措辞或新增语言都不会影响任何调用方的判定
func (e *protectedPathError) Is(target error) bool {
	return target == ErrProtectedPath
}

// NewProtectedPathError 构造「受保护路径」错误，供删除口之外的调用方（如备份口）复用同一形态：
// 既能被 errors.Is(err, ErrProtectedPath) 识别，文案也统一由本类型走 l10n，不出现第二份格式化逻辑
//
// 展示用的路径在这里统一归一化，调用方直接传入用户给的原始值即可：
// 这样 "~/." 之类的写法不会原样出现在错误信息里，也避免每个调用方各写一遍 Abs/Clean
func NewProtectedPathError(path string) error {
	display := path
	// 取不到绝对路径时回退到原路径：调用方既然已经判定命中受保护路径，
	// 说明 IsProtectedPath 内部成功取到过绝对路径（取不到会直接放行），因此这里不会影响拦截结论
	if abs, err := filepath.Abs(path); err == nil {
		display = filepath.Clean(abs)
	}
	return &protectedPathError{path: display}
}

// validateRemovable 是删除侧唯一的路径围栏：目标为根目录本身或用户家目录本身时拒绝删除
//
// 判断口径完全委托给 pathutil.IsProtectedPath（路径语义的归属地），本函数只负责把结论包装成
// 可被 errors.Is(ErrProtectedPath) 识别的错误，避免同一个围栏在删除口和备份口各写一份
//
// 刻意不在这里做符号链接展开：os.RemoveAll 对符号链接只删除链接自身，删掉一个指向根目录的
// 链接本身无害，展开反而会误伤（详见 Delete 的注释）
func validateRemovable(path string) error {
	if !pathutil.IsProtectedPath(path) {
		return nil
	}
	return NewProtectedPathError(path)
}

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
	// 数据可恢复，删除计划文案、危险路径高亮和调用方的确认提示都会随之切换到「移入回收站」语义；
	// 为 true 时数据不可恢复，上述文案切换到「真实删除」语义。
	//
	// 注意可恢复性不能替代围栏：默认策略把一个目录整体移入回收站，对根目录或家目录而言同样意味着
	// 「它从原位瞬间消失」，因此两种策略都必须先过 validateRemovable，围栏与 NoTrash 无关
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
//
// 围栏先于一切：目标命中受保护路径时立即返回 ErrProtectedPath，发生在生成删除计划、
// 写出计划与询问确认之前（详见 validateRemovable）
func RemoveWithConfirm(path string, opts RemoveOptions) ([]string, error) {
	// 必须在最前面拦截：否则用户会先看到「将删除 /home/user」这样一份惊悚且不可能执行的计划，
	// 再被告知拒绝，既误导用户，也让人怀疑围栏是否真的生效
	if err := validateRemovable(path); err != nil {
		return nil, err
	}

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
//
// 安全围栏：目标为根目录本身或用户家目录本身时一律拒绝（ErrProtectedPath），且与 noTrash 无关。
// 本函数是项目唯一的执行口，unlink 等调用方会直接调用它而不经过 RemoveWithConfirm，
// 因此这里必须自己拦一次；围栏刻意放在存在性检查之前——受保护路径「即使不存在也拒绝」
// 语义更简单，也让「拒绝」这件事明确地不依赖任何文件系统状态
func Delete(path string, noTrash bool) error {
	if err := validateRemovable(path); err != nil {
		return err
	}

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
