package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	createcopy "github.com/jy-eggroll/flk/internal/create/copy"
	"github.com/jy-eggroll/flk/internal/create/shared"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/spf13/cobra"
)

var (
	copySrc string
	copyDst string
)

var copyCmd = &cobra.Command{
	Use:     "copy",
	Aliases: []string{"cp"},
	Short:   l10n.T("Copy a file (fallback when symlinks/hardlinks are unsupported)", nil),
	Long:    l10n.T("Copy a file (fallback when symlinks/hardlinks are unsupported)", nil),
	RunE:    Copy,
}

func init() {
	createCmd.AddCommand(copyCmd)
	// copy 会持久化记录，并保证 JSON 模式的 stdout 只有一个最终 CreateResult
	MarkNeedsStore(copyCmd)
	MarkSupportsJSON(copyCmd)
	copyCmd.Flags().StringVar(&copySrc, "src", "", l10n.T("Source file path", nil))
	copyCmd.Flags().StringVar(&copyDst, "dst", "", l10n.T("Destination file path", nil))
	copyCmd.Flags().BoolVar(&createSmart, "smart", false, l10n.T("Smart mode: when dst exists, back it up to src before copying", nil))
	copyCmd.Flags().BoolVar(&createForce, "force", false, l10n.T("Force overwrite an existing file or directory", nil))
	copyCmd.Flags().StringVarP(&createDevice, "device", "d", "all", l10n.T("Device name used for later device filtering", nil))
	copyCmd.MarkFlagRequired("src")
	copyCmd.MarkFlagRequired("dst")
}

// Copy 复制普通文件，并覆盖“目标反向备份为源文件”的智能恢复分支
// 无论走常规复制还是智能备份，最终都会执行同一套绝对路径检查、store 保存和唯一结果渲染
func Copy(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)
	const resultType = "copy"

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

	logger.Info(l10n.T("Copying file", nil), "src", copySrc, "dst", copyDst, "device", createDevice, "force", createForce)

	normalizedSrc, err := pathutil.NormalizePath(copySrc)
	if err != nil {
		message := l10n.T("Failed to normalize the source file path: {{.Err}}", map[string]any{"Err": err.Error()})
		return failure(message, fmt.Errorf("%s: %w", l10n.T("Failed to normalize the source file path", nil), err))
	}

	normalizedDst, err := pathutil.NormalizePath(copyDst)
	if err != nil {
		message := l10n.T("Failed to normalize the destination file path: {{.Err}}", map[string]any{"Err": err.Error()})
		return failure(message, fmt.Errorf("%s: %w", l10n.T("Failed to normalize the destination file path", nil), err))
	}
	logger.Debug(l10n.T("Path normalization complete", nil), "normalizedSrc", normalizedSrc, "normalizedDst", normalizedDst)

	srcInfo, _ := os.Stat(normalizedSrc)
	dstInfo, _ := os.Stat(normalizedDst)
	if srcInfo == nil && dstInfo != nil && dstInfo.IsDir() {
		// copy 只支持普通文件；该分支保留原有决策，不尝试把目标目录反向备份为源目录
		message := l10n.T("The source file does not exist and the destination is a directory; copying is not supported", nil)
		return failure(message, errors.New(message))
	}

	// 智能恢复中，src 不存在而 dst 存在时，备份动作本身已经完成复制目标；其余情况继续走常规 Create
	operationCompleted := false
	if srcInfo == nil && dstInfo != nil {
		backupResult, err := shared.HandleTargetBackup(shared.BackupOptions{
			SourcePath:  normalizedSrc,
			TargetPath:  normalizedDst,
			Smart:       createSmart,
			Force:       createForce,
			NoTrash:     noTrash,
			SourceLabel: "src",
			TargetLabel: "dst",
			Output:      cmd.ErrOrStderr(),
		})
		if err != nil {
			if errors.Is(err, safeop.ErrOperationCancelled) {
				return renderCreateCancellation(cmd, format, resultType)
			}
			return failure(err.Error(), err)
		}
		operationCompleted = backupResult.BackedUp
	}

	if !operationCompleted {
		if err := createcopy.Create(normalizedSrc, normalizedDst, createForce, createSmart, noTrash, cmd.ErrOrStderr()); err != nil {
			if errors.Is(err, safeop.ErrOperationCancelled) {
				return renderCreateCancellation(cmd, format, resultType)
			}
			return failure(err.Error(), err)
		}
	}

	// 复制或智能备份已经产生文件系统结果；后续记录失败返回非零并说明不回滚，避免错误地报告整体成功
	absDstPath, err := pathutil.ToAbsolute(normalizedDst)
	if err != nil {
		persistenceErr := createPersistenceError(l10n.T("Copy operation", nil), fmt.Errorf("%s: %w", l10n.T("Failed to produce the absolute destination path", nil), err))
		return failure(persistenceErr.Error(), persistenceErr)
	}
	fields := map[string]string{
		"src": normalizedSrc,
		"dst": absDstPath,
	}
	if err := persistCreateRecord(createDevice, "copy", fields); err != nil {
		persistenceErr := createPersistenceError(l10n.T("Copy operation", nil), err)
		return failure(persistenceErr.Error(), persistenceErr)
	}

	return renderCreateResult(cmd, format, output.CreateResult{Success: true, Type: resultType, Message: l10n.T("Copied successfully", nil)}, nil)
}
