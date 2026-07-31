package shared

import (
	"fmt"
	"io"
	"os"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/pterm/pterm"
)

// BackupOptions 控制备份提示、进度输出与后续删除确认行为
type BackupOptions struct {
	SourcePath  string    // 主路径 (real/prim/src) — 备份目标
	TargetPath  string    // 副路径 (fake/seco/dst) — 备份来源
	Smart       bool      // --smart: 自动备份，不询问
	Force       bool      // --force: 跳过删除确认
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
func HandleTargetBackup(opts BackupOptions) (BackupResult, error) {
	// 即使 target 当前不存在，后续创建阶段仍可能需要删除阻塞父目录，因此必须在任何提前返回前传递 Force 和 Output
	result := BackupResult{
		RemoveOpts: safeop.RemoveOptions{
			Force:  opts.Force,
			Output: opts.Output,
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
				promptMsg = fmt.Sprintf(
					"%s 已存在文件，是否将 %s 备份到 %s 并覆盖？\n选择 n 将直接用现有的 %s 创建链接",
					opts.SourceLabel, opts.TargetLabel, opts.SourceLabel, opts.SourceLabel,
				)
			} else {
				promptMsg = fmt.Sprintf(
					"将 %s 中的文件按目录结构备份到 %s 下？\n选择 n 后 %s 将会成为空目录",
					opts.TargetLabel, opts.SourceLabel, opts.TargetLabel,
				)
			}
		} else {
			promptMsg = fmt.Sprintf(
				"%s 不存在，是否将 %s 复制到 %s 再创建链接？",
				opts.SourceLabel, opts.TargetLabel, opts.SourceLabel,
			)
		}

		confirm, err := pterm.DefaultInteractiveConfirm.WithDefaultValue(true).Show(promptMsg)
		if err != nil {
			return result, fmt.Errorf("获取用户输入失败: %w", err)
		}
		shouldBackup = confirm
	}

	if shouldBackup {
		logger.Info("执行备份", "from", opts.TargetPath, "to", opts.SourcePath)
		if err := pathutil.Copy(opts.TargetPath, opts.SourcePath); err != nil {
			return result, fmt.Errorf("复制失败: %w", err)
		}
		result.BackedUp = true
		logger.Info("备份完成", "from", opts.TargetPath, "to", opts.SourcePath)

		// 备份提示属于过程信息，默认写入 stderr；显式检查写错误，避免管道关闭等异常被静默吞掉
		progressOutput := opts.Output
		if progressOutput == nil {
			progressOutput = os.Stderr
		}
		if _, err := io.WriteString(progressOutput, pterm.Success.Sprintln("复制成功: "+opts.SourcePath)); err != nil {
			return result, fmt.Errorf("备份已完成，但输出备份结果失败: %w", err)
		}
	} else if realExists == nil {
		// source 不存在且用户拒绝备份 → 无法继续
		return result, safeop.ErrOperationCancelled
	}

	// 在基础选项上补充后续删除步骤的确认文案，Output 始终保持为命令层传入的 stderr
	if !opts.Force {
		if result.BackedUp {
			result.RemoveOpts.ConfirmMessage = "已备份，可安全删除"
			result.RemoveOpts.ConfirmDefault = true
		} else {
			result.RemoveOpts.ConfirmMessage = "删除可能导致数据丢失，是否仍删除？"
			result.RemoveOpts.ConfirmDefault = false
		}
	}

	return result, nil
}
