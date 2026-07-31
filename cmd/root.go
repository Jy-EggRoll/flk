package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

const (
	// AnnotationNeedsStore 标记命令在进入 Run/RunE 前必须完成持久化存储初始化
	// 根生命周期只检查最终执行命令上的标记，避免 help、version、completion 等无关命令读取或创建存储文件
	AnnotationNeedsStore = "flk/needs-store"

	// AnnotationSupportsJSON 标记命令能够在 --output json 下保证 stdout 只包含结构化业务结果
	// 未声明该能力的命令若收到 JSON 请求会在业务函数执行前失败，防止普通文本伪装成 JSON 成功输出
	AnnotationSupportsJSON = "flk/supports-json"
)

const annotationEnabled = "true"

var (
	outputFormat string
	WorkDir      string

	// verboseCount 由 CountVarP 累加：-v 开启 Info，-vv 进一步开启 Debug
	// 业务命令始终按需记录日志，最终是否输出完全由 logger.Config 的级别过滤决定
	verboseCount int

	// windowsAdminChecker 由 main 注入，非 Windows 构建保持 nil
	// 回调只负责返回权限状态，所有展示均留在根生命周期内，确保使用统一 logger 和 stderr writer
	windowsAdminChecker func() bool
)

var rootCmd = &cobra.Command{
	Use:           "flk",
	Short:         "flk 是一个跨平台的文件链接管理工具",
	Long:          "flk 是一个跨平台的文件链接管理工具",
	SilenceErrors: true,
	SilenceUsage:  true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return prepareCommand(cmd)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		showVersion, err := cmd.Flags().GetBool("version")
		if err != nil {
			return err
		}
		if showVersion {
			return renderVersion(cmd.OutOrStdout())
		}
		return cmd.Help()
	},
}

// MarkNeedsStore 为命令声明存储依赖，并保留命令可能已有的其他 annotation
// 其他命令文件应在注册命令时调用该函数，而不是自行初始化 store，从而让初始化失败沿 Cobra 错误链返回到唯一退出边界
func MarkNeedsStore(command *cobra.Command) {
	setCommandAnnotation(command, AnnotationNeedsStore)
}

// MarkSupportsJSON 为命令声明结构化输出能力，并保留命令可能已有的其他 annotation
// 只有能够保证 JSON 模式下 stdout 不混入欢迎语、交互提示或普通文本的业务叶子命令才能调用该函数
func MarkSupportsJSON(command *cobra.Command) {
	setCommandAnnotation(command, AnnotationSupportsJSON)
}

// setCommandAnnotation 安全写入 Cobra annotation；命令对象在包初始化阶段均为单线程注册，因此这里不需要额外同步
func setCommandAnnotation(command *cobra.Command, key string) {
	if command.Annotations == nil {
		command.Annotations = make(map[string]string)
	}
	command.Annotations[key] = annotationEnabled
}

// hasCommandAnnotation 只读取最终执行命令自身的能力声明
// 不沿父链继承可避免把 create/serve 之类父命令的能力误授予未来新增但尚未适配的子命令
func hasCommandAnnotation(command *cobra.Command, key string) bool {
	return command.Annotations != nil && command.Annotations[key] == annotationEnabled
}

// renderedError 表示错误内容已经由业务命令以表格或 JSON 等形式写给用户
// Error 和 Unwrap 保留原始错误语义，外层仍可用 errors.Is/As 判断真实原因，而 Execute 不会再次打印同一错误
// 类型本身不导出，防止调用方依赖实现细节；构造与判断统一通过 MarkErrorRendered、IsErrorRendered 完成
type renderedError struct {
	cause error
}

func (err *renderedError) Error() string {
	return err.cause.Error()
}

func (err *renderedError) Unwrap() error {
	return err.cause
}

// MarkErrorRendered 包装一个已经完成用户可见渲染的错误
// nil 保持 nil，重复包装保持原值，便于命令在多个错误传播层安全调用
func MarkErrorRendered(err error) error {
	if err == nil || IsErrorRendered(err) {
		return err
	}
	return &renderedError{cause: err}
}

// IsErrorRendered 判断错误链中是否包含已渲染标记
// 使用 errors.As 可识别 fmt.Errorf("...: %w", err) 等继续包装后的错误链
func IsErrorRendered(err error) bool {
	var target *renderedError
	return errors.As(err, &target)
}

// SetWindowsAdminChecker 注入 Windows 管理员状态检查函数
// main_windows.go 只实现平台能力，根生命周期根据 bool 结果选择 Info/Warn，测试也可注入确定性回调
func SetWindowsAdminChecker(checker func() bool) {
	windowsAdminChecker = checker
}

