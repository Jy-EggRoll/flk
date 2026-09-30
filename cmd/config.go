// config.go 实现 "flk config" 及其子命令：查看、按项读写、重置、体检。
//
// 本子树刻意遮蔽 root 的 PersistentPreRunE（见 configCmd 的说明），因此这里
// **不得依赖任何由根生命周期准备的东西**：全局 store 未初始化、工作目录未被设置、
// 日志级别仍是包内默认值。所有读写都直接落到设置文件（internal/config），
// 这也正是这些命令想要的行为——它们要能在其它一切都不可用时工作。
//
// 输出分成两条通道：
//   - stdout 只放数据（JSON、生效值、路径、体检结果），**全程不走 pterm**：
//     pterm 不检测 TTY，会把 ANSI 转义写进管道，让 `flk config show | jq` 之类的用法直接失败
//   - stderr 放状态提示（"已写入""已重置"与错误），与 fix/unlink 的做法一致
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jy-eggroll/flk/internal/config"
	"github.com/jy-eggroll/flk/internal/prompt"
	"github.com/jy-eggroll/flk/pkg/l10n"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

// configCmd 是 "flk config" 子树，裸执行等同于 show。
var configCmd = &cobra.Command{
	Use:   "config",
	Short: l10n.T("Show and edit the settings", nil),
	Long: l10n.T(`Show and edit flk's settings.

Examples:
  flk config                          Show the current settings
  flk config get <key>                Print the effective value of one setting
  flk config set <key> <value>        Set a value
  flk config reset <key>              Remove a setting so its default applies
  flk config reset --all              Delete the settings file
  flk config validate                 Check the settings file for problems

Run "flk config --help" for the list of available keys.`, nil),
	Args: cobra.NoArgs,
	RunE: runConfigShow,
	// 遮蔽 root 的 PersistentPreRunE。root 的实现在所有子命令前初始化日志并按需装载 store，
	// 而它读取设置文件失败时会让命令直接失败——于是"最该报出问题的 validate"和
	// "唯一能救命的 reset --all"都会在 RunE 之前被挡下，用户只能手工删文件。
	// cobra 只执行向上找到的第一个 PersistentPreRunE，挂个自己的实现即可遮蔽。
	//
	// 这里仍做两件本子树必须的准备（它们不依赖设置文件，因此不会重现上面那个死结）：
	//   - pterm 的输出切到 stderr：reset --all 的确认提示是唯一用到 pterm 的地方，
	//     不切就会把提示写进 stdout，破坏本子树"stdout 只放数据"的约定
	//   - 把全局 --yes 同步进 prompt：不然 `flk config reset --all --yes` 的 --yes 会被忽略，
	//     在脚本里表现为"明明加了 --yes 却报错要求交互"
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		pterm.SetDefaultOutput(cmd.ErrOrStderr())
		prompt.Configure(assumeYes)
		return nil
	},
}

// configShowCmd 与裸 "flk config" 等价，提供明确的 show 子命令。
var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: l10n.T("Show the current settings", nil),
	Args:  cobra.NoArgs,
	RunE:  runConfigShow,
}

// configPathCmd 打印设置文件的路径。
var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: l10n.T("Show the path to the settings file", nil),
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := config.DefaultPath()
		if err != nil {
			return err
		}
		// 走裸 fmt 而非 pterm：路径经常被脚本直接取用
		fmt.Fprintln(cmd.OutOrStdout(), path)
		return nil
	},
}

// configGetCmd 打印某项设置的生效值。
var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: l10n.T("Print the effective value of one setting", nil),
	Long: l10n.T(`Print the value of one setting as it takes effect at runtime.

The value comes from the settings file with defaults filled in. Command-line
overrides such as --lang are NOT taken into account, since they only apply to the
current run.

Output has no colors and is meant to be piped: scalars are printed as-is, and
list values are printed one entry per line.

Examples:
  flk config get language
  flk config get allowHosts`, nil),
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		value, err := config.Get(args[0])
		if err != nil {
			if errors.Is(err, config.ErrUnknownKey) {
				return errUnknownConfigKey(args[0])
			}
			return err
		}
		printConfigValue(cmd, value)
		return nil
	},
}

