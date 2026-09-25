package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jy-eggroll/flk/internal/create/hardlink"
	"github.com/jy-eggroll/flk/internal/create/shared"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/spf13/cobra"
)

var (
	hardlinkPrim string
	hardlinkSeco string
)

var hardlinkCmd = &cobra.Command{
	Use:     "hardlink",
	Aliases: []string{"hd"},
	Short:   l10n.T("Create a hard link (same-partition files only)", nil),
	Long:    l10n.T("Create a hard link (same-partition files only)", nil),
	RunE:    Hardlink,
}

func init() {
	createCmd.AddCommand(hardlinkCmd)
	// 根层据此按需初始化 store，并允许本叶子命令接受 --output json
	MarkNeedsStore(hardlinkCmd)
	MarkSupportsJSON(hardlinkCmd)
	hardlinkCmd.Flags().StringVarP(&hardlinkPrim, "prim", "p", "", l10n.T("Path to the primary file", nil))
	hardlinkCmd.Flags().StringVarP(&hardlinkSeco, "seco", "s", "", l10n.T("Path to the secondary file", nil))
	hardlinkCmd.Flags().BoolVar(&createSmart, "smart", false, l10n.T("Smart mode: when seco exists, back it up to prim before creating the link", nil))
	hardlinkCmd.Flags().BoolVar(&createForce, "force", false, l10n.T("Force overwrite an existing file or directory", nil))
	hardlinkCmd.Flags().StringVarP(&createDevice, "device", "d", "all", l10n.T("Device name used for later device filtering", nil))
	hardlinkCmd.MarkFlagRequired("prim")
	hardlinkCmd.MarkFlagRequired("seco")
}

// Hardlink 创建硬链接，并把最终业务状态统一渲染到 Cobra stdout
func Hardlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)
	const resultType = "hardlink"

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

	logger.Info(l10n.T("Creating a hard link", nil), "prim", hardlinkPrim, "seco", hardlinkSeco, "device", createDevice, "force", createForce)

	normalizedPrim, err := pathutil.NormalizePath(hardlinkPrim)
	if err != nil {
		message := l10n.T("Failed to normalize the primary file path: {{.Err}}", map[string]any{"Err": err.Error()})
		return failure(message, fmt.Errorf("%s: %w", l10n.T("Failed to normalize the primary file path", nil), err))
	}

	normalizedSeco, err := pathutil.NormalizePath(hardlinkSeco)
	if err != nil {
		message := l10n.T("Failed to normalize the secondary file path: {{.Err}}", map[string]any{"Err": err.Error()})
		return failure(message, fmt.Errorf("%s: %w", l10n.T("Failed to normalize the secondary file path", nil), err))
	}
	logger.Debug(l10n.T("Path normalization complete", nil), "normalizedPrim", normalizedPrim, "normalizedSeco", normalizedSeco)

	backupResult, err := shared.HandleTargetBackup(shared.BackupOptions{
		SourcePath:  normalizedPrim,
		TargetPath:  normalizedSeco,
		Smart:       createSmart,
		Force:       createForce,
		NoTrash:     noTrash,
		SourceLabel: "prim",
		TargetLabel: "seco",
		Output:      cmd.ErrOrStderr(),
	})
	if err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			return renderCreateCancellation(cmd, format, resultType)
		}
		return failure(err.Error(), err)
	}

	logger.Info(l10n.T("Creating a hard link", nil), "prim", normalizedPrim, "seco", normalizedSeco)
	if err := hardlink.Create(normalizedPrim, normalizedSeco, backupResult.RemoveOpts); err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			return renderCreateCancellation(cmd, format, resultType)
		}
		return failure(err.Error(), err)
	}

	// 链接已经创建后，记录准备或保存失败只能报告失败，不能回滚用户已经完成的文件系统操作
	absSecoPath, err := pathutil.ToAbsolute(normalizedSeco)
	if err != nil {
		persistenceErr := createPersistenceError(l10n.T("Hard link creation", nil), fmt.Errorf("%s: %w", l10n.T("Failed to produce the absolute secondary path", nil), err))
		return failure(persistenceErr.Error(), persistenceErr)
	}
	fields := map[string]string{
		"prim": normalizedPrim,
		"seco": absSecoPath,
	}
	if err := persistCreateRecord(createDevice, "hardlink", fields); err != nil {
		persistenceErr := createPersistenceError(l10n.T("Hard link creation", nil), err)
		return failure(persistenceErr.Error(), persistenceErr)
	}

	return renderCreateResult(cmd, format, output.CreateResult{Success: true, Type: resultType, Message: l10n.T("Created successfully", nil)}, nil)
}
