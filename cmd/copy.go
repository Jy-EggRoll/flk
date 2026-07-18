package cmd

import (
	"errors"
	"os"
	"strings"

	"github.com/jy-eggroll/flk/internal/create/copy"
	"github.com/jy-eggroll/flk/internal/create/shared"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/pterm/pterm"
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
	copyCmd.Flags().StringVar(&copySrc, "src", "", "源文件路径")
	copyCmd.Flags().StringVar(&copyDst, "dst", "", "目标文件路径")
	copyCmd.Flags().BoolVar(&createSmart, "smart", false, "智能模式：当 dst 存在时，自动将 dst 备份到 src 再复制")
	copyCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	copyCmd.Flags().StringVarP(&createDevice, "device", "d", "all", "设备名称，用于后续设备过滤")
	copyCmd.MarkFlagRequired("src")
	copyCmd.MarkFlagRequired("dst")
}

func Copy(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)

	if strings.Contains(createDevice, ",") || strings.Contains(createDevice, " ") {
		result := output.CreateResult{Success: false, Type: "复制", Error: "设备名称不能包含逗号或空格"}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	normalizedSrc, err := pathutil.NormalizePath(copySrc)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "复制", Error: "源文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	normalizedDst, err := pathutil.NormalizePath(copyDst)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "复制", Error: "目标文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	srcExists, _ := os.Stat(normalizedSrc)
	dstExists, _ := os.Stat(normalizedDst)

	if srcExists == nil && dstExists != nil && dstExists.IsDir() {
		// 仅在不涉及目录时提示备份（copy 只支持文件）
		logger.Error("src 不存在，dst 是目录，复制不支持目录")
		result := output.CreateResult{Success: false, Type: "复制", Error: "源文件不存在，目标路径是目录，不支持复制"}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	if srcExists == nil && dstExists != nil {
		backupResult, err := shared.HandleTargetBackup(shared.BackupOptions{
			SourcePath:  normalizedSrc,
			TargetPath:  normalizedDst,
			Smart:       createSmart,
			Force:       createForce,
			SourceLabel: "src",
			TargetLabel: "dst",
		})
		if err != nil {
			if errors.Is(err, safeop.ErrOperationCancelled) {
				pterm.Info.Println("已取消操作")
				return nil
			}
			result := output.CreateResult{Success: false, Type: "复制", Error: err.Error()}
			output.PrintCreateResult(format, result)
			return err
		}

		if backupResult.BackedUp {
			// 对于复制操作，备份本身就是操作本身，不需要后续的删除和链接步骤
			// HandleTargetBackup 已输出"复制成功"，此处只做持久化和结束
			persistRecord(createDevice, "copy", map[string]string{
				"src": normalizedSrc,
				"dst": normalizedDst,
			})
			return nil
		}
	}

	var result output.CreateResult
	if err := copy.Create(normalizedSrc, normalizedDst, createForce, createSmart); err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			pterm.Info.Println("已取消操作")
			return nil
		}
		result = output.CreateResult{Success: false, Type: "复制", Error: err.Error()}
	} else {
		result = output.CreateResult{Success: true, Type: "复制", Message: "复制成功"}
		persistRecord(createDevice, "copy", map[string]string{
			"src": normalizedSrc,
			"dst": normalizedDst,
		})
	}
	output.PrintCreateResult(format, result)
	if result.Success {
		return nil
	}
	return errors.New(result.Error)
}
