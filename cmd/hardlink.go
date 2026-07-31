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
	"github.com/spf13/cobra"
)

var (
	hardlinkPrim string
	hardlinkSeco string
)

var hardlinkCmd = &cobra.Command{
	Use:     "hardlink",
	Aliases: []string{"hd"},
	Short:   "创建硬链接 (仅支持同分区文件)",
	Long:    "创建硬链接 (仅支持同分区文件)",
	RunE:    Hardlink,
}

func init() {
	createCmd.AddCommand(hardlinkCmd)
	// 根层据此按需初始化 store，并允许本叶子命令接受 --output json
	MarkNeedsStore(hardlinkCmd)
	MarkSupportsJSON(hardlinkCmd)
	hardlinkCmd.Flags().StringVarP(&hardlinkPrim, "prim", "p", "", "主要文件路径")
	hardlinkCmd.Flags().StringVarP(&hardlinkSeco, "seco", "s", "", "次要文件路径")
	hardlinkCmd.Flags().BoolVar(&createSmart, "smart", false, "智能模式：当 seco 存在时，自动将 seco 备份到 prim 再创建链接")
	hardlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	hardlinkCmd.Flags().StringVarP(&createDevice, "device", "d", "all", "设备名称，用于后续设备过滤")
	hardlinkCmd.MarkFlagRequired("prim")
	hardlinkCmd.MarkFlagRequired("seco")
}

// Hardlink 创建硬链接，并把最终业务状态统一渲染到 Cobra stdout
func Hardlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)
	const resultType = "硬链接"

	failure := func(message string, cause error) error {
		if cause == nil {
			cause = errors.New(message)
		}
		return renderCreateResult(cmd, format, output.CreateResult{Success: false, Type: resultType, Error: message}, cause)
	}

	if strings.Contains(createDevice, ",") || strings.Contains(createDevice, " ") {
		const message = "设备名称不能包含逗号或空格"
		return failure(message, errors.New(message))
	}

	logger.Info("开始创建硬链接", "prim", hardlinkPrim, "seco", hardlinkSeco, "device", createDevice, "force", createForce)

	normalizedPrim, err := pathutil.NormalizePath(hardlinkPrim)
	if err != nil {
		message := "主要文件路径标准化失败: " + err.Error()
		return failure(message, fmt.Errorf("主要文件路径标准化失败: %w", err))
	}

	normalizedSeco, err := pathutil.NormalizePath(hardlinkSeco)
	if err != nil {
		message := "次要文件路径标准化失败: " + err.Error()
		return failure(message, fmt.Errorf("次要文件路径标准化失败: %w", err))
	}
	logger.Debug("路径标准化完成", "normalizedPrim", normalizedPrim, "normalizedSeco", normalizedSeco)

	backupResult, err := shared.HandleTargetBackup(shared.BackupOptions{
		SourcePath:  normalizedPrim,
		TargetPath:  normalizedSeco,
		Smart:       createSmart,
		Force:       createForce,
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

	logger.Info("创建硬链接", "prim", normalizedPrim, "seco", normalizedSeco)
	if err := hardlink.Create(normalizedPrim, normalizedSeco, backupResult.RemoveOpts); err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			return renderCreateCancellation(cmd, format, resultType)
		}
		return failure(err.Error(), err)
	}

	// 链接已经创建后，记录准备或保存失败只能报告失败，不能回滚用户已经完成的文件系统操作
	absSecoPath, err := pathutil.ToAbsolute(normalizedSeco)
	if err != nil {
		persistenceErr := createPersistenceError("硬链接创建", fmt.Errorf("生成次要文件绝对路径失败: %w", err))
		return failure(persistenceErr.Error(), persistenceErr)
	}
	fields := map[string]string{
		"prim": normalizedPrim,
		"seco": absSecoPath,
	}
	if err := persistCreateRecord(createDevice, "hardlink", fields); err != nil {
		persistenceErr := createPersistenceError("硬链接创建", err)
		return failure(persistenceErr.Error(), persistenceErr)
	}

	return renderCreateResult(cmd, format, output.CreateResult{Success: true, Type: resultType, Message: "创建成功"}, nil)
}
