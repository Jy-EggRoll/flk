package shared

import (
	"fmt"
	"io"
	"os"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/prompt"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
)

// BackupOptions 控制备份提示、进度输出与后续删除确认行为
type BackupOptions struct {
	SourcePath  string    // 主路径 (real/prim/src) — 备份目标
	TargetPath  string    // 副路径 (fake/seco/dst) — 备份来源
	Smart       bool      // --smart: 自动备份，不询问
	Force       bool      // --force: 跳过删除确认
	NoTrash     bool      // --no-trash: 覆盖目标时真实删除而不移入回收站
	SourceLabel string    // "real"/"prim"/"src" — 提示用
	TargetLabel string    // "fake"/"seco"/"dst" — 提示用
	Output      io.Writer // 接收备份进度和删除计划，命令层应传入 stderr，避免污染结构化 stdout
}

// BackupResult 记录备份操作的结果，并携带创建阶段删除目标所需的完整输出配置
type BackupResult struct {
	BackedUp   bool                 // 是否执行了备份
	RemoveOpts safeop.RemoveOptions // 用于后续删除步骤的确认配置
}

// HandleTargetBackup 检查 target 路径是否存在，如果存在则询问用户是否备份到 source
// target 存在说明有重要数据需要保护，source 是数据的最终归宿
//
// 安全围栏一（受保护路径）：source 与 target 任一为受保护路径（根目录本身或用户家目录本身）时
// 立即返回 ErrProtectedPath
//
// 安全围栏二（主副路径重叠）：source 与 target 为同一路径，或 source 位于 target 内部时
// 立即返回 pathutil.ErrCopyPathOverlap
//
// 注意本文件不再自带哨兵与错误类型：原先的 shared.ErrBackupPathOverlap 已删除并合并进
// pathutil.ErrCopyPathOverlap。理由是两者表达的是同一件事——备份在下面调用的就是
// pathutil.Copy(target, source)，它拦下的「source 位于 target 内部或两者相同」换算到 Copy 的
// 视角恰好是「目标位于源内部或源目标相同」：同一对路径、同一个谓词、同一种损害机制
// （CopyDir 边复制边把目标写进源里）。合并后全项目只有一份口径、一个哨兵、一条文案，
// 也顺带让「提前判断」与「底层守卫」不会再出现口径漂移
//
// 保留独立的错误类型这件事本身仍然必要：ErrProtectedPath 说的是「路径本身不允许作为操作对象」，
// 与 ErrCopyPathOverlap 说的「两个路径之间的关系不允许」是两回事，两者必须能被分别判定
//
// 两道围栏都发生在 os.Stat、备份确认与真正的 pathutil.Copy 之前，遵循同一原则——
// 「提前失败、不触碰任何文件系统状态」
func HandleTargetBackup(opts BackupOptions) (BackupResult, error) {
	// 两个路径都拦，规则是「/ 与 ~ 本身不得作为 flk 的操作对象（主路径或副路径）」：
	// 受保护路径作为 target 时，其内容是海量用户数据，复制到 source 会把磁盘撑满；
	// 作为 source 同样荒谬——任何软件配置都不会以根目录或家目录本身为路径。
	// 反之，拦截双方不会误伤合法用法，因为合法用法的主副路径都是家目录的子项而非家目录本身。
	// 判定复用 pathutil.IsProtectedPath，与删除口共享同一份实现，这里不写第二份口径
	//
	// 注意这条围栏挡不住、也不负责挡「source 位于 target 内部」：那与路径是否受保护无关，
	// 而由下面的关系围栏精确拦下（典型触发是 -r 指向家目录内的仓库、-f 指向该仓库自身）
	for _, protectedCandidate := range []string{opts.SourcePath, opts.TargetPath} {
		if pathutil.IsProtectedPath(protectedCandidate) {
			return BackupResult{}, safeop.NewProtectedPathError(protectedCandidate)
		}
	}

	// 路径关系围栏：两个路径各自可能都平平无奇，但它们的相对位置会让这次备份变成自毁操作，
	// 因此在同一处提前拒绝，仍然发生在 os.Stat、备份确认与 pathutil.Copy 之前，理由有三：
	//   1. 与受保护路径围栏同类，「拒绝」这件事不应依赖任何文件系统状态——提前失败最简单也最可预期
	//   2. 用户不应该先被问「是否把 fake 备份到 real」、回答 y 之后才被告知这组路径非法，
	//      那既误导用户，也让人怀疑围栏是否真的生效
	//   3. 更关键的是损害不可逆：source 位于 target 内部时 pathutil.Copy 会边复制边把源放进目标内部，
	//      源目录持续长大直到磁盘写满；source 与 target 相同时，CopyFile 用 os.Create 打开目标即截断，
	//      随后从已被截断的同一文件读取，结果是文件被清零——事后报错毫无意义，损害早已发生
	//
	// 放在最前面而不是紧挨 Copy 的另一个实际原因：中间隔着 prompt.Confirm，
	// 非交互环境下它会先报「无法获取用户输入」，把真正的非法路径原因掩盖成一条无关的报错
	//
	// 判定完全委托给 pathutil.CheckCopyPaths，参数顺序与下面真正的复制调用
	// pathutil.Copy(opts.TargetPath, opts.SourcePath) 严格一致（src=副路径、dst=主路径），
	// 因此这里拦下的形态与底层守卫将来会拦下的形态天然是同一件事，不存在「提前判断与底层口径漂移」：
	//   - 主副路径相同 → SamePath 命中
	//   - 主路径（dst）位于副路径（src）内部 → IsSubPath 命中
	// 反过来「副路径位于主路径内部」是正常备份用法（把副路径内容并入更大的主路径），
	// 不在 CheckCopyPaths 的拦截范围内，因此绝不会被误拦
	if err := pathutil.CheckCopyPaths(opts.TargetPath, opts.SourcePath); err != nil {
		return BackupResult{}, err
	}

	// 即使 target 当前不存在，后续创建阶段仍可能需要删除阻塞父目录，因此必须在任何提前返回前传递 Force、NoTrash 和 Output
	result := BackupResult{
		RemoveOpts: safeop.RemoveOptions{
			Force:   opts.Force,
			NoTrash: opts.NoTrash,
			Output:  opts.Output,
		},
	}

	realExists, _ := os.Stat(opts.SourcePath)
	fakeExists, _ := os.Stat(opts.TargetPath)

	// target 不存在，无需备份
	if fakeExists == nil {
		return result, nil
	}

	// 决定是否备份
	shouldBackup := opts.Smart

	if !opts.Smart {
		var promptMsg string
		if realExists != nil {
			if !realExists.IsDir() {
				promptMsg = l10n.T("{{.Src}} already exists as a file; back up {{.Tgt}} to {{.Src}} and overwrite?\nChoose n to create the link using the existing {{.Src}}", map[string]any{"Src": opts.SourceLabel, "Tgt": opts.TargetLabel})
			} else {
				promptMsg = l10n.T("Back up the files in {{.Tgt}} into {{.Src}} preserving the directory structure?\nAfter choosing n, {{.Tgt}} will become an empty directory", map[string]any{"Src": opts.SourceLabel, "Tgt": opts.TargetLabel})
			}
		} else {
			promptMsg = l10n.T("{{.Src}} does not exist; copy {{.Tgt}} to {{.Src}} before creating the link?", map[string]any{"Src": opts.SourceLabel, "Tgt": opts.TargetLabel})
		}

		// 统一走 prompt.Confirm：--yes 时直接同意备份；非交互且未 --yes 时立即报错而不是挂起
		confirm, err := prompt.Confirm(promptMsg, true)
		if err != nil {
			return result, fmt.Errorf("%s: %w", l10n.T("Failed to get user input", nil), err)
		}
		shouldBackup = confirm
	}

	if shouldBackup {
		logger.Info(l10n.T("Performing backup", nil), "from", opts.TargetPath, "to", opts.SourcePath)
		if err := pathutil.Copy(opts.TargetPath, opts.SourcePath); err != nil {
			return result, fmt.Errorf("%s: %w", l10n.T("Copy failed", nil), err)
		}
		result.BackedUp = true
		logger.Info(l10n.T("Backup complete", nil), "from", opts.TargetPath, "to", opts.SourcePath)

		// 备份提示属于过程信息，默认写入 stderr；显式检查写错误，避免管道关闭等异常被静默吞掉
		progressOutput := opts.Output
		if progressOutput == nil {
			progressOutput = os.Stderr
		}
		if _, err := io.WriteString(progressOutput, pterm.Success.Sprintln(l10n.T("Copied successfully: {{.Path}}", map[string]any{"Path": opts.SourcePath}))); err != nil {
			return result, fmt.Errorf("%s: %w", l10n.T("The backup is complete, but outputting the backup result failed", nil), err)
		}
	} else if realExists == nil {
		// source 不存在且用户拒绝备份 → 无法继续
		return result, safeop.ErrOperationCancelled
	}

	// 在基础选项上补充后续删除步骤的确认文案，Output 始终保持为命令层传入的 stderr
	if !opts.Force {
		if result.BackedUp {
			result.RemoveOpts.ConfirmMessage = l10n.T("Backup complete; safe to delete", nil)
			result.RemoveOpts.ConfirmDefault = true
		} else {
			result.RemoveOpts.ConfirmMessage = l10n.T("Deleting may cause data loss; delete anyway?", nil)
			result.RemoveOpts.ConfirmDefault = false
		}
	}

	return result, nil
}
