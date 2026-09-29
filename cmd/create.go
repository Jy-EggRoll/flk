package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var (
	createForce  bool
	createSmart  bool
	createDevice string
)

// createCmd 仅作为创建类命令的分组父节点
// 用户未指定 symlink、hardlink 或 copy 时展示帮助，不执行业务，也不会触发欢迎语或存储初始化
var createCmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"cr"},
	Short:   l10n.T("Create a link", nil),
	Long:    l10n.T("Create a link", nil),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(createCmd)
}

// 以下函数是 symlink、hardlink、copy 三个 create 叶子命令的共享实现
// 它们原先寄居在 cmd/symlink.go：共享代码放在某一个具体命令文件里会造成错误的归属暗示
// 后来者容易误以为这些能力只服务于 symlink，进而在其它命令里再写一份重复实现
// 统一收拢到 create 父命令所在的本文件后，create 系列的公共契约只有一个可寻址的位置
// 注意：本文件新增的任何共享函数都必须保持对三个叶子命令完全等价，不能只照顾某一个命令的语义

// newCreateFailure 为单个 create 叶子命令构造统一的失败渲染闭包
// 抽取理由：symlink、hardlink、copy 三处曾各写一份逐字相同的闭包，唯一差异只有 resultType 常量
// 三份副本一旦需要改动失败契约（例如补充字段或调整标记顺序）就必须同步三处，极易漏改
// 参数 cmd 与 format 由调用方在命令入口解析后传入，闭包只负责在失败时产出唯一的结构化结果
// 潜在影响点：闭包内先补 cause 再渲染结果，cause 为 nil 时用 errors.New(message) 兜底
// 这个兜底顺序不能颠倒，否则 renderCreateResult 会收到 nil 而丢掉根层区分已渲染错误的标记
func newCreateFailure(cmd *cobra.Command, format output.OutputFormat, resultType string) func(message string, cause error) error {
	return func(message string, cause error) error {
		if cause == nil {
			cause = errors.New(message)
		}
		return renderCreateResult(cmd, format, output.CreateResult{Success: false, Type: resultType, Error: message}, cause)
	}
}

// validateCreateDevice 校验 --device 的设备名是否合法，返回空串表示通过，否则返回本地化错误文案
// 抽取理由：三处 create 叶子命令都逐字重复了「含逗号或空格即拒绝」的判断，且还要各自取一次同样的文案
// 之所以返回字符串而不是 error：调用方需要把同一份文案同时用于结构化结果的 Error 字段与 errors.New 的 cause
// 若返回 error，调用方就不得不多做一次 .Error()，反而增加了三处各写一遍的转换代码
// 潜在影响点：设备名最终会作为 store 的 key 参与设备过滤，逗号与空格是记录分隔语义中的保留字符
// 该校验必须发生在任何日志与文件系统操作之前，保证非法输入不产生副作用
func validateCreateDevice(deviceName string) string {
	if strings.Contains(deviceName, ",") || strings.Contains(deviceName, " ") {
		return l10n.T("Device name must not contain commas or spaces", nil)
	}
	return ""
}

// renderCreateResult 把唯一的最终创建结果写入 Cobra stdout，并在业务失败已经成功渲染后附加根层可识别的标记
// 输出本身失败时不能标记为已渲染，否则根层会吞掉唯一可见错误
func renderCreateResult(cmd *cobra.Command, format output.OutputFormat, result output.CreateResult, operationErr error) error {
	if err := output.PrintCreateResult(cmd.OutOrStdout(), format, result); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to output the creation result", nil), err)
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
			Error:   l10n.T("Operation cancelled", nil),
		}, nil)
	}
	// 取消提示属于交互状态而非业务结果，写入 stderr 后仍保持零退出
	if _, err := io.WriteString(cmd.ErrOrStderr(), pterm.Info.Sprintln(l10n.T("Operation cancelled", nil))); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to output the cancellation result", nil), err)
	}
	return nil
}

// persistCreateRecord 只负责把已经完成的文件操作登记到根生命周期初始化好的全局 store
// Manager.AddRecord 当前是纯内存操作且无 error 返回；nil manager/data 是其唯一可预先识别的失败，Save 错误则原样上抛
func persistCreateRecord(device, linkType string, fields map[string]string) error {
	manager := store.GlobalManager
	if manager == nil || manager.Data == nil {
		return errors.New(l10n.T("Failed to add the record: the store is not initialized", nil))
	}
	manager.AddRecord(device, linkType, fields)
	if err := manager.Save(store.StorePath); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to save the record", nil), err)
	}
	return nil
}

// createPersistenceError 说明文件系统操作已经生效但记录阶段失败，明确告知调用者不会自动回滚
func createPersistenceError(action string, err error) error {
	return fmt.Errorf("%s", l10n.T("{{.Action}} completed, but {{.Err}}; the completed file operation was not rolled back", map[string]any{"Action": action, "Err": err.Error()}))
}
