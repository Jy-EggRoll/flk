package cmd

import (
	"errors"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/spf13/cobra"
)

/*
serveCmd 直接启动 WebUI 管理面板，不再有子命令

历史沿革（改造前的事实）：serve 原本只是个「父命令壳」，真正的服务挂在 serve config 下。
但该页面早已不限于「看配置文件」——它能查看与编辑清单、检测链接是否可用、一键修复失效链接，
后续还要支持解除链接，因此 config 这个名字既不准确，又平白多出一层无意义的嵌套；
当前 serve 下也只有这一个子命令，去掉这一层不会损失任何表达力。
若日后 serve 需要挂载真正独立的子命令（例如 serve status），再引入才不会显得多余。
*/
var serveCmd = &cobra.Command{
	Use:     "serve",
	Aliases: []string{"server"},
	Short:   l10n.T("Open the WebUI management panel in a browser", nil),
	Long:    l10n.T("Start an HTTP service and open the WebUI management panel in your browser.\nThe panel can view and edit the store file (flk-store.json), check whether links are valid, and repair the invalid ones.\nThe page keeps itself in sync when the store file is changed outside the browser.", nil),
	// serve 不接受任何位置参数（它没有子命令），必须显式拒绝
	// 为什么不能依赖 cobra 的默认行为：默认校验规则对「无子命令的命令」是 ArbitraryArgs，
	// 多余参数会被静默忽略并照常启动服务——于是已被移除的 `flk serve config` 会变成
	// 「启动服务并把 config 当空气」，用户以为改的仍是那个旧入口，实际行为却已完全不同，
	// 这类静默降级比直接报错危险得多
	//
	// 对旧入口的三个别名单独给一句引导：它们曾指向 serve 的子命令，改名后敲它们的人
	// 最需要的不是「unknown command」这种通用报错，而是「它去哪了」；
	// 其余位置参数仍然走 cobra 的默认报错，不额外加工
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			switch args[0] {
			case "config", "cfg", "c":
				return errors.New(l10n.T(`The "config" subcommand has been merged into serve; run "flk serve" directly`, nil))
			}
		}
		return cobra.NoArgs(cmd, args)
	},
	RunE: runServe,
}

func init() {
	// serve 依赖已加载的 store（页面要展示与编辑清单），但尚未定义机器可读的启动结果，
	// 因此只声明存储能力、不声明 JSON 能力
	MarkNeedsStore(serveCmd)
	rootCmd.AddCommand(serveCmd)

	// 四个 flag 都直接声明在 serve 上，而不是 PersistentFlags：
	// 持久化 flag 的意义是「供本命令及其全部子命令共享」，而 serve 已无子命令，
	// 用 Flags 声明语义更准确，也让帮助里的 Local Flags 段落如实反映归属
	serveCmd.Flags().IntP("port", "p", 8999, l10n.T("Port to listen on", nil))
	serveCmd.Flags().String("host", "127.0.0.1", l10n.T("Host to bind", nil))
	// allow-host 是访问白名单的显式入口
	// WebUI 能直接读写文件系统与清单，属于高危入口，因此默认只允许回环地址访问；
	// 要从局域网或自定义域名访问，必须在这里逐条列出，而不是靠「绑定了哪个地址就放行哪个地址」隐式开口子
	// 用 StringSlice 而非 String：同时支持 --allow-host a --allow-host b 与 --allow-host a,b 两种写法
	// 与设置文件的分工：全局设置文件 ~/.config/flk/flk-config.json 的 allowHosts 字段是它的等价物
	//（用 `flk config set allowHosts a,b` 或 `flk config reset allowHosts` 维护，不必手工编辑文件），
	// 两者是并集关系（见 runServe 里的白名单组装），命令行适合临时授权，设置文件适合长期生效
	// 潜在影响点：白名单条目只做「主机名/IP 字面量」比对（见 eggokit/webui 的 normalizeHost），
	// 因此填域名时该域名解析到哪个 IP 不受约束——这是用户显式授权的结果，不是绕过
	serveCmd.Flags().StringSlice("allow-host", nil,
		l10n.T("Extra hosts allowed to access the WebUI; the WebUI can operate on your files, so list only hosts you trust (repeatable or comma-separated)", nil))
	// --no-open 只影响是否自动拉起浏览器，服务本身照常启动，便于无人值守或远程调试
	serveCmd.Flags().Bool("no-open", false, l10n.T("Do not open the browser automatically", nil))
}
