package cmd

import (
	"os"

	"github.com/jy-eggroll/flk/internal/create/symlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/spf13/cobra"
)

var (
	symlinkReal string
	symlinkFake string
)

var symlinkCmd = &cobra.Command{
	Use:   "symlink",
	Short: "创建符号链接（支持文件和文件夹）",
	Long:  "创建符号链接（支持文件和文件夹）",
	RunE:  Symlink,
}

func init() {
	createCmd.AddCommand(symlinkCmd)
	symlinkCmd.Flags().StringVarP(&symlinkReal, "real", "r", "", "真实文件路径")
	symlinkCmd.Flags().StringVarP(&symlinkFake, "fake", "f", "", "链接文件路径")
	symlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	symlinkCmd.Flags().StringVar(&createDevice, "device", "all", "设备名称，用于后续设备过滤检查")
	symlinkCmd.MarkFlagRequired("real")
	symlinkCmd.MarkFlagRequired("fake")
}

func Symlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)

	normalizedReal, err := pathutil.NormalizePath(symlinkReal)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "符号链接", Error: "真实文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return nil
	}

	normalizedFake, err := pathutil.NormalizePath(symlinkFake)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "符号链接", Error: "链接文件路径标准化失败: " + err.Error()}
		output.PrintCreateResult(format, result)
		return nil
	}

	var result output.CreateResult
	if err := symlink.Create(normalizedReal, normalizedFake, createForce); err != nil {
		result = output.CreateResult{Success: false, Type: "符号链接", Error: err.Error()}
	} else {
		result = output.CreateResult{Success: true, Type: "符号链接", Message: "创建成功"}
		// 持久化数据
		if store.GlobalManager == nil {
			if err := store.InitStore(store.StorePath); err != nil {
				logger.Error("初始化存储失败：" + err.Error())
			}
		}
		mgr := store.GlobalManager
		if mgr != nil {
			absFakePath, _ := pathutil.ToAbsolute(normalizedFake)
			fields := map[string]string{
				"real": normalizedReal,
				"fake": absFakePath,
			}
			parentPath, _ := os.Getwd()
			mgr.AddRecord(createDevice, "symlink", parentPath, fields)
			if err := mgr.Save(store.StorePath); err != nil {
				logger.Error("持久化失败：" + err.Error())
			}
		}
	}
	output.PrintCreateResult(format, result)
	return nil
}