// configSetCmd 校验并写入单个设置项。
var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: l10n.T("Set a value in the settings file", nil),
	Long: l10n.T(`Validate and write one setting into the settings file.

Other keys in the file are left untouched, including keys flk does not know about.

Examples:
  flk config set language zh-CN
  flk config set logLevel debug
  flk config set allowHosts 192.168.1.5,my.dev.lan`, nil),
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, value := args[0], args[1]

		s, ok := config.Lookup(key)
		if !ok {
			return errUnknownConfigKey(key)
		}

		// 校验与写入共用注册表里的同一个解析器，因此"set 接受什么"与
		// "config validate 认可什么"不可能出现分歧
		parsed, err := s.Parse(value)
		if err != nil {
			return errInvalidConfigValue(s.Key, value, s.Expected)
		}
		if err := config.SetKey(s.Key, parsed); err != nil {
			return err
		}

		configSuccess(cmd, l10n.T("{{.Key}} = {{.Value}}", map[string]any{"Key": s.Key, "Value": config.ValueText(parsed)}))
		if s.Key == "language" {
			// 语言在进程启动时就由 l10n.Init 定下了，改设置不会影响当前这次输出。
			// 刻意不在这里重新 Init：那会违反 l10n 包"Init 之后语言只通过 SetLanguage 变"的契约
			configInfo(cmd, l10n.T("The new language takes effect on the next run", nil))
		}
		if s.Key == "logLevel" {
			// 同理：日志级别在 prepareCommand 里就已落到 logger 实例上，本次运行不受影响
			configInfo(cmd, l10n.T("The new log level takes effect on the next run", nil))
		}
		return nil
	},
}

// configResetCmd 重置单项或全部设置。
var configResetCmd = &cobra.Command{
	Use:   "reset [key]",
	Short: l10n.T("Reset one setting, or the whole settings file", nil),
	Long: l10n.T(`Reset settings.

With a key, the value is REMOVED from the file, so the setting falls back to its
built-in default; with --defaults the default value is written explicitly instead.

Without a key the same choice applies to everything: the settings file is deleted,
or with --defaults it is rewritten with default values only. Either way the
language, the allowed hosts of the WebUI and the log level are all cleared, so the
file is deleted only after a confirmation.

Examples:
  flk config reset language              Remove the key from the file
  flk config reset language --defaults   Write the default value instead
  flk config reset --defaults --yes      Rewrite the file with default values only
  flk config reset --all                 Delete the settings file
  flk config reset --all --yes           Skip the confirmation prompt`, nil),
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		// key 与 --all 同时给出属于语义冲突（"只重置这一个"与"重置全部"），直接拒绝，
		// 避免"我明明指定了 key，怎么把整个设置文件删了"这类误解。
		// 两者都不给时按"整体重设"处理：于是 `flk config reset --defaults --yes`
		// 与显式写明意图的 `flk config reset --all --defaults --yes` 完全等价，
		// 用户不必先记住 --all 这个存在感很低的修饰词
		if all && len(args) == 1 {
			return errors.New(l10n.T("Specify either a key or --all, but not both", nil))
		}
		writeDefaults, _ := cmd.Flags().GetBool("defaults")
		if len(args) == 0 {
			return resetAllSettings(cmd, writeDefaults)
		}
		return resetOneSetting(cmd, args[0], writeDefaults)
	},
}

// configValidateCmd 体检设置文件。
var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: l10n.T("Check the settings file for problems", nil),
	Long: l10n.T(`Check the settings file and report anything that would be silently ignored,
silently replaced by a default, or that makes the file unreadable.

Output has no colors so it can be consumed by scripts. The exit code is 1 when at
least one error is found.

Examples:
  flk config validate`, nil),
	Args: cobra.NoArgs,
	RunE: runConfigValidate,
}

// runConfigShow 以 JSON 打印当前生效的设置。
//
// 输出只有 JSON 一份文档，人看的补充信息（文件路径）走 stderr：
// 把标题或路径混进 stdout 会让 `flk config show | jq` 直接失败，而这正是该子命令的主要用法
func runConfigShow(cmd *cobra.Command, args []string) error {
	// 不读任何全局状态（本子树遮蔽了 PersistentPreRunE，全局 store 与工作目录都未初始化），
	// 直接从设置文件重新加载，语义与 get 保持一致：只看设置文件 + 补默认值
	settings, err := config.Load()
	if err != nil {
		configFail(cmd, l10n.T("Cannot read the settings file: {{.Err}}", map[string]any{"Err": err}))
		configInfo(cmd, l10n.T("Run \"flk config validate\" to see what is wrong", nil))
		// 已经打印过原因，包一层让 Execute 不再重复输出同一件事
		return MarkErrorRendered(err)
	}

	// 结构体序列化的字段顺序是稳定的（按 Config 的字段顺序），因此输出可以直接进 diff
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(encoded))

	if path, pathErr := config.DefaultPath(); pathErr == nil {
		fmt.Fprintln(cmd.ErrOrStderr(), l10n.T("Config file: {{.Path}}", map[string]any{"Path": path}))
	}
	return nil
}

