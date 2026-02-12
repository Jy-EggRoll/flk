package cmd

import (
	"errors"
	"os"

	"github.com/jy-eggroll/flk/internal/create/hardlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/spf13/cobra"
)

var (
	hardlinkPrim string
	hardlinkSeco string
)

var hardlinkCmd = &cobra.Command{
	Use:   "hardlink",
	Short: "创建硬链接（仅支持同分区文件）",
	Long:  "创建硬链接（仅支持同分区文件）",
	RunE:  Hardlink,
}

func init() {
	createCmd.AddCommand(hardlinkCmd)
	hardlinkCmd.Flags().StringVarP(&hardlinkPrim, "prim", "p", "", "主要文件路径")
	hardlinkCmd.Flags().StringVarP(&hardlinkSeco, "seco", "s", "", "次要文件路径")
	hardlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	hardlinkCmd.Flags().StringVarP(&createDevice, "device", "d", "all", "设备名称，用于后续设备过滤检查")
	hardlinkCmd.MarkFlagRequired("prim")
	hardlinkCmd.MarkFlagRequired("seco")
}

func Hardlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)

	normalizedPrim, err := pathutil.NormalizePath(hardlinkPrim)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "硬链接", Error: "主要文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return nil
	}

	normalizedSeco, err := pathutil.NormalizePath(hardlinkSeco)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "硬链接", Error: "次要文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return nil
	}

	var result output.CreateResult
	if err := hardlink.Create(normalizedPrim, normalizedSeco, createForce); err != nil {
		result = output.CreateResult{Success: false, Type: "硬链接", Error: err.Error()}
	} else {
		result = output.CreateResult{Success: true, Type: "硬链接", Message: "创建成功"}
		// 存储逻辑
		if store.GlobalManager == nil {
			if err := store.InitStore(store.StorePath); err != nil {
				logger.Error("初始化存储失败：" + err.Error())
			}
		}
		mgr := store.GlobalManager
		if mgr != nil {
			absSecoPath, _ := pathutil.ToAbsolute(normalizedSeco)
			fields := map[string]string{
				"prim": normalizedPrim,
				"seco": absSecoPath,
			}
			parentPath, _ := os.Getwd()
			mgr.AddRecord(createDevice, "hardlink", parentPath, fields)
			if err := mgr.Save(store.StorePath); err != nil {
				logger.Error("持久化失败：" + err.Error())
			}
		}
	}
	output.PrintCreateResult(format, result)
	if result.Success {
		return nil
	}
	return errors.New(result.Error)
}
