package cmd

import (
	"errors"
	"os"
	"strings"

	"github.com/jy-eggroll/flk/internal/create/copy"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/internal/store"
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
	copyCmd.Flags().StringVarP(&copySrc, "src", "s", "", "源文件路径")
	copyCmd.Flags().StringVarP(&copyDst, "dst", "d", "", "目标文件路径")
	copyCmd.Flags().BoolVar(&createSmart, "smart", false, "智能模式：当 src 不存在但 dst 存在时，自动将 dst 复制到 src")
	copyCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	copyCmd.Flags().StringVarP(&createDevice, "device", "D", "all", "设备名称，用于后续设备过滤")
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

	if srcExists == nil && dstExists == nil {
		logger.Info("src 和 dst 都存在，正常复制 src -> dst")
	} else if srcExists == nil && dstExists != nil {
		logger.Info("src 不存在但 dst 存在")
		if createSmart {
			logger.Info("smart 模式：直接复制 dst -> src")
			var result output.CreateResult
			if err := copy.Create(normalizedDst, normalizedSrc, createForce, false); err != nil {
				if errors.Is(err, safeop.ErrOperationCancelled) {
					pterm.Info.Println("已取消操作")
					return nil
				}
				result = output.CreateResult{Success: false, Type: "复制", Error: err.Error()}
				output.PrintCreateResult(format, result)
				return err
			}
			result = output.CreateResult{Success: true, Type: "复制", Message: "智能复制成功 (dst -> src)"}
			output.PrintCreateResult(format, result)
			return nil
		} else {
			confirm, _ := pterm.DefaultInteractiveConfirm.Show("src 不存在但 dst 存在，是否将 dst 复制到 src？")
			if confirm {
				logger.Info("用户确认智能复制 dst -> src")
				var result output.CreateResult
				if err := copy.Create(normalizedDst, normalizedSrc, createForce, false); err != nil {
					if errors.Is(err, safeop.ErrOperationCancelled) {
						pterm.Info.Println("已取消操作")
						return nil
					}
					result = output.CreateResult{Success: false, Type: "复制", Error: err.Error()}
					output.PrintCreateResult(format, result)
					return err
				}
				result = output.CreateResult{Success: true, Type: "复制", Message: "智能复制成功 (dst -> src)"}
				output.PrintCreateResult(format, result)
				return nil
			} else {
				pterm.Info.Println("已取消智能复制")
				return nil
			}
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
		if store.GlobalManager == nil {
			if err := store.InitStore(store.StorePath); err != nil {
				logger.Error("初始化存储失败 " + err.Error())
			}
		}
		mgr := store.GlobalManager
		if mgr != nil {
			absDstPath, _ := pathutil.ToAbsolute(normalizedDst)
			fields := map[string]string{
				"src": normalizedSrc,
				"dst": absDstPath,
			}
			parentPath := WorkDir
			mgr.AddRecord(createDevice, "copy", parentPath, fields)
			if err := mgr.Save(store.StorePath); err != nil {
				logger.Error("持久化失败 " + err.Error())
			}
		}
	}
	output.PrintCreateResult(format, result)
	if result.Success {
		return nil
	}
	return errors.New(result.Error)
}
