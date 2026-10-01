package cmd

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jy-eggroll/flk/internal/config"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

/*
本文件实现 serveCmd 的服务端：以网页形式展示并编辑 flk-store.json 的内容
通过 SSE 推送文件变更事件，实现浏览器端实时更新

历史沿革：本文件原名 serve_config.go，服务曾挂在 serve config 子命令下；
该子命令已整体并入 serve（详见 cmd/serve.go 中 serveCmd 的注释），因此文件与命令一起更名，
但页面自身的文件仍叫 cmd/ui/config.html——它展示的正是「配置清单」，这个文件名依然准确，无需跟着改
*/

//go:embed ui/config.html
var configHTML []byte

// servedConfigHTML 把**当前**语言注入 WebUI 页面后返回，因此必须在每次请求时现算。
//
// WebUI 是静态资源，其文案不经过 Go 源码的 l10n 提取管线（该管线只扫描 .go），
// 因此页面内自带一份以英文源串为 key 的翻译表，Go 端只负责把当前语言写进
// window.__FLK_LANG__，由前端自行切换；这样后端无需感知页面里有哪些文案。
//
// 两个占位符：
//   - __FLK_LANG_VALUE__：<script> 里的语言常量，供页面内翻译表选语言
//   - __FLK_HTML_LANG__：<html lang> 属性，供浏览器选字体、断词与拼读规则
//     （它不影响页面文案，但硬编码成 zh-CN 时英文界面在无障碍工具里会被读成中文）
//
// 为什么每次请求都要重新渲染而不是启动时烧一次：语言可以在运行期切换
// （见 /api/language），烧一次的话切换后刷新页面拿到的还是旧语言；
// 又因为 configHTML 是共享的只读切片、bytes.Replace 会返回新切片，
// 这个渲染过程本身不修改共享数据，天然可并发
func servedConfigHTML() []byte {
	lang := l10n.Current()
	out := bytes.Replace(configHTML, []byte("__FLK_HTML_LANG__"), []byte(lang), 1)
	return bytes.Replace(out, []byte("__FLK_LANG_VALUE__"), []byte(lang), 1)
}

// storeRev 计算 store 文件的版本标识，用于前端区分「本页保存」与「外部修改」
// 返回空串表示文件当前不可读，调用方需容忍
func storeRev() string {
	normalizedPath, err := pathutil.NormalizePath(store.StorePath)
	if err != nil {
		return ""
	}
	fi, err := os.Stat(normalizedPath)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.ModTime().UnixNano(), fi.Size())
}

// 三类 SSE 事件名。channel 里传递的就是事件名本身，SSE 协程据此组装负载
//
// 为什么需要三种事件：updated 表达「store 文件变了」，repaired 表达「文件系统里的链接状态变了」，
// language 表达「服务端语言变了」。前端对 updated 的处理是「比对 rev，相同即判定为本页自己保存并忽略」，
// 而修复链接与切换语言都完全不改清单文件，rev 不会变化，若沿用 updated 就会被所有页面静默忽略，
// 页面上的有效性徽标会一直停在修复前的旧状态、注入的语言也停留在切换前
const (
	sseEventUpdated  = "updated"
	sseEventRepaired = "repaired"
	sseEventLanguage = "language"
)

// sseHub 管理 SSE 客户端连接，用于广播变更事件
//
// lastRev 记录最近一次变更的 store 版本号：前端靠它判断「这次事件是不是我自己保存引起的」，
// 从而避免自己写盘后触发一次多余的整表重载。版本号放在 hub 上而不是塞进 channel，
// 是因为 channel 只承担「有变更」这一信号，即便多次变更被合并成一次唤醒，
// 客户端读到的也始终是最新版本，语义上更稳
type sseHub struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
	lastRev string
}

func newSSEHub() *sseHub {
	return &sseHub{clients: make(map[chan string]struct{})}
}

func (h *sseHub) register() chan string {
	ch := make(chan string, 1)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *sseHub) unregister(ch chan string) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

