package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/prompt"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

// unlink 命令：解除已建立的链接关系
//
// 语义说明（与 create/check/fix 的字段约定保持一致）：
//
//	real/prim/src 是「权威源」，fake/seco/dst 是「派生位置」。
//	本命令用权威源的「实际文件」替换派生位置上由 flk 创建的符号链接 / 硬链接 / 副本，
//	使派生位置成为一份独立的真实数据，从此与权威源不再存在链接关系，并从存储中移除该追踪记录。
//
// 三类记录的处理方式：
//   - symlink：fake 当前是指向 real 的符号链接。删除该符号链接，并把 real 的实际文件/目录
//     （若 real 自身还是符号链接，则跟随到其最终指向的真实目录）复制到 fake 位置
//   - hardlink：seco 与 prim 共享同一 inode。删除 seco 这个名字，再用 prim 的实际内容在
//     seco 位置复制出一份独立文件，使其不再与 prim 共享 inode
//   - copy：dst 本就是一份独立文件，不存在文件系统层面的链接，无需任何物理操作，
//     仅需移除追踪记录（该动作发生在物理替换之后，不在 unlinkFilesystem 内）
//
// 仅处理「当前有效」的记录：无效记录（链接已损坏/缺失）应交由 fix 命令处理，本命令不触碰
//
// --keep-record 模式：只做上述物理替换，不从配置文件中移除追踪记录。
// 解除后记录会因链接不复存在而被 check 判为「无效」，之后可用 fix 按原记录重建链接，
// 适用于「临时断开链接、稍后还想恢复」的场景。注意 copy 记录在此模式下没有任何可执行的动作
// （其唯一的动作就是移除记录），会被跳过并给出提示
var unlinkCmd = &cobra.Command{
	Use:     "unlink",
	Aliases: []string{"ul"},
	Short:   l10n.T("Remove link relationships, replacing links/copies with real files", nil),
	Long:    l10n.T("Replace the created symlinks, hard links, and copies with the real files from real/prim/src, remove the relationships, and drop the tracking records (only valid records are handled)", nil),
	RunE:    RunUnlink,
}

func init() {
	MarkNeedsStore(unlinkCmd)
	MarkSupportsJSON(unlinkCmd)
	rootCmd.AddCommand(unlinkCmd)
	unlinkCmd.Flags().StringVarP(&unlinkDevice, "device", "d", "", l10n.T("Device names to filter by, comma-separated", nil))
	unlinkCmd.Flags().BoolVar(&unlinkSymlink, "symlink", false, l10n.T("Process only symbolic links", nil))
	unlinkCmd.Flags().BoolVar(&unlinkHardlink, "hardlink", false, l10n.T("Process only hard links", nil))
	unlinkCmd.Flags().BoolVar(&unlinkCopy, "copy", false, l10n.T("Process only copies", nil))
	unlinkCmd.Flags().StringVar(&unlinkDir, "dir", "", l10n.T("Process only records containing this path", nil))
	unlinkCmd.Flags().BoolVar(&unlinkForce, "force", false, l10n.T("Skip the deletion confirmation and proceed directly", nil))
	unlinkCmd.Flags().BoolVar(&unlinkAll, "all", false, l10n.T("Automatically remove all valid links, skipping interactive mode", nil))
	unlinkCmd.Flags().BoolVar(&unlinkKeepRecord, "keep-record", false, l10n.T("Only remove the link relationship and keep the tracking record (the record becomes invalid; use fix to recreate the link)", nil))
}

var (
	unlinkDevice     string
	unlinkSymlink    bool
	unlinkHardlink   bool
	unlinkCopy       bool
	unlinkDir        string
	unlinkForce      bool
	unlinkAll        bool
	unlinkKeepRecord bool
)

