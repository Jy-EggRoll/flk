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
	"github.com/spf13/cobra"
)

var (
	copySrc string
	copyDst string
)

var copyCmd = &cobra.Command{
	Use:     "copy",
	Aliases: []string{"cp"},
	Short:   "复制文件 (不支持符号链接/硬链接时的回退方案)",
	Long:    "复制文件 (不支持符号链接/硬链接时的回退方案)",
	RunE:    Copy,
}

func init() {
	createCmd.AddCommand(copyCmd)
	// copy 会持久化记录，并保证 JSON 模式的 stdout 只有一个最终 CreateResult
	MarkNeedsStore(copyCmd)
	MarkSupportsJSON(copyCmd)
	copyCmd.Flags().StringVar(&copySrc, "src", "", "源文件路径")
	copyCmd.Flags().StringVar(&copyDst, "dst", "", "目标文件路径")
	copyCmd.Flags().BoolVar(&createSmart, "smart", false, "智能模式：当 dst 存在时，自动将 dst 备份到 src 再复制")
	copyCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	copyCmd.Flags().StringVarP(&createDevice, "device", "d", "all", "设备名称，用于后续设备过滤")
	copyCmd.MarkFlagRequired("src")
	copyCmd.MarkFlagRequired("dst")
}

// Copy 复制普通文件，并覆盖“目标反向备份为源文件”的智能恢复分支
// 无论走常规复制还是智能备份，最终都会执行同一套绝对路径检查、store 保存和唯一结果渲染
func Copy(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)
	const resultType = "复制"

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

	logger.Info("开始复制文件", "src", copySrc, "dst", copyDst, "device", createDevice, "force", createForce)

	normalizedSrc, err := pathutil.NormalizePath(copySrc)
	if err != nil {
		message := "源文件路径标准化失败: " + err.Error()
		return failure(message, fmt.Errorf("源文件路径标准化失败: %w", err))
	}

	normalizedDst, err := pathutil.NormalizePath(copyDst)
	if err != nil {
		message := "目标文件路径标准化失败: " + err.Error()
		return failure(message, fmt.Errorf("目标文件路径标准化失败: %w", err))
	}
	logger.Debug("路径标准化完成", "normalizedSrc", normalizedSrc, "normalizedDst", normalizedDst)

	srcInfo, _ := os.Stat(normalizedSrc)
	dstInfo, _ := os.Stat(normalizedDst)
	if srcInfo == nil && dstInfo != nil && dstInfo.IsDir() {
		// copy 只支持普通文件；该分支保留原有决策，不尝试把目标目录反向备份为源目录
		const message = "源文件不存在，目标路径是目录，不支持复制"
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
		if err := createcopy.Create(normalizedSrc, normalizedDst, createForce, createSmart, cmd.ErrOrStderr()); err != nil {
			if errors.Is(err, safeop.ErrOperationCancelled) {
				return renderCreateCancellation(cmd, format, resultType)
			}
			return failure(err.Error(), err)
		}
	}

	// 复制或智能备份已经产生文件系统结果；后续记录失败返回非零并说明不回滚，避免错误地报告整体成功
	absDstPath, err := pathutil.ToAbsolute(normalizedDst)
	if err != nil {
		persistenceErr := createPersistenceError("复制操作", fmt.Errorf("生成目标文件绝对路径失败: %w", err))
		return failure(persistenceErr.Error(), persistenceErr)
	}
	fields := map[string]string{
		"src": normalizedSrc,
		"dst": absDstPath,
	}
	if err := persistCreateRecord(createDevice, "copy", fields); err != nil {
		persistenceErr := createPersistenceError("复制操作", err)
		return failure(persistenceErr.Error(), persistenceErr)
	}

	return renderCreateResult(cmd, format, output.CreateResult{Success: true, Type: resultType, Message: "复制成功"}, nil)
}
