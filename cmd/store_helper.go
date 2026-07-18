package cmd

import (
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
)

// persistRecord 将一条创建成功的记录写入全局存储并落盘
// 三个 create 命令（symlink/hardlink/copy）以及 fix 的修复逻辑都共用这一段持久化代码，
// 抽成 helper 以避免重复，并保证存储路径统一使用折叠绝对路径（~ 格式）
func persistRecord(device, linkType string, fields map[string]string) {
	if store.GlobalManager == nil {
		if err := store.InitStore(store.StorePath); err != nil {
			logger.Error("初始化存储失败 " + err.Error())
		}
	}
	mgr := store.GlobalManager
	if mgr == nil {
		return
	}
	processed := make(map[string]string, len(fields))
	for k, v := range fields {
		// 存储统一使用折叠绝对路径，fake/seco/dst 取绝对路径后折叠，real/prim/src 直接折叠
		absPath, _ := pathutil.ToAbsolute(v)
		folded, err := pathutil.FoldHome(absPath)
		if err != nil {
			folded = absPath
		}
		processed[k] = folded
	}
	mgr.AddRecord(device, linkType, processed)
	if err := mgr.Save(store.StorePath); err != nil {
		logger.Error("持久化失败 " + err.Error())
	}
}
