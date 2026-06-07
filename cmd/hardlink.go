package cmd

import (
	"errors"
	"os"
	"strings"

	"github.com/jy-eggroll/flk/internal/create/hardlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/pterm/pterm"
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
	hardlinkCmd.Flags().StringVarP(&hardlinkPrim, "prim", "p", "", "主要文件路径")
	hardlinkCmd.Flags().StringVarP(&hardlinkSeco, "seco", "s", "", "次要文件路径")
	hardlinkCmd.Flags().BoolVar(&createSmart, "smart", false, "智能模式：当 prim 不存在但 seco 存在时，自动将 seco 复制到 prim 再创建链接")
	hardlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	hardlinkCmd.Flags().StringVarP(&createDevice, "device", "d", "all", "设备名称，用于后续设备过滤")
	hardlinkCmd.MarkFlagRequired("prim")
	hardlinkCmd.MarkFlagRequired("seco")
}

func Hardlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)

	if strings.Contains(createDevice, ",") || strings.Contains(createDevice, " ") {
		result := output.CreateResult{Success: false, Type: "硬链接", Error: "设备名称不能包含逗号或空格"}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	normalizedPrim, err := pathutil.NormalizePath(hardlinkPrim)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "硬链接", Error: "主要文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	normalizedSeco, err := pathutil.NormalizePath(hardlinkSeco)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "硬链接", Error: "次要文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	primExists, _ := os.Stat(normalizedPrim)
	secoExists, _ := os.Stat(normalizedSeco)
	if primExists == nil && secoExists != nil && createSmart {
		logger.Info("智能模式：检测到 prim 不存在但 seco 存在，准备复制 seco 到 prim")
		if err := pathutil.Copy(normalizedSeco, normalizedPrim); err != nil {
			result := output.CreateResult{Success: false, Type: "硬链接", Error: "智能复制失败: " + err.Error()}
			output.PrintCreateResult(format, result)
			return errors.New(result.Error)
		}
		logger.Info("智能复制完成", "from", normalizedSeco, "to", normalizedPrim)
		pterm.Success.Println("智能复制成功: " + normalizedPrim)
	} else if primExists == nil && secoExists != nil && !createForce {
		confirm, _ := pterm.DefaultInteractiveConfirm.Show(
			"prim 不存在但 seco 存在，是否将 seco 复制到 prim 再创建链接？",
		)
		if confirm {
			logger.Info("用户确认智能复制")
			if err := pathutil.Copy(normalizedSeco, normalizedPrim); err != nil {
				result := output.CreateResult{Success: false, Type: "硬链接", Error: "智能复制失败: " + err.Error()}
				output.PrintCreateResult(format, result)
				return errors.New(result.Error)
			}
			logger.Info("智能复制完成", "from", normalizedSeco, "to", normalizedPrim)
			pterm.Success.Println("智能复制成功: " + normalizedPrim)
		} else {
			pterm.Info.Println("已取消智能复制")
			return nil
		}
	}

	var result output.CreateResult
	if err := hardlink.Create(normalizedPrim, normalizedSeco, createForce); err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			pterm.Info.Println("已取消操作")
			return nil
		}
		result = output.CreateResult{Success: false, Type: "硬链接", Error: err.Error()}
	} else {
		result = output.CreateResult{Success: true, Type: "硬链接", Message: "创建成功"}
		// 存储逻辑
		if store.GlobalManager == nil {
			if err := store.InitStore(store.StorePath); err != nil {
				logger.Error("初始化存储失败 " + err.Error())
			}
		}
		mgr := store.GlobalManager
		if mgr != nil {
			absSecoPath, _ := pathutil.ToAbsolute(normalizedSeco)
			fields := map[string]string{
				"prim": normalizedPrim,
				"seco": absSecoPath,
			}
			mgr.AddRecord(createDevice, "hardlink", fields)
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