// RunUnlink 执行解除链接流程：列出当前有效记录，交互或批量地将其还原为独立真实文件
// 结果集写 stdout，提示和逐项状态写 stderr；一批操作会尽可能全部执行，再聚合操作及存储落盘错误
func RunUnlink(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	format := output.OutputFormat(outputFormat)

	// 批量（--all/--yes）或显式 --force 都表示“不再逐项确认”
	// 这样 --all 才名副其实：不仅跳过选择循环，也不在逐项删除时再次弹确认，
	// 从而在非终端环境下也能完整跑完，而不是卡在逐项确认上
	skipConfirm := unlinkForce || unlinkAll || prompt.AssumeYes()

	// checkAndDisplay 复用 check 的检查逻辑并仅保留有效记录
	// JSON 空集必须输出 [] 供脚本稳定解析；表格模式继续显示原有的“没有可解除”提示
	checkAndDisplay := func() ([]output.CheckResult, error) {
		deviceFilters := parseDeviceFilters(unlinkDevice)
		results, err := performCheck(CheckOptions{
			DeviceFilters: deviceFilters,
			CheckSymlink:  unlinkSymlink,
			CheckHardlink: unlinkHardlink,
			CheckCopy:     unlinkCopy,
			CheckDir:      unlinkDir,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l10n.T("Check failed", nil), err)
		}

		// 结果过滤统一走 filterCheckResults：unlink 只保留有效记录（待解除项），
		// 过滤方向由 keepValid=true 表达；fix 的对应闭包复用同一份实现、只是方向相反
		validResults := filterCheckResults(results, true)

		if format == output.JSON || len(validResults) > 0 {
			if err := output.PrintCheckResults(out, format, validResults); err != nil {
				return nil, fmt.Errorf("%s: %w", l10n.T("Output failed", nil), err)
			}
		} else {
			pterm.Info.WithWriter(errOut).Println(l10n.T("There are no valid links to remove", nil))
		}

		return validResults, nil
	}

	validResults, err := checkAndDisplay()
	if err != nil {
		return err
	}
	if len(validResults) == 0 {
		return nil
	}

	// JSON 非批量模式只列出待解除项；--all 或 --yes 也只保留首次 JSON，后续状态进入 stderr
	if format == output.JSON && !unlinkAll && !prompt.AssumeYes() {
		return nil
	}

	var operationErrors []error
	unlinkSelected := func(indices []int) {
		for _, idx := range indices {
			result := validResults[idx]
			// 逐条解除的三段日志：开始（Debug）→ 成功（Info）或失败（Warn）
			// 与 fix 的 repairSelected 同构：循环这里就是「每条」的粒度，字段统一走 recordLogArgs（type/device/from,to）
			// 放在循环而不是 unlinkResult 内部，是因为 unlinkResult 有多个错误返回点，逐点补日志会把单条结果拆散
			logger.Debug(l10n.T("Removing the link relationship", nil), recordLogArgs(result)...)
			if err := unlinkResult(result, skipConfirm, errOut); err != nil {
				// 失败用 Warn 而不是 Error：批量解除时单条失败不中断其余记录，符合「用户需知道但可继续」的级别语义
				logger.Warn(l10n.T("Removal failed", nil), append(recordLogArgs(result), "error", err)...)
				pterm.Error.WithWriter(errOut).Println(l10n.T("Removal failed #{{.Index}}: {{.Err}}", map[string]any{"Index": idx + 1, "Err": err.Error()}))
				operationErrors = append(operationErrors, fmt.Errorf("%s: %w", l10n.T("Removal #{{.Index}} failed", map[string]any{"Index": idx + 1}), err))
			} else {
				logger.Info(l10n.T("Removed the link relationship", nil), recordLogArgs(result)...)
				pterm.Success.WithWriter(errOut).Println(l10n.T("Removed #{{.Index}}", map[string]any{"Index": idx + 1}))
			}
		}
	}
	saveStore := func() {
		// 落盘统一走共享的 saveTrackedStore（含全局实例判空），
		// 「Save failed」文案与 operationErrors 的收集仍留在本命令内，保持既有输出与退出码语义
		// --keep-record 模式下内存 store 未发生变化，此时落盘只是重写一份内容等价（仅排序）的文件，无害
		if err := saveTrackedStore(); err != nil {
			pterm.Error.WithWriter(errOut).Println(l10n.T("Save failed: {{.Err}}", map[string]any{"Err": err.Error()}))
			operationErrors = append(operationErrors, fmt.Errorf("%s: %w", l10n.T("Save failed", nil), err))
		}
	}

	// --all 或 --yes：批量解除所有有效链接，单项失败不阻断其余记录，结束后统一决定退出码
	// --yes 把“同意一切确认”自然延伸为“全部处理”，使其成为真正的非交互总开关
	if unlinkAll || prompt.AssumeYes() {
		pterm.Info.WithWriter(errOut).Println(l10n.T("Automatically removing all valid links...", nil))
		indices := make([]int, len(validResults))
		for idx := range validResults {
			indices[idx] = idx
		}
		unlinkSelected(indices)
		saveStore()
		return errors.Join(operationErrors...)
	}

	// 既非批量也未启用 --yes 时，只有真正可交互才能进入输入循环；
	// 否则明确失败并给出补救参数，避免在无终端环境下永久阻塞
	if !prompt.Interactive() {
		return fmt.Errorf("%s", l10n.T("Cannot interact with the user (stdin is not a terminal); rerun with --all or --yes", nil))
	}

	// 交互模式：输入编号解除对应项，all/a 全部解除，exit/q 主动退出仍视为成功
	for {
		pterm.DefaultBox.WithWriter(errOut).WithTitle("INFO").Println(pterm.Green(l10n.T("Enter all or a to remove everything\nEnter exit or q to quit\nEnter numbers to remove the corresponding items\nSeparate with spaces", nil)))
		input, err := pterm.DefaultInteractiveTextInput.WithMultiLine(false).Show(l10n.T("Enter input", nil))
		if err != nil {
			// EOF 等输入错误必须向上传播；若继续下一轮会反复得到同一错误并形成 CPU 空转
			operationErrors = append(operationErrors, fmt.Errorf("%s: %w", l10n.T("Input error", nil), err))
			return errors.Join(operationErrors...)
		}

		input = strings.TrimSpace(input)
		if input == "exit" || input == "q" {
			return errors.Join(operationErrors...)
		}

		var indices []int
		if input == "all" || input == "a" {
			for idx := range validResults {
				indices = append(indices, idx)
			}
		} else {
			// 编号解析统一走 parseSelectionIndices：与 fix 的普通修复分支共用同一份实现，
			// 非法项由共享函数打印警告、跳过，合法项按输入顺序转成 0 基索引
			indices = parseSelectionIndices(input, len(validResults), errOut)
		}

		if len(indices) == 0 {
			continue
		}

		unlinkSelected(indices)
		saveStore()

		validResults, err = checkAndDisplay()
		if err != nil {
			operationErrors = append(operationErrors, err)
			return errors.Join(operationErrors...)
		}
		if len(validResults) == 0 {
			return errors.Join(operationErrors...)
		}
	}
}

