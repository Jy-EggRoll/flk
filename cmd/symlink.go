package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jy-eggroll/flk/internal/create/shared"
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
	// create 叶子命令会读写 store，并已保证 JSON stdout 只有一个最终 CreateResult
	MarkNeedsStore(symlinkCmd)
	MarkSupportsJSON(symlinkCmd)
	symlinkCmd.Flags().StringVarP(&symlinkReal, "real", "r", "", "真实文件路径")
	symlinkCmd.Flags().StringVarP(&symlinkFake, "fake", "f", "", "链接文件路径")
	symlinkCmd.Flags().BoolVar(&createSmart, "smart", false, "智能模式：当 fake 存在时，自动将 fake 备份到 real 再创建链接")
	symlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	symlinkCmd.Flags().StringVarP(&createDevice, "device", "d", "all", "设备名称，用于后续设备过滤")
	symlinkCmd.MarkFlagRequired("real")
	symlinkCmd.MarkFlagRequired("fake")
}

// renderCreateResult 把唯一的最终创建结果写入 Cobra stdout，并在业务失败已经成功渲染后附加根层可识别的标记
// 该 helper 放在允许修改的 create 叶子文件中供三个同包命令复用；输出本身失败时不能标记为已渲染，否则根层会吞掉唯一可见错误
func renderCreateResult(cmd *cobra.Command, format output.OutputFormat, result output.CreateResult, operationErr error) error {
	if err := output.PrintCreateResult(cmd.OutOrStdout(), format, result); err != nil {
		return fmt.Errorf("输出创建结果失败: %w", err)
	}
	if operationErr != nil {
		return MarkErrorRendered(operationErr)
	}
	return nil
}

// renderCreateCancellation 保留 table 模式原有的人类提示并以零退出，同时让 JSON 模式仍输出且只输出一个 CreateResult
// 取消不是执行失败，因此无论采用哪种格式，只要提示写入成功就返回 nil
func renderCreateCancellation(cmd *cobra.Command, format output.OutputFormat, resultType string) error {
	if format == output.JSON {
		return renderCreateResult(cmd, format, output.CreateResult{
			Success: false,
			Type:    resultType,
			Error:   "已取消操作",
		}, nil)
	}
	// 取消提示属于交互状态而非业务结果，写入 stderr 后仍保持零退出
	if _, err := io.WriteString(cmd.ErrOrStderr(), pterm.Info.Sprintln("已取消操作")); err != nil {
		return fmt.Errorf("输出取消结果失败: %w", err)
	}
	return nil
}

// persistCreateRecord 只负责把已经完成的文件操作登记到根生命周期初始化好的全局 store
// Manager.AddRecord 当前是纯内存操作且无 error 返回；nil manager/data 是其唯一可预先识别的失败，Save 错误则原样上抛
func persistCreateRecord(device, linkType string, fields map[string]string) error {
	manager := store.GlobalManager
	if manager == nil || manager.Data == nil {
		return errors.New("添加记录失败：持久化存储尚未初始化")
	}
	manager.AddRecord(device, linkType, fields)
	if err := manager.Save(store.StorePath); err != nil {
		return fmt.Errorf("保存记录失败: %w", err)
	}
	return nil
}

// createPersistenceError 说明文件系统操作已经生效但记录阶段失败，明确告知调用者不会自动回滚
func createPersistenceError(action string, err error) error {
	return fmt.Errorf("%s已完成，但%s；未回滚已完成的文件操作", action, err)
}

// Symlink 创建符号链接，并保证每条成功、失败或取消路径只产生一个最终 stdout 结果
func Symlink(cmd *cobra.Command, args []string) error {
	format := output.OutputFormat(outputFormat)
	const resultType = "符号链接"

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

	// 日志调用始终执行，是否展示完全由根层配置的日志级别决定
	logger.Info("开始创建符号链接", "real", symlinkReal, "fake", symlinkFake, "device", createDevice, "force", createForce)

	normalizedReal, err := pathutil.NormalizePath(symlinkReal)
	if err != nil {
		message := "真实文件路径标准化失败 " + err.Error()
		return failure(message, fmt.Errorf("真实文件路径标准化失败: %w", err))
	}

	normalizedFake, err := pathutil.NormalizePath(symlinkFake)
	if err != nil {
		message := "链接文件路径标准化失败 " + err.Error()
		return failure(message, fmt.Errorf("链接文件路径标准化失败: %w", err))
	}
	logger.Debug("路径标准化完成", "normalizedReal", normalizedReal, "normalizedFake", normalizedFake)

	backupResult, err := shared.HandleTargetBackup(shared.BackupOptions{
		SourcePath:  normalizedReal,
		TargetPath:  normalizedFake,
		Smart:       createSmart,
		Force:       createForce,
		SourceLabel: "real",
		TargetLabel: "fake",
		Output:      cmd.ErrOrStderr(),
	})
	if err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			return renderCreateCancellation(cmd, format, resultType)
		}
		return failure(err.Error(), err)
	}

	logger.Info("创建符号链接", "real", normalizedReal, "fake", normalizedFake)
	if err := symlink.Create(normalizedReal, normalizedFake, backupResult.RemoveOpts); err != nil {
		if errors.Is(err, safeop.ErrOperationCancelled) {
			return renderCreateCancellation(cmd, format, resultType)
		}
		return failure(err.Error(), err)
	}

	// 文件操作已经成功，此后的绝对路径或 store 错误必须呈现失败结果并返回非零，但不得撤销已创建的链接
	absFakePath, err := pathutil.ToAbsolute(normalizedFake)
	if err != nil {
		persistenceErr := createPersistenceError("符号链接创建", fmt.Errorf("生成链接绝对路径失败: %w", err))
		return failure(persistenceErr.Error(), persistenceErr)
	}
	fields := map[string]string{
		"real": normalizedReal,
		"fake": absFakePath,
	}
	if err := persistCreateRecord(createDevice, "symlink", fields); err != nil {
		persistenceErr := createPersistenceError("符号链接创建", err)
		return failure(persistenceErr.Error(), persistenceErr)
	}

	return renderCreateResult(cmd, format, output.CreateResult{Success: true, Type: resultType, Message: "创建成功"}, nil)
}