// broadcast 在持锁状态下把一次事件投递给所有客户端
// 容量为 1 的 channel 刻意不阻塞：客户端尚未消费上一次事件时，本次事件被合并丢弃，
// 因为每次唤醒都会重新从 hub 读取最新状态（版本号或纯粹的重检信号），丢中间事件不影响最终一致性
func (h *sseHub) broadcast(event string) {
	for ch := range h.clients {
		select {
		case ch <- event:
		default:
		}
	}
}

// notify 广播一次 store 文件变更（updated），并记住本次变更对应的 store 版本
func (h *sseHub) notify(rev string) {
	h.mu.Lock()
	h.lastRev = rev
	h.broadcast(sseEventUpdated)
	h.mu.Unlock()
}

// notifyRepaired 广播一次「链接状态已变化」事件（repaired）
//
// 与 notify 的分工：修链只动文件系统、不动清单，store 版本不变，
// 前端会把携带相同 rev 的 updated 当成自己的保存而忽略，因此必须用独立事件名，
// 让所有打开的页面重新拉取 /api/check 刷新有效性徽标
//
// 潜在影响点：本函数刻意不写 lastRev——它不代表清单发生了变更，
// 改动 lastRev 会让前端把后续真正的外部文件变更误判成「本页自己保存」而漏掉一次重载
func (h *sseHub) notifyRepaired() {
	h.broadcastEphemeral(sseEventRepaired)
}

// notifyLanguage 广播一次「服务端语言已切换」事件（language）
//
// 与 notifyRepaired 同族：语言切换同样不动清单，因此不能借 updated 之名广播
// （rev 没变，所有页面都会把它当成"自己的保存"忽略掉）。前端收到后整页重载，
// 让页面文案、注入的语言、命令树三者一次对齐
func (h *sseHub) notifyLanguage() {
	h.broadcastEphemeral(sseEventLanguage)
}

// broadcastEphemeral 广播一次「不改动清单」的事件（repaired / language）
//
// 抽出来只为让两个调用方共用同一份"必须持锁、且不得写 lastRev"的知识：
// 第二个事件类型若复制一遍实现，很容易漏掉锁或顺手写一次 lastRev
func (h *sseHub) broadcastEphemeral(event string) {
	h.mu.Lock()
	h.broadcast(event)
	h.mu.Unlock()
}

// currentRev 读取最近一次变更的版本号，供 SSE 连接协程组装事件负载
func (h *sseHub) currentRev() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastRev
}

