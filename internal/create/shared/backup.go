package shared

import (
	"fmt"
	"os"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/pterm/pterm"
)

// BackupOptions 控制备份提示的行为
type BackupOptions struct {
	SourcePath  string // 主路径 (real/prim/src) — 备份目标
	TargetPath  string // 副路径 (fake/seco/dst) — 备份来源
	Smart       bool   // --smart: 自动备份，不询问
	Force       bool   // --force: 跳过删除确认
	SourceLabel string // "real"/"prim"/"src" — 提示用
	TargetLabel string // "fake"/"seco"/"dst" — 提示用
}

// BackupResult 记录备份操作的结果
type BackupResult struct {
	BackedUp   bool                    // 是否执行了备份
	RemoveOpts safeop.RemoveOptions    // 用于后续删除步骤的确认配置
}

// HandleTargetBackup 检查 target 路径是否存在，如果存在则询问用户是否备份到 source
// target 存在说明有重要数据需要保护，source 是数据的最终归宿
func HandleTargetBackup(opts BackupOptions) (BackupResult, error) {
	result := BackupResult{}

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
		pterm.Success.Println("复制成功: " + opts.SourcePath)
	} else if realExists == nil {
		// source 不存在且用户拒绝备份 → 无法继续
		return result, safeop.ErrOperationCancelled
	}

	// 构建后续删除步骤的确认选项
	result.RemoveOpts = safeop.RemoveOptions{Force: opts.Force}
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
