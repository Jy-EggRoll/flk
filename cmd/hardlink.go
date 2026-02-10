package cmd

import (
	"fmt"
	"os"

	"github.com/jy-eggroll/flk/internal/create/hardlink"
	"github.com/jy-eggroll/flk/internal/logger"
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
	logger.Init(nil)

	normalizedPrim, err := pathutil.NormalizePath(hardlinkPrim)
	if err != nil {
		logger.Debug("处理主要文件路径时出错: " + err.Error())
	} else {
		logger.Debug("经过处理的主要文件路径：" + normalizedPrim)
	}

	normalizedSeco, err := pathutil.NormalizePath(hardlinkSeco)
	if err != nil {
		logger.Debug("处理次要文件路径时出错: " + err.Error())
	} else {
		logger.Debug("经过处理的次要文件路径：" + normalizedSeco)
	}
	logger.Debug("强制覆盖选项：" + fmt.Sprint(createForce))
	logger.Debug("设备名称：" + createDevice)
	if err := hardlink.Create(normalizedPrim, normalizedSeco, createForce); err != nil {
		logger.Error("硬链接创建失败：" + err.Error() + fmt.Sprintf("错误类型是：%T", err))
		return nil
	}
	logger.Info("硬链接创建成功")
	// 使用全局存储管理器，确保数据是基于现有存储的追加而非覆写
	if store.GlobalManager == nil {
		// 尝试初始化全局存储
		if err := store.InitStore(store.StorePath); err != nil {
			logger.Error("初始化存储失败：" + err.Error())
		}
	}
	mgr := store.GlobalManager
	if mgr != nil {
		absSecoPath, _ := pathutil.ToAbsolute(normalizedSeco)
		fields := map[string]string{ // 要存储的字段键值对（路径类值会被自动折叠）
			"prim": normalizedPrim,
			"seco": absSecoPath,
		}
		parentPath, _ := os.Getwd()
		mgr.AddRecord(createDevice, "hardlink", parentPath, fields)
		if err := mgr.Save(store.StorePath); err != nil {
			logger.Error("持久化失败：" + err.Error())
		}
	}
	return nil
}