// runServe 启动 WebUI 服务并阻塞到服务退出，是 serve 命令的唯一执行入口
//
// 访问白名单由三部分组成（回环地址由 guard 内置，不在此列出）：
//   - 服务实际绑定的地址（--host）
//   - --allow-host 逐条列出的地址（临时授权）
//   - 设置文件 allowHosts 字段列出的地址（长期生效，见 internal/config）
//
// 前两者与第三者是并集：任意一处列出的主机都放行，用户不必为了长期生效而每次都敲一遍命令行
func runServe(cmd *cobra.Command, args []string) error {
	// 从自身 flag 获取网络配置（serve 已无子命令，flag 全部声明在 serveCmd 上）
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")
	noOpen, _ := cmd.Flags().GetBool("no-open")
	// 访问白名单的显式部分，与绑定地址、设置文件一起交给 guard（回环地址由 guard 内置）
	allowHosts, _ := cmd.Flags().GetStringSlice("allow-host")

	// 设置文件里的白名单属于可选的长期授权，读取失败只警告不中止：
	// 一个损坏的设置文件不该让整个 WebUI 起不来，那样用户连「改回配置文件」的界面都打不开；
	// 降级后仍可用 --allow-host 完成本次授权，且 language 等其它字段的读取路径（LoadLanguage）本就各自容错
	//
	// 走 pterm 而不是 logger：这条是启动时面向用户的提示，与紧随其后的服务地址、白名单同属一屏正常输出；
	// 走 logger 会渲染成 logfmt 行，与 pterm 的信息行混成两种风格（约定见 internal/logger 的包注释）
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		pterm.Warning.WithWriter(cmd.ErrOrStderr()).Println(l10n.T("Failed to read the settings file; its allowHosts list will be ignored: {{.Err}}", map[string]any{"Err": cfgErr.Error()}))
	}
	var fileAllowHosts []string
	if cfg != nil {
		fileAllowHosts = cfg.AllowHosts
	}

	// 显式构造新切片而不是链式 append：append 在容量足够时会就地改写底层数组，
	// 而 allowHosts 是 flag 返回的切片，就地改写它可能影响同一进程内后续对该 flag 值的读取
	allowedHosts := make([]string, 0, 1+len(allowHosts)+len(fileAllowHosts))
	allowedHosts = append(allowedHosts, host)
	allowedHosts = append(allowedHosts, allowHosts...)
	allowedHosts = append(allowedHosts, fileAllowHosts...)

	// 端口自动顺延：从指定端口开始尝试，被占用则依次 +1，最多尝试 100 次
	listener, usedPort, err := listenWithRetry(host, port, 100)
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Could not find an available port (tried {{.From}} to {{.To}})", map[string]any{"From": port, "To": port + 99}), err)
	}

	addr := fmt.Sprintf("%s:%d", host, usedPort)
	hub := newSSEHub()

	// 端点之间共享的依赖收进 serveServer：hub 用于广播变更事件，三把锁用于串行化各自的临界区
	//
	// 三把锁的用途、归属，以及「为什么光靠 store 自己的锁不够」「为什么必须随服务实例存活、
	// 刻意不用包级变量」的完整理由，都随端点实现迁到了 serve_api.go 中 serveServer 的字段说明
	//
	// 锁在此处逐次新建：生命周期与本次服务实例严格一致，避免同进程内再次调用 runServe（测试里就会）时
	// 两代服务共用一把锁；watchStoreFile 也传入同一把 writeMu（见其函数注释）
	srv := &serveServer{
		hub:        hub,
		writeMu:    new(sync.Mutex),
		repairMu:   new(sync.Mutex),
		languageMu: new(sync.Mutex),
	}

	// 启动文件变更监听（轮询方式，每秒检查一次文件的修改时间）
	go watchStoreFile(hub, srv.writeMu)

	mux := http.NewServeMux()

	// 逐个注册各端点；端点逻辑集中在 serve_api.go 中对应的 serveServer 方法上
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/config", srv.handleConfig)
	mux.HandleFunc("/api/meta", srv.handleMeta)
	mux.HandleFunc("/api/check", srv.handleCheck)
	mux.HandleFunc("/api/repair", srv.handleRepair)
	mux.HandleFunc("/api/unlink", srv.handleUnlink)
	mux.HandleFunc("/api/language", srv.handleLanguage)
	mux.HandleFunc("/api/events", srv.handleEvents)

	// 非回环绑定意味着同网段任何人都能打开这个 WebUI，而 WebUI 可以直接改写清单文件，
	// 因此启动时必须明确警告；绑定回环地址（默认）时保持安静，不打扰用户
	// 与上面两条同理走 pterm：它是启动摘要的一部分，用户必须在同一屏里看到它，而不是一条 logfmt 行
	if !isLoopbackHost(host) {
		pterm.Warning.WithWriter(cmd.ErrOrStderr()).Println(l10n.T("The WebUI is bound to {{.Host}}, a non-loopback address; anyone who can reach this port can read and edit your store file", map[string]any{"Host": host}))
	}

	// 把「当前生效的完整白名单」与地址一起打印出来：
	// 白名单现在有三个来源（绑定地址、--allow-host、设置文件 allowHosts），用户无法只凭命令行判断
	// 设置文件里的条目是否真的被读到（写错字段名、拼错主机名都不会报错），打印是唯一的确认手段
	// 展示内容直接取自 buildAllowedHosts（guard 判定用的同一份实现），因此不会出现「打印一套、实际放行另一套」
	//
	// 为什么走 cmd.OutOrStdout() 而不是 logger.Info：logger 的默认级别是 Warn（见 internal/logger/config.go），
	// Info 级日志默认被过滤掉，而这条信息与下面的服务地址一样属于「必须默认可见」的启动摘要；
	// 两行合并成一次写入，只保留一个写失败分支，避免为第二行再复制一遍同样的错误处理
	startupSummary := l10n.T("Service started: http://localhost:{{.Port}}", map[string]any{"Port": usedPort}) + "\n" +
		l10n.T("Allowed hosts for this session: {{.Hosts}}", map[string]any{"Hosts": strings.Join(allowedHostDisplay(allowedHosts), ", ")})

	logger.Info(l10n.T("Starting service", nil), "addr", addr)
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), startupSummary); err != nil {
		_ = listener.Close()
		return fmt.Errorf("%s: %w", l10n.T("Failed to output the service address", nil), err)
	}

	if !noOpen {
		tryOpenBrowser(fmt.Sprintf("http://localhost:%d", usedPort))
	}

	// 统一在入口处套上护栏：Host 校验挡 DNS rebinding、Origin/Referer 校验挡 CSRF、body 上限挡超大请求
	// 白名单在函数开头就已组装成 allowedHosts：绑定地址 + --allow-host + 设置文件 allowHosts（回环地址由 guard 内置）
	// 绑定地址必须放行：--host 192.168.1.5 时用户从地址栏访问用的就是它，否则自己都打不开页面
	// 注意绑定通配地址（0.0.0.0 / ::）时它不是一个能出现在 Host 头里的主机名，不会被任何请求命中，
	// 因此要通过局域网访问必须显式绑定具体 IP，或把该 IP 写进 --allow-host / 设置文件——这是刻意收紧的取向
	if err := http.Serve(listener, guard(mux, allowedHosts)); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Service failed to run", nil), err)
	}
	return nil
}