// unlinkResult 解除单条记录的链接关系
// 成功完成物理替换后，默认从全局存储中移除该记录；--keep-record 模式下保留记录，
// 仅解除文件系统层面的链接关系（记录随后会被 check 判为无效，可用 fix 按原记录重建链接）
// skipConfirm 为真时（来自 --all/--yes/--force）不再逐项确认，直接执行物理替换
// 注意：本函数只更新内存中的 store（走共享的 removeTrackedRecord），
// 落盘由调用方在一批操作后统一执行 saveTrackedStore，减少重复写盘
//
// 拆分说明（本次改造）：物理替换与清单记录删除被拆成两步，物理部分收在 unlinkFilesystem 里，
// 本函数只负责「替换成功后再移除记录」这个顺序。拆分的唯一动机是 WebUI——
// 网页端解除时这两件事必须落在不同的锁里：文件系统操作（可能复制整棵目录，耗时数秒）
// 不能持清单写锁，否则保存请求要排队、轮询重载也得停摆；而清单改动又必须在清单写锁内完成。
// 若两者仍耦合在同一个函数里，服务端就只能二选一，要么长时间持锁要么留下内存与磁盘的分叉
//
// noTrash 由本函数转交 unlinkFilesystem 并在那里生效，取值来自包级变量：
// CLI 的删除策略是全局 --no-trash 的一次性取值，与 WebUI 的「逐次选择」不同源
func unlinkResult(result output.CheckResult, skipConfirm bool, errorOutput ...io.Writer) error {
	if err := unlinkFilesystem(result, skipConfirm, noTrash, errorOutput...); err != nil {
		return err
	}

	// 物理替换完成后移除追踪记录（记录中的路径为折叠形式，result 字段直接来自存储，故可原样匹配）
	// --keep-record 模式下跳过移除，让记录留在配置文件中供后续 fix 重建
	//
	// 这里仍读包级 unlinkKeepRecord 而不把它做成参数：--keep-record 是 CLI 独有的模式，
	// 网页端不存在「解除后保留记录」的诉求（保留记录等于链接已被移除却仍宣称有链接，
	// 那条记录会立刻变成待修复项，语义自相矛盾），因此不为它扩散一个没有调用方的参数
	if !unlinkKeepRecord {
		removeTrackedRecord(result)
	}
	return nil
}

