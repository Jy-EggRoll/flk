package cmd

import (
	"errors"
	"fmt"

	"github.com/jy-eggroll/flk/internal/create/shared"
	"github.com/jy-eggroll/flk/internal/create/symlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/spf13/cobra"
)

var (
	symlinkReal string
	symlinkFake string
)

var symlinkCmd = &cobra.Command{
	Use:     "symlink",
	Aliases: []string{"sm"},
	Short:   l10n.T("Create a symbolic link (files and directories supported)", nil),
	Long:    l10n.T("Create a symbolic link (files and directories supported)", nil),
	RunE:    Symlink,
}

func init() {
	createCmd.AddCommand(symlinkCmd)
	// create 叶子命令会读写 store，并已保证 JSON stdout 只有一个最终 CreateResult
	MarkNeedsStore(symlinkCmd)
	MarkSupportsJSON(symlinkCmd)
	symlinkCmd.Flags().StringVarP(&symlinkReal, "real", "r", "", l10n.T("Path to the real file", nil))
	symlinkCmd.Flags().StringVarP(&symlinkFake, "fake", "f", "", l10n.T("Path to the link file", nil))
	symlinkCmd.Flags().BoolVar(&createSmart, "smart", false, l10n.T("Smart mode: when fake exists, back it up to real before creating the link", nil))
	symlinkCmd.Flags().BoolVar(&createForce, "force", false, l10n.T("Force overwrite an existing file or directory", nil))
	symlinkCmd.Flags().StringVarP(&createDevice, "device", "d", "all", l10n.T("Device name used for later device filtering", nil))
	symlinkCmd.MarkFlagRequired("real")
	symlinkCmd.MarkFlagRequired("fake")
}

// Symlink 创建符号链接，并保证每条成功、失败或取消路径只产生一个最终 stdout 结果
func Symlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)
	const resultType = "symlink"

	// 失败渲染与设备名校验统一走 cmd/create.go 的 create 系列共享实现，避免三个叶子命令各写一份
	failure := newCreateFailure(cmd, format, resultType)

	if message := validateCreateDevice(createDevice); message != "" {
		return failure(message, errors.New(message))
	}

	// 日志调用始终执行，是否展示完全由根层配置的日志级别决定
	logger.Info(l10n.T("Creating a symbolic link", nil), "real", symlinkReal, "fake", symlinkFake, "device", createDevice, "force", createForce)

	normalizedReal, err := pathutil.NormalizePath(symlinkReal)
	if err != nil {
		message := l10n.T("Failed to normalize the real file path: {{.Err}}", map[string]any{"Err": err.Error()})
		return failure(message, fmt.Errorf("%s: %w", l10n.T("Failed to normalize the real file path", nil), err))
	}

	normalizedFake, err := pathutil.NormalizePath(symlinkFake)
	if err != nil {
		message := l10n.T("Failed to normalize the link file path: {{.Err}}", map[string]any{"Err": err.Error()})
		return failure(message, fmt.Errorf("%s: %w", l10n.T("Failed to normalize the link file path", nil), err))
	}
	logger.Debug(l10n.T("Path normalization complete", nil), "normalizedReal", normalizedReal, "normalizedFake", normalizedFake)

	backupResult, err := shared.HandleTargetBackup(shared.BackupOptions{
		SourcePath:  normalizedReal,
		TargetPath:  normalizedFake,
		Smart:       createSmart,
		Force:       createForce,
		NoTrash:     noTrash,
		SourceLabel: "real",
		TargetLabel: "fake",
		Output:      cmd.ErrOrStderr(),
	})
	if err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			return renderCreateCancellation(cmd, format, resultType)
		}
		return failure(err.Error(), err)
	}

	logger.Info(l10n.T("Creating a symbolic link", nil), "real", normalizedReal, "fake", normalizedFake)
	if err := symlink.Create(normalizedReal, normalizedFake, backupResult.RemoveOpts); err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			return renderCreateCancellation(cmd, format, resultType)
		}
		return failure(err.Error(), err)
	}

	// 文件操作已经成功，此后的绝对路径或 store 错误必须呈现失败结果并返回非零，但不得撤销已创建的链接
	absFakePath, err := pathutil.ToAbsolute(normalizedFake)
	if err != nil {
		persistenceErr := createPersistenceError(l10n.T("Symbolic link creation", nil), fmt.Errorf("%s: %w", l10n.T("Failed to produce the absolute link path", nil), err))
		return failure(persistenceErr.Error(), persistenceErr)
	}
	fields := map[string]string{
		"real": normalizedReal,
		"fake": absFakePath,
	}
	if err := persistCreateRecord(createDevice, "symlink", fields); err != nil {
		persistenceErr := createPersistenceError(l10n.T("Symbolic link creation", nil), err)
		return failure(persistenceErr.Error(), persistenceErr)
	}

	return renderCreateResult(cmd, format, output.CreateResult{Success: true, Type: resultType, Message: l10n.T("Created successfully", nil)}, nil)
}