// listenWithRetry 从 startPort 开始依次尝试端口，成功时返回 listener 和实际使用的端口
func listenWithRetry(host string, startPort, maxAttempts int) (net.Listener, int, error) {
	for i := 0; i < maxAttempts; i++ {
		port := startPort + i
		addr := fmt.Sprintf("%s:%d", host, port)
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			return listener, port, nil
		}
		logger.Debug(l10n.T("Port in use, trying the next one", nil), "port", port)
	}
	return nil, 0, fmt.Errorf("%s", l10n.T("Ports {{.From}}-{{.To}} are all in use", map[string]any{"From": startPort, "To": startPort + maxAttempts - 1}))
}

// watchStoreFile 轮询检查 store 文件的修改时间，有变化时刷新全局存储并通知 SSE 客户端
//
// writeMu 与 POST /api/config 共用（即 serveServer.writeMu，传入的正是同一把锁，
// 其用途与归属见 cmd/serve_api.go 中 serveServer 的字段说明）：
// 「读磁盘 → 发布」必须与「改内存 → 写磁盘」互斥，否则轮询可能读到 POST 尚未落盘的旧内容
// 并把它发布回来，把用户刚保存的数据在内存里回滚掉。
// LoadFromFile 在旧格式迁移分支里还会回写文件，本身也属于写路径，更不该与 POST 交错，
// 因此整段加载 + 发布都放在锁内，不做「先无锁加载、再加锁发布」的拆分
func watchStoreFile(hub *sseHub, writeMu *sync.Mutex) {
	normalizedPath, err := pathutil.NormalizePath(store.StorePath)
	if err != nil {
		logger.Error(l10n.T("Failed to resolve the store path", nil), "error", err)
		return
	}

	var lastModTime time.Time
	var lastSize int64

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		fi, err := os.Stat(normalizedPath)
		if err != nil {
			continue
		}
		modTime := fi.ModTime()
		size := fi.Size()
		if modTime.Equal(lastModTime) && size == lastSize {
			continue
		}
		lastModTime = modTime
		lastSize = size

		// 文件有变化，重新加载并整体替换全局实例
		// 直接 SetGlobal 换指针（而不是往旧实例里搬数据）：所有读写都已收口到带锁的访问器，
		// 不必再靠「保留同一个 Manager 指针」来维持调用方的一致性
		writeMu.Lock()
		newMgr, loadErr := store.LoadFromFile(store.StorePath)
		if loadErr == nil {
			store.SetGlobal(newMgr)
		}
		writeMu.Unlock()

		if loadErr != nil {
			logger.Warn(l10n.T("Failed to reload the store file", nil), "error", loadErr)
			continue
		}
		hub.notify(storeRev())
	}
}