// unlinkFilesystem 只做解除链接的物理部分：把派生位置上的符号链接 / 硬链接 / 副本
// 替换成一份来自权威源的独立真实数据，全程不碰清单（store）
//
// 与 unlinkResult 的分工：本函数不读也不写 store，因此调用方可以在不持清单写锁的情况下调用它，
// 「是否顺带移除追踪记录」以及何时落盘完全由调用方决定（CLI 由 unlinkResult 决定，
// WebUI 由 /api/unlink 在文件系统操作完成后再进锁处理）
//
// noTrash 决定派生位置旧链接的删除策略，作为显式参数传入而不是读包级变量：
//   - CLI：传包级 noTrash（全局 --no-trash 的一次性取值）
//   - WebUI：传本次请求体里的 noTrash，用户可以逐次选择「本次是否真实删除」
//
// 三类记录的处理方式见文件头注释；仅处理「当前有效」的记录（无效记录应交由 fix 处理），
// 调用方负责先用 filterCheckResults(results, true) 过滤出有效项再传进来
//
// skipConfirm 为真时跳过交互确认；WebUI 传 true——网页上已经用确认对话框列过要解除哪一条，
// 服务端再确认一次会落到服务端 stdin（无人值守时直接报错），解除必然失败
func unlinkFilesystem(result output.CheckResult, skipConfirm, noTrash bool, errorOutput ...io.Writer) error {
	// 解除过程中的确认、警告和状态都属于交互诊断信息，默认写 stderr，并允许命令注入 Cobra 的错误输出 writer
	// 网页调用必须显式传入非 nil writer：nil 会回退到服务端终端，用户既看不到输出、终端还被污染
	errOut := io.Writer(os.Stderr)
	if len(errorOutput) > 0 && errorOutput[0] != nil {
		errOut = errorOutput[0]
	}

	switch result.Type {
	case "symlink":
		expandedReal, err := pathutil.NormalizePath(result.Real)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the source path", nil), err)
		}
		expandedFake, err := pathutil.NormalizePath(result.Fake)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the link path", nil), err)
		}
		if err := replaceWithReal(expandedReal, expandedFake, "real", "fake", skipConfirm, noTrash, errOut); err != nil {
			return err
		}
	case "hardlink":
		expandedPrim, err := pathutil.NormalizePath(result.Prim)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the primary file path", nil), err)
		}
		expandedSeco, err := pathutil.NormalizePath(result.Seco)
		if err != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to expand the secondary file path", nil), err)
		}
		if err := replaceWithReal(expandedPrim, expandedSeco, "prim", "seco", skipConfirm, noTrash, errOut); err != nil {
			return err
		}
	case "copy":
		// dst 本就是独立文件，不存在文件系统层面的链接，无需任何物理操作，记录的处理交给调用方
		// --keep-record 模式下连记录也保留，对 copy 而言没有任何可执行的动作，
		// 明确提示后按成功返回，避免用户误以为「解除成功」是做了什么实际变更
		if unlinkKeepRecord {
			pterm.Warning.WithWriter(errOut).Println(l10n.T("The copy record has no filesystem-level link to remove; skipped in --keep-record mode: {{.Dst}}", map[string]any{"Dst": result.Dst}))
			return nil
		}
	default:
		return fmt.Errorf("%s", l10n.T("Unknown type {{.Type}}", map[string]any{"Type": result.Type}))
	}

	// 物理替换到此结束，本函数不碰 store：记录的移除与落盘由调用方决定（见函数注释的分工说明）
	return nil
}