// resetOneSetting 重置单个设置项。
func resetOneSetting(cmd *cobra.Command, key string, writeDefault bool) error {
	s, ok := config.Lookup(key)
	if !ok {
		return errUnknownConfigKey(key)
	}

	defaultText := config.ValueText(s.Default)
	if writeDefault {
		if err := config.SetKey(s.Key, s.Default); err != nil {
			return err
		}
		configSuccess(cmd, l10n.T("{{.Key}} was reset to its default ({{.Value}})",
			map[string]any{"Key": s.Key, "Value": defaultText}))
		return nil
	}

	if err := config.UnsetKey(s.Key); err != nil {
		return err
	}
	configSuccess(cmd, l10n.T("{{.Key}} was removed from the settings file; the default ({{.Value}}) now applies",
		map[string]any{"Key": s.Key, "Value": defaultText}))
	return nil
}

// resetAllSettings 重置整份设置：删除文件，或写入一份全默认值的文件。
//
// 确认链路刻意不复用命令自己的参数，而是走全局 --yes + prompt.Confirm：
// 本子树不声明自己的 --yes，全局那个持久化 flag 就能正常生效（它绑定的是根命令的变量，
// 若在这里再声明一个同名局部 flag，局部会遮蔽继承来的那个，表现为"加了 --yes 却仍要求交互"），
// 而 prompt.Confirm 已经统一处理了"非交互环境立即报错而不是挂起"这一条
func resetAllSettings(cmd *cobra.Command, writeDefaults bool) error {
	path, err := config.DefaultPath()
	if err != nil {
		return err
	}

	// 删除是不可逆的，必须先让损失可见并取得确认。
	// 写默认值这条路径是安全操作（结果与"从未配置过"等价），不需要确认
	if !writeDefaults {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			configInfo(cmd, l10n.T("There is no settings file to delete: {{.Path}}", map[string]any{"Path": path}))
			return nil
		}
		// --yes 由 configCmd 的 PersistentPreRunE 同步进 prompt；未加 --yes 且 stdin 不是终端时
		// prompt.Confirm 直接报错并提示改用 --yes，绝不阻塞（它读 stdin 而不是 /dev/tty）
		confirmed, confirmErr := prompt.Confirm(
			l10n.T("Delete {{.Path}} and reset every setting to its default?", map[string]any{"Path": path}), false)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			configInfo(cmd, l10n.T("Aborted; nothing was changed", nil))
			return nil
		}
	}

	if err := config.ResetAll(writeDefaults); err != nil {
		return err
	}

	if writeDefaults {
		configSuccess(cmd, l10n.T("Wrote a settings file with default values: {{.Path}}", map[string]any{"Path": path}))
		return nil
	}
	configSuccess(cmd, l10n.T("Deleted the settings file: {{.Path}}", map[string]any{"Path": path}))
	return nil
}

// runConfigValidate 体检设置文件。
func runConfigValidate(cmd *cobra.Command, args []string) error {
	path, err := config.DefaultPath()
	if err != nil {
		return err
	}

	// 文件不存在不是问题：默认设置本来就允许不存在。这一条**必须能跑到**，
	// 也正因为如此本子树才要遮蔽 PersistentPreRunE（否则读文件失败时连体检都执行不了）
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		fmt.Fprintln(cmd.OutOrStdout(), l10n.T("No settings file at {{.Path}}; flk is running on built-in defaults",
			map[string]any{"Path": path}))
		return nil
	}

	issues, err := config.ValidateAt(path)
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), l10n.T("No problems found in {{.Path}}", map[string]any{"Path": path}))
		return nil
	}

	errorCount := 0
	for _, issue := range issues {
		if issue.Level == config.LevelError {
			errorCount++
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", levelTag(issue.Level), issue.Message)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), l10n.T("{{.Errors}} error(s), {{.Warnings}} warning(s)",
		map[string]any{"Errors": errorCount, "Warnings": len(issues) - errorCount}))

	if errorCount > 0 {
		// 逐条问题已经打印过，这里只负责让退出码非零。
		// 用 MarkErrorRendered 包一层，Execute 便不会把这件事再打印一遍
		//（否则终端会多出一段与上面重复的红字）。文案因此只用于内部诊断，保持英文即可
		return MarkErrorRendered(fmt.Errorf("config validate: %d problem(s) found in %s", errorCount, path))
	}
	return nil
}