// tryOpenBrowser 尝试在默认浏览器中打开指定 URL，失败时静默忽略
func tryOpenBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	case "windows":
		err = exec.Command("cmd", "/c", "start", url).Start()
	}
	if err != nil {
		logger.Debug(l10n.T("Failed to open the browser automatically", nil), "error", err)
	}
}

// formatFileSize 将字节数格式化为人类可读的大小
func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// recordLocator 是「设备 + 类型 + 字段值」这套单条记录定位三元组，/api/repair 与 /api/unlink 共用
//
// 为什么抽成独立类型而不是两个端点各声明一份：定位口径是这两个破坏性端点里唯一「选错就改坏数据」的地方，
// 两份声明意味着 device/type/字段映射一旦调整就可能只改一处，出现「修复认得出、解除认不出」，
// 或者更糟的「解除按旧口径选中了另一条记录」；共用一份实现后，定位规则只有一个来源
//
// 为什么用「字段值整体匹配」而不是下标定位待处理记录：/api/check 的结果顺序与页面上的行对不上稳定下标
// （页面可能有未保存改动、行可拖拽换序），而字段值取自 store 原值、与顺序无关，
// 且 performCheck 返回的字段就是存储中的原值，两侧能逐字比对
type recordLocator struct {
	Device string            `json:"device"`
	Type   string            `json:"type"`
	Fields map[string]string `json:"fields"`
}

// locateOne 判断请求是否给全了「设备 + 类型 + 字段」这套单条定位三元组
// 字段表为空时无法比对任何记录，一律视为定位信息不完整
func (l recordLocator) locateOne() bool {
	return l.Device != "" && l.Type != "" && len(l.Fields) > 0
}

// repairRequest 是 POST /api/repair 的请求体，两种形态互斥
//
//   - {"all": true}：修复当前平台的全部无效条目（网页操作栏的批量修复按钮）
//   - {"device": ..., "type": ..., "fields": {...}}：只修复指定的那一条（网页行内的单条修复按钮）
//
// 定位三元组内嵌 recordLocator（与 /api/unlink 共用同一份字段与判定），All 是修复独有的全量开关
type repairRequest struct {
	recordLocator
	All bool `json:"all"`
}

// unlinkRequest 是 POST /api/unlink 的请求体，只支持「单条解除」一种形态
//
// 刻意不提供 all=true 的全量解除：解除要先把权威源的真实内容复制回派生位置（目录可能很大），
// 一次点击就对着整个平台的全部有效记录开跑，用户很难预期耗时与磁盘占用；
// CLI 上 --all 是用户在终端里明确敲下的、且能逐条看到输出，两者的确认强度不对等
// 因此这里必须给全定位三元组，缺定位信息就报错，而不是默不作声地什么都不做
//
// NoTrash 是「本次请求是否真实删除」的开关，默认 false（移入回收站，可恢复）；
// 它是请求级取值而不是读包级 noTrash，正是为了让网页能逐次选择，不受服务端启动参数影响
type unlinkRequest struct {
	recordLocator
	NoTrash bool `json:"noTrash"`
}

