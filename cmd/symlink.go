package cmd

import (
	"errors"
	"os"
	"strings"

	"github.com/jy-eggroll/flk/internal/create/symlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var (
	symlinkReal string
	symlinkFake string
)

var symlinkCmd = &cobra.Command{
	Use:     "symlink",
	Aliases: []string{"sm"},
	Short:   "创建符号链接 (支持文件和文件夹)",
	Long:    "创建符号链接 (支持文件和文件夹)",
	RunE:    Symlink,
}

func init() {
	createCmd.AddCommand(symlinkCmd)
	symlinkCmd.Flags().StringVarP(&symlinkReal, "real", "r", "", "真实文件路径")
	symlinkCmd.Flags().StringVarP(&symlinkFake, "fake", "f", "", "链接文件路径")
	symlinkCmd.Flags().BoolVar(&createSmart, "smart", false, "智能模式：当 real 不存在但 fake 存在时，自动将 fake 复制到 real 再创建链接")
	symlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	symlinkCmd.Flags().StringVarP(&createDevice, "device", "d", "all", "设备名称，用于后续设备过滤")
	symlinkCmd.MarkFlagRequired("real")
	symlinkCmd.MarkFlagRequired("fake")
}

func Symlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)

	if strings.Contains(createDevice, ",") || strings.Contains(createDevice, " ") {
		result := output.CreateResult{Success: false, Type: "符号链接", Error: "设备名称不能包含逗号或空格"}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	if verbose {
		logger.Info("开始创建符号链接", "real", symlinkReal, "fake", symlinkFake, "device", createDevice, "force", createForce)
	}

	normalizedReal, err := pathutil.NormalizePath(symlinkReal)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "符号链接", Error: "真实文件路径标准化失败 " + err.Error()}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	var normalizedFake string
	normalizedFake, err = pathutil.NormalizePath(symlinkFake)
	if err != nil {
		result := output.CreateResult{Success: false, Type: "符号链接", Error: "链接文件路径标准化失败 " + err.Error()}
		output.PrintCreateResult(format, result)
		return errors.New(result.Error)
	}

	if verbose {
		logger.Debug("路径标准化完成", "normalizedReal", normalizedReal, "normalizedFake", normalizedFake)
	}

	realExists, _ := os.Stat(normalizedReal)
	fakeExists, _ := os.Stat(normalizedFake)
	if realExists == nil && fakeExists != nil && createSmart {
		logger.Info("智能模式：检测到 real 不存在但 fake 存在，准备复制 fake 到 real")
		if err := pathutil.Copy(normalizedFake, normalizedReal); err != nil {
			result := output.CreateResult{Success: false, Type: "符号链接", Error: "智能复制失败: " + err.Error()}
			output.PrintCreateResult(format, result)
			return errors.New(result.Error)
		}
		logger.Info("智能复制完成", "from", normalizedFake, "to", normalizedReal)
		pterm.Success.Println("智能复制成功: " + normalizedReal)
	} else if realExists == nil && fakeExists != nil && !createForce {
		confirm, _ := pterm.DefaultInteractiveConfirm.Show(
			"real 不存在但 fake 存在，是否将 fake 复制到 real 再创建链接？",
		)
		if confirm {
			logger.Info("用户确认智能复制")
			if err := pathutil.Copy(normalizedFake, normalizedReal); err != nil {
				result := output.CreateResult{Success: false, Type: "符号链接", Error: "智能复制失败: " + err.Error()}
				output.PrintCreateResult(format, result)
				return errors.New(result.Error)
			}
			logger.Info("智能复制完成", "from", normalizedFake, "to", normalizedReal)
			pterm.Success.Println("智能复制成功: " + normalizedReal)
		} else {
			pterm.Info.Println("已取消智能复制")
			return nil
		}
	}

	if verbose {
		logger.Info("创建符号链接 real=" + normalizedReal + ", fake=" + normalizedFake)
	}

	var result output.CreateResult
	if err := symlink.Create(normalizedReal, normalizedFake, createForce); err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			pterm.Info.Println("已取消操作")
			return nil
		}
		result = output.CreateResult{Success: false, Type: "符号链接", Error: err.Error()}
	} else {
		result = output.CreateResult{Success: true, Type: "符号链接", Message: "创建成功"}
		// 持久化数据
		if store.GlobalManager == nil {
			if err := store.InitStore(store.StorePath); err != nil {
				logger.Error("初始化存储失败 " + err.Error())
			}
		}
		mgr := store.GlobalManager
		if mgr != nil {
			absFakePath, _ := pathutil.ToAbsolute(normalizedFake)
			fields := map[string]string{
				"real": normalizedReal,
				"fake": absFakePath,
			}
			parentPath := WorkDir
			mgr.AddRecord(createDevice, "symlink", parentPath, fields)
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