// replaceWithReal 用权威源的「实际文件」替换派生位置上的链接
// source：权威源（real/prim），derived：派生位置（fake/seco）
//
// 关键设计：
//   - 先用 filepath.EvalSymlinks 解析 source 的真实路径：若 source 自身是符号链接，会跟随到其最终
//     指向的真实文件/目录，满足需求「如果是符号链接，则是实际目录」，确保复制出的是真实数据而非又一个链接
//   - source 不可用（缺失/无法解析）时立即报错并跳过，绝不删除派生位置，避免破坏数据（源缺失不破坏）
//   - 将派生位置的旧链接删除（默认移入回收站而非真实删除，所有数据都可恢复；
//     noTrash 为真时改为真实删除）
//   - 移动后用 pathutil.Copy 把真实数据复制到派生位置；Copy 会正确处理文件与目录（目录递归复制）
//   - 自引用前置守卫（本次新增）：派生位置位于权威源内部、或两者是同一路径时直接报错并中止，
//     保证在这次「先删后复制」的操作里不产生任何文件系统变更（详见函数体内的注释）
//
// skipConfirm 为真时跳过交互确认（来自 --all/--yes/--force）；为假且环境不可交互时，
// prompt.Confirm 会返回错误而不是无限等待，保证不会在无人值守场景挂起
//
// noTrash 决定旧链接的删除策略，由调用方显式传入（CLI 传包级 noTrash，WebUI 传请求体里的取值）：
// 本函数刻意不再读包级变量，否则网页端「本次是否真实删除」的选择会被命令行开关覆盖
func replaceWithReal(source, derived, sourceLabel, derivedLabel string, skipConfirm, noTrash bool, errorOutput ...io.Writer) error {
	errOut := io.Writer(os.Stderr)
	if len(errorOutput) > 0 && errorOutput[0] != nil {
		errOut = errorOutput[0]
	}

	actualSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("%s", l10n.T("{{.Source}} is unavailable ({{.Err}}); skipped to avoid data loss", map[string]any{"Source": sourceLabel, "Err": err.Error()}))
	}

	// 自引用前置守卫（本次新增）：命中的形态是「派生位置位于权威源内部」或「两者是同一路径」
	// 这两种情况下复制会变成自毁——目标先被建出来，紧接着 ReadDir 源目录读到自己刚建的目标并
	// 递归下钻，每层多一级同名子目录，直到路径超长报错，期间在用户的源目录里留下大量垃圾目录
	// （可达路径：flk create symlink -r ~/repo -f ~/repo/self 之后执行 flk unlink）
	//
	// 为什么必须放在这里（三个位置约束，缺一不可）：
	//   1. 必须在 safeop.Delete(derived) 之前：本函数是「先删除派生位置、再复制」的顺序，
	//      若只依赖 pathutil.Copy 的底层守卫，报错时派生位置上的链接/数据已经被删除（noTrash 为假时进回收站、
	//      为真时是永久删除），状态已被改变，用户还得自己恢复
	//   2. 必须在 prompt.Confirm 之前：不能让用户先确认「删除链接并替换为真实文件」、回答 y 之后
	//      才被告知这组路径非法——那既误导用户，也让人怀疑围栏是否真的生效
	//   3. 必须使用 EvalSymlinks 之后的 actualSource 而不是入参 source：source 自身可能是符号链接，
	//      复制真正读取的是解析后的真实路径，用未解析的写法判断会漏判
	//
	// 判定复用 pathutil.CheckCopyPaths（与 pathutil.Copy 内部守卫同一份口径与文案），
	// 参数顺序与下面的复制调用 pathutil.Copy(actualSource, derived) 严格一致（src 在前、dst 在后）
	//
	// 错误原样返回、不再包一层「复制失败」：文案本身已经说清原因与方向，多包一层只会让用户看到
	// 「复制失败: 拒绝把 X 复制进 Y」这种冗余信息；同时保持错误链首层就是哨兵错误，errors.Is 判定更直观
	if err := pathutil.CheckCopyPaths(actualSource, derived); err != nil {
		return err
	}

	if !skipConfirm {
		// 文案不能写死「删除链接」：noTrash 为真时该链接是被永久删除的，
		// 而默认模式下它只是被移入回收站，用中性表述才能同时覆盖两种策略
		pterm.Warning.WithWriter(errOut).Println(l10n.T("About to remove the link and replace it with a real file: {{.Path}}", map[string]any{"Path": derived}))
		// 统一经 prompt.Confirm：--yes 直接同意，非终端且未 --yes 时返回错误而非阻塞
		confirm, cerr := prompt.Confirm(l10n.T("Confirm replacing this link with a real file?", nil), false)
		if cerr != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to get the confirmation input", nil), cerr)
		}
		if !confirm {
			return safeop.ErrOperationCancelled
		}
	}

	// 派生位置不存在时视为已就绪（safeop.Delete 对不存在的路径直接返回 nil）
	// 删除策略统一交给 safeop：noTrash 为假时移入回收站，为真时为真实删除；
	// 因此错误文案保持中性（只说删除），不写死「移至回收站」以免与真实删除模式矛盾
	if err := safeop.Delete(derived, noTrash); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to delete {{.Derived}}", map[string]any{"Derived": derivedLabel}), err)
	}

	// 用权威源的真实内容在派生位置生成一份独立副本，至此二者不再共享链接关系
	if err := pathutil.Copy(actualSource, derived); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to copy the real file to {{.Derived}}", map[string]any{"Derived": derivedLabel}), err)
	}
	return nil
}

// 本文件原先自带的「移除记录」与「落盘」两个封装已合并到共享实现：
// 记录移除见 cmd/check.go 的 removeTrackedRecord，落盘见同文件的 saveTrackedStore
// 合并原因是 fix 的删除分支维护着同一份逻辑，放在一起才能保证字段映射与判空保护只有一处