// repairOutcome 是 /api/repair 响应 results 的元素，描述一条记录的修复结果
// Paths 是「权威副本 → 派生位置」的可读路径串，页面直接展示，无需再回 store 反查
type repairOutcome struct {
	Device  string `json:"device"`
	Type    string `json:"type"`
	Paths   string `json:"paths"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// languageRequest 是 POST /api/language 的请求体，形如 {"language": "zh-CN"}
//
// 刻意只接受一个语言标签、不支持一次推送多个设置项：每个设置项的校验口径与副作用都不同
// （语言要改进程状态并写文件，allowHosts 只影响白名单），混在一个端点里迟早出现
// "改 A 成功、改 B 失败"的半成品，而调用方没有任何办法表达"要么全成功要么全失败"
type languageRequest struct {
	Language string `json:"language"`
}

// writeMethodNotAllowed 统一回写「请求方法不被允许」的 405 响应
//
// 收成一处的理由：六个端点此前各自硬编码了同一串英文，中文界面下这几条会漏译
// （本项目的界面语言覆盖所有面向用户的文案，包括服务端错误），
// 而且日后要改这串文案时，很容易只改到其中几处、剩下的继续以旧文案示人
func writeMethodNotAllowed(w http.ResponseWriter) {
	http.Error(w, l10n.T("Method not allowed", nil), http.StatusMethodNotAllowed)
}

// writeActionResponse 输出破坏性操作端点（/api/repair、/api/unlink）的 JSON 响应
//
// 与 /api/check 同风格：结果一律以 200 返回，业务层面的成败由 success 承载，
// 这样前端只需解析一处响应体即可拿到 output 与逐条结果；只有请求本身不合法（方法错、JSON 错）才用状态码拒绝
// outcomes 为空时显式归一成空切片：nil 会被序列化成 null，前端遍历时需要额外的判空分支
//   - /api/repair 会填充 outcomes，/api/unlink 只解除单条、不产出逐条结果，传 nil 得到空数组
//
// platform 字段告知结果属于哪个平台，与 /api/check 的响应保持对称
// 解除了单条记录的端点复用同一份响应结构：前端读的是同一组字段（success / output），
// 多出来的 platform 与空 results 不会干扰解除结果的展示，也避免两份几乎相同的响应拼装各自演化
func writeActionResponse(w http.ResponseWriter, success bool, outcomes []repairOutcome, output string) {
	if outcomes == nil {
		outcomes = []repairOutcome{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success":  success,
		"platform": runtime.GOOS,
		"results":  outcomes,
		"output":   output,
	})
}

// filterRecordTargets 在候选集合里按「设备 + 类型 + 全部字段值」精确匹配出唯一目标
//
// 该函数同时服务修复（候选=无效项）与解除（候选=有效项）两个端点：
// 两边只是候选集合的过滤方向不同（filterCheckResults 的 keepValid 取值相反），
// 定位口径与「选错记录」的风险完全相同，因此定位实现只保留一份
//
// 匹配方式说明：
//   - 设备与类型必须逐字相等，避免把同名路径的另一类记录误处理
//   - 字段以 store 中的原值为准（buildRecordEntry 提供与 performCheck 一致的字段映射），
//     只要求请求里给出的字段与记录一致，不要求字段数量相等——多传的字段不影响定位
//   - 重复记录（同一设备/类型/字段值出现多次）只取第一条：它们指向完全相同的路径，
//     逐条重复执行只会把刚建好/刚解除的链接再处理一遍
//
// 返回值与输入同为「空结果为非 nil 空切片」的形态，调用方只做 len 判断
func filterRecordTargets(targets []output.CheckResult, loc recordLocator) []output.CheckResult {
	matched := make([]output.CheckResult, 0, 1)
	for _, result := range targets {
		if result.Device != loc.Device || result.Type != loc.Type {
			continue
		}
		entry := buildRecordEntry(result)
		if len(entry) == 0 {
			continue
		}
		match := true
		for key, value := range entry {
			if loc.Fields[key] != value {
				match = false
				break
			}
		}
		if match {
			// 只取第一条即返回：同一份定位信息对应的记录彼此等价，重复处理没有意义
			return append(matched, result)
		}
	}
	return matched
}

// recordDisplayPaths 把一条检查结果渲染成「权威副本 → 派生位置」的可读路径串，供 /api/repair 的输出与逐条结果展示
//
// 字段名与顺序都取自 recordFields（字段名的唯一真源，见 cmd/check.go），本函数只做拼接：
// 此前它自带一份同样内容的映射表，新增链接类型时要改两处，因此那一份已被删除
// 未知类型返回空串：修复本身会以「Unknown type」失败，展示层不必再造一个错误分支
func recordDisplayPaths(result output.CheckResult) string {
	entry := buildRecordEntry(result)
	if len(entry) == 0 {
		return ""
	}
	// 走到这里说明类型已在 recordFields 中，取表里的字段名即可与 buildRecordEntry 的键一一对应
	fields := recordFields[result.Type]
	return entry[fields[0]] + " → " + entry[fields[1]]
}
