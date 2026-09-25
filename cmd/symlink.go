package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jy-eggroll/flk/internal/create/shared"
	"github.com/jy-eggroll/flk/internal/create/symlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
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

// renderCreateResult 把唯一的最终创建结果写入 Cobra stdout，并在业务失败已经成功渲染后附加根层可识别的标记
// 该 helper 放在允许修改的 create 叶子文件中供三个同包命令复用；输出本身失败时不能标记为已渲染，否则根层会吞掉唯一可见错误
func renderCreateResult(cmd *cobra.Command, format output.OutputFormat, result output.CreateResult, operationErr error) error {
	if err := output.PrintCreateResult(cmd.OutOrStdout(), format, result); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to output the creation result", nil), err)
	}
	if operationErr != nil {
		return MarkErrorRendered(operationErr)
	}
	return nil
}

// renderCreateCancellation 保留 table 模式原有的人类提示并以零退出，同时让 JSON 模式仍输出且只输出一个 CreateResult
// 取消不是执行失败，因此无论采用哪种格式，只要提示写入成功就返回 nil
func renderCreateCancellation(cmd *cobra.Command, format output.OutputFormat, resultType string) error {
	if format == output.JSON {
		return renderCreateResult(cmd, format, output.CreateResult{
			Success: false,
			Type:    resultType,
			Error:   l10n.T("Operation cancelled", nil),
		}, nil)
	}
	// 取消提示属于交互状态而非业务结果，写入 stderr 后仍保持零退出
	if _, err := io.WriteString(cmd.ErrOrStderr(), pterm.Info.Sprintln(l10n.T("Operation cancelled", nil))); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to output the cancellation result", nil), err)
	}
	return nil
}

// persistCreateRecord 只负责把已经完成的文件操作登记到根生命周期初始化好的全局 store
// Manager.AddRecord 当前是纯内存操作且无 error 返回；nil manager/data 是其唯一可预先识别的失败，Save 错误则原样上抛
func persistCreateRecord(device, linkType string, fields map[string]string) error {
	manager := store.GlobalManager
	if manager == nil || manager.Data == nil {
		return errors.New(l10n.T("Failed to add the record: the store is not initialized", nil))
	}
	manager.AddRecord(device, linkType, fields)
	if err := manager.Save(store.StorePath); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to save the record", nil), err)
	}
	return nil
}

// createPersistenceError 说明文件系统操作已经生效但记录阶段失败，明确告知调用者不会自动回滚
func createPersistenceError(action string, err error) error {
	return fmt.Errorf("%s", l10n.T("{{.Action}} completed, but {{.Err}}; the completed file operation was not rolled back", map[string]any{"Action": action, "Err": err.Error()}))
}

// Symlink 创建符号链接，并保证每条成功、失败或取消路径只产生一个最终 stdout 结果
func Symlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)
	const resultType = "symlink"

	failure := func(message string, cause error) error {
		if cause == nil {
			cause = errors.New(message)
		}
		return renderCreateResult(cmd, format, output.CreateResult{Success: false, Type: resultType, Error: message}, cause)
	}

	if strings.Contains(createDevice, ",") || strings.Contains(createDevice, " ") {
		message := l10n.T("Device name must not contain commas or spaces", nil)
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