// prepareCommand 完成所有业务命令共享且必须发生在 Run/RunE 之前的生命周期工作
// 顺序刻意固定为：输出设施初始化、参数语义校验、工作目录设置、按需 store、平台提示、欢迎语
// 任一步返回错误都会阻止业务函数执行，并由 Execute 在统一边界决定是否打印
func prepareCommand(command *cobra.Command) error {
	errWriter := command.ErrOrStderr()

	// 环境配置是基础层，显式 -v/-vv 在其上覆盖日志级别；所有日志强制跟随 Cobra 注入的 stderr，便于测试和重定向
	config, err := logger.FromEnv()
	if err != nil {
		fallback := logger.DefaultConfig()
		fallback.Writer = errWriter
		logger.Init(fallback)
		return err
	}
	if verboseCount > 0 {
		config = logger.ApplyVerbose(config, verboseCount)
	}
	config.Writer = errWriter
	logger.Init(config)

	// pterm 的交互确认、文本输入和选择器通过包级默认 writer 绘制；统一切到 stderr，避免提示符污染 stdout 业务数据
	pterm.SetDefaultOutput(errWriter)

	// 保留历史容错语义：非法 output 不终止命令，而是警告后回退 table
	if outputFormat != "json" && outputFormat != "table" {
		logger.Warn("未知的输出格式，已回退为 table", "output", outputFormat)
		outputFormat = "table"
	}

	// JSON 是显式能力而不是所有命令的默认承诺，必须在任何业务副作用及欢迎语之前拒绝未标记命令
	if outputFormat == "json" && !hasCommandAnnotation(command, AnnotationSupportsJSON) {
		return fmt.Errorf("命令 %q 不支持 JSON 输出", command.CommandPath())
	}

	pathutil.SetWorkDir(WorkDir)

	// 只有声明存储依赖的最终业务命令才会读取或创建 store；失败必须阻止业务继续，不能只记日志后带着 nil manager 运行
	if hasCommandAnnotation(command, AnnotationNeedsStore) {
		if err := store.InitStore(store.StorePath); err != nil {
			return fmt.Errorf("初始化存储失败: %w", err)
		}
	}

	// 平台回调不直接输出，确保权限提示服从日志级别并始终写入当前命令的 stderr
	if windowsAdminChecker != nil {
		if windowsAdminChecker() {
			logger.Info("当前以管理员权限运行")
		} else {
			logger.Warn("当前未以管理员权限运行")
		}
	}

	// 欢迎语仅属于真实执行的 table 业务叶子命令；help/completion/version、root 与仅展示帮助的父命令均不会触发
	if outputFormat == "table" && isBusinessLeaf(command) {
		_, _ = fmt.Fprintln(errWriter, "欢迎使用 flk！")
	}
	return nil
}

// isBusinessLeaf 排除 Cobra 的辅助命令、版本入口和仍有可用子命令的父节点
// 这里按完整祖先链识别 completion，覆盖 completion bash/zsh 等实际执行叶子，避免生成脚本时混入欢迎语
func isBusinessLeaf(command *cobra.Command) bool {
	if command.Parent() == nil || command.Name() == "version" || command.HasAvailableSubCommands() {
		return false
	}
	for current := command; current != nil; current = current.Parent() {
		switch current.Name() {
		case "completion", "help", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return false
		}
	}
	return command.Run != nil || command.RunE != nil
}

// Execute 是 CLI 的唯一 Cobra 执行边界，返回进程退出码但绝不自行终止进程
// Cobra 自身错误和未渲染业务错误在此恰好打印一次；结构化输出已经呈现的错误只返回非零码，避免重复污染 stdout/stderr
func Execute() int {
	err := rootCmd.Execute()
	if err == nil {
		return 0
	}
	if !IsErrorRendered(err) {
		_, _ = fmt.Fprintln(rootCmd.ErrOrStderr(), err)
	}
	return 1
}

func init() {
	if wd, err := os.Getwd(); err == nil {
		WorkDir = wd
	} else {
		WorkDir = "."
	}

	rootCmd.PersistentFlags().StringVar(
		&store.StorePath,
		"store-path",
		store.DefaultStorePath,
		"用于存放 flk-store.json 的路径",
	)
	rootCmd.PersistentFlags().StringVar(&outputFormat, "output", "table", "输出格式: json/table")
	rootCmd.PersistentFlags().StringVarP(&WorkDir, "work-dir", "w", WorkDir, "工作目录，作为存储和路径计算的基准")
	rootCmd.PersistentFlags().CountVarP(&verboseCount, "verbose", "v", "增加日志详细程度（-v 为 Info，-vv 为 Debug）")
	rootCmd.Flags().Bool("version", false, "显示版本信息")

}