// errUnknownConfigKey 生成"未知键"的统一提示，顺带告诉用户怎么列出全部键。
// 所有入口（get / set / reset）共用它，因此提示口径必然一致
func errUnknownConfigKey(key string) error {
	return errors.New(l10n.T("Unknown config key: {{.Key}} (run \"flk config --help\" to see the available keys)",
		map[string]any{"Key": key}))
}

// errInvalidConfigValue 生成"值非法"的统一提示。
//
// 所有键的值错误都收敛到这一条模板（合法取值由注册表的 Expected 描述），
// 中英双语各只需一条文案；若让每个键各写一段专属散文，文案数量会随校验规则数线性增长。
//
// 这里用 Retranslate 而不是 T 来翻译 Expected：Expected 是运行期变量，
// 而提取工具要求 .T(...) 的首参必须是字符串字面量（消息 id 即原文），
// 用 T 会让 l10n:check 直接报错。Retranslate 就是为"翻译一个已知消息 id"准备的入口，
// 且不会被提取工具当成新消息。因此 zh-CN 下这句"期望形式"同样是中文
func errInvalidConfigValue(key, value, expected string) error {
	return errors.New(l10n.T("Invalid value for {{.Key}}: {{.Value}} (expected {{.Expected}})",
		map[string]any{"Key": key, "Value": value, "Expected": l10n.Retranslate(expected, nil)}))
}

// levelTag 返回体检条目的级别标记。
// 刻意用固定 ASCII 且不翻译：validate 面向脚本，可 grep 的稳定性比本地化更重要
func levelTag(level config.Level) string {
	if level == config.LevelError {
		return "[error]  "
	}
	return "[warning]"
}

// printConfigValue 以脚本友好的形式打印设置值：标量裸值、列表每行一项、空列表不输出。
// 全程走裸 fmt，不经 pterm 着色
func printConfigValue(cmd *cobra.Command, value any) {
	switch list := value.(type) {
	case []string:
		for _, item := range list {
			fmt.Fprintln(cmd.OutOrStdout(), item)
		}
	case []any:
		for _, item := range list {
			fmt.Fprintln(cmd.OutOrStdout(), config.ValueText(item))
		}
	default:
		fmt.Fprintln(cmd.OutOrStdout(), config.ValueText(value))
	}
}

// configSuccess / configInfo / configFail 是本子树的状态提示出口。
//
// 三条都写 stderr：本子树的 stdout 是数据通道（JSON、生效值、路径、体检结果），
// 状态提示混进去会让 `flk config show | jq`、`flk config get language | xargs` 直接失败。
// 显式指定 writer 而不是依赖 pterm 的全局默认输出，理由与 fix/unlink 相同：
// 不依赖"某个更早的钩子是否跑过"这种隐形前提
func configSuccess(cmd *cobra.Command, message string) {
	pterm.Success.WithWriter(cmd.ErrOrStderr()).Println(message)
}

func configInfo(cmd *cobra.Command, message string) {
	pterm.Info.WithWriter(cmd.ErrOrStderr()).Println(message)
}

func configFail(cmd *cobra.Command, message string) {
	pterm.Error.WithWriter(cmd.ErrOrStderr()).Println(message)
}

func init() {
	configResetCmd.Flags().Bool("all", false,
		l10n.T("Reset the whole settings file instead of a single key", nil))
	configResetCmd.Flags().Bool("defaults", false,
		l10n.T("Write the default value instead of removing the setting", nil))

	configCmd.AddCommand(
		configShowCmd,
		configPathCmd,
		configGetCmd,
		configSetCmd,
		configResetCmd,
		configValidateCmd,
	)
	rootCmd.AddCommand(configCmd)
}
