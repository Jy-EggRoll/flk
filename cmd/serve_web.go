package cmd

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/logger"
	"github.com/jy-eggroll/eggokit/webui"
	"github.com/jy-eggroll/flk/internal/config"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

/*
本文件实现 serveCmd 的服务端：以网页形式展示并编辑 flk-store.json 的内容
通过 SSE 推送文件变更事件，实现浏览器端实时更新

历史沿革：本文件原名 serve_config.go，服务曾挂在 serve config 子命令下；
该子命令已整体并入 serve（详见 cmd/serve.go 中 serveCmd 的注释），因此文件与命令一起更名，
但页面自身的文件仍叫 cmd/ui/config.html——它展示的正是「配置清单」，这个文件名依然准确，无需跟着改

页面资产由三个文件组成（原先全部挤在一个 2300+ 行的 HTML 里，按语言拆开）：
  - ui/config.html：标记 + 内嵌翻译表（MSG 必须留在 .html——l10n 扫描器只认 HTML 里的
    var MSG 表，这是它不能搬去 app.js 的硬约束）+ 首帧前的主题/语言引导脚本
  - ui/style.css：全部样式（设计令牌 + 组件规则，浅/暗两套主题）
  - ui/app.js：全部页面逻辑（IIFE，消费 HTML 里定义的全局 MSG 与 window.__FLK_LANG__）

三者整体嵌成一份目录、再交给 webui 包托管：原先按文件各嵌一份、由 flk 自己写路径路由，
而路由、ETag 与目录托管的实现已在 webui 里（重复一份就会立刻漂移）。
用 all:ui 而不是逐个列文件：日后新增资源（图标、字体）不必再回来改这里，也不会漏嵌
*/

//go:embed all:ui
var uiFS embed.FS

// configHTML 惰性装载首页模板：页面字节随二进制固定，读一次即可，不必每次请求都去 FS 里查找
//
// 用 sync.OnceValue 而不是包级变量 + init：读取结果天然只算一次，也没有「谁先赋值」的顺序问题
// 读失败只可能是 embed 指令与实际文件不符，那是构建期错误（编译都过不了），
// 因此不走 panic，退化成空页面也比让整个进程崩掉好
var configHTML = sync.OnceValue(func() []byte {
	content, err := fs.ReadFile(uiFS, "ui/config.html")
	if err != nil {
		return nil
	}
	return content
})

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
// 又因为模板本身是只读的共享切片（configHTML() 每次返回同一份）、bytes.Replace 会返回新切片，
// 这个渲染过程不修改任何共享数据，天然可并发
func servedConfigHTML() []byte {
	lang := l10n.Current()
	out := bytes.Replace(configHTML(), []byte("__FLK_HTML_LANG__"), []byte(lang), 1)
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
// 从而避免自己写盘后触发一次多余的整表重载。版本号放在 hub 上而不是写进 channel，
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
// 访问白名单只组装「调用方额外授权」的部分：--allow-host 逐条列出的地址（临时授权）
// 与设置文件 allowHosts 字段（长期生效，见 internal/config）的并集。
// 服务实际绑定的地址与回环地址由 webui 自动纳入（见它 New 里的说明），这里不再重复列一遍——
// 重复一份就多一处可能与实际判定漂移的清单
func runServe(cmd *cobra.Command, args []string) error {
	// 从自身 flag 获取网络配置（serve 已无子命令，flag 全部声明在 serveCmd 上）
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")
	noOpen, _ := cmd.Flags().GetBool("no-open")
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
	allowedHosts := make([]string, 0, len(allowHosts)+len(fileAllowHosts))
	allowedHosts = append(allowedHosts, allowHosts...)
	allowedHosts = append(allowedHosts, fileAllowHosts...)

	// 把嵌入目录的 ui 子目录作为静态资源根交给 webui：/style.css、/app.js 都从这份 FS 里取
	assets, subErr := fs.Sub(uiFS, "ui")
	if subErr != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to load the WebUI assets", nil), subErr)
	}

	hub := newSSEHub()

	// 端点之间共享的依赖收进 serveServer：hub 用于广播变更事件，三把锁用于串行化各自的临界区
	//
	// 三把锁的用途、归属，以及「为什么光靠 store 自己的锁不够」「为什么必须随服务实例存活、
	// 刻意不用包级变量」的完整理由，都随端点实现迁到了 serve_api.go 中 serveServer 的字段说明
	//
	// 锁在此处逐次新建：生命周期与本次服务实例严格一致，避免同进程内再次调用 runServe（测试里就会）时
	// 两代服务共用一把锁；watchStoreFile 也传入同一把 writeMu（见其函数注释）
	srvState := &serveServer{
		hub:        hub,
		writeMu:    new(sync.Mutex),
		repairMu:   new(sync.Mutex),
		languageMu: new(sync.Mutex),
	}

	// 启动文件变更监听（轮询方式，每秒检查一次文件的修改时间）
	go watchStoreFile(hub, srvState.writeMu)

	// 业务端点整体作为 API 交给 webui：页面入口、静态资源托管与 Host / token / Origin 三道校验都由它负责，
	// 这里只保留 /api/* 这层业务路由。把端点挂在 "/api/" 前缀下（而不是原来直接挂在默认 mux 上、再自己写
	// 路径路由），意味着 flk 侧不再需要 "/" 与静态资源的路由与默认 404，那部分已随 webui 上收
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", srvState.handleConfig)
	mux.HandleFunc("/api/meta", srvState.handleMeta)
	mux.HandleFunc("/api/check", srvState.handleCheck)
	mux.HandleFunc("/api/repair", srvState.handleRepair)
	mux.HandleFunc("/api/unlink", srvState.handleUnlink)
	mux.HandleFunc("/api/language", srvState.handleLanguage)
	mux.HandleFunc("/api/events", srvState.handleEvents)

	srv, err := webui.New(webui.Config{
		Host: host,
		// 端口自动顺延：从指定端口开始尝试，被占用则依次 +1，最多尝试 100 次
		Port:   webui.Sequential(port, 100),
		Assets: assets,
		// 首页必须由 Index 现算，不能让 webui 直接托管 config.html：语言可在运行期切换
		//（见 /api/language），若把渲染烧死在启动时，切换后刷新拿到的还是旧语言
		IndexName: "config.html",
		Index:     servedConfigHTML,
		API:       mux,
		// 只传额外授权；绑定地址由 webui 自动追加（--host 192.168.1.5 时用户从地址栏访问用的就是它）
		AllowHosts:  allowedHosts,
		OpenBrowser: !noOpen,
		// 页面与 API 都必须带 token：WebUI 能直接改写清单文件，只靠回环绑定挡不住本机其它进程
		Auth: webui.Auth{Enabled: true},
	})
	if err != nil {
		return err
	}

	// 非回环绑定意味着同网段任何人都能打开这个 WebUI，而 WebUI 可以直接改写清单文件，
	// 因此启动时必须明确警告；「什么算回环」与文案都由 webui 给出，避免两处判断各说各话
	if warning := srv.NonLoopbackWarning(); warning != "" {
		pterm.Warning.WithWriter(cmd.ErrOrStderr()).Println(warning)
	}

	// 为什么走 cmd.OutOrStdout() 而不是 logger.Info：logger 的默认级别是 Warn（见 internal/logger/config.go），
	// Info 级日志默认被过滤掉，而这条属于「必须默认可见」的启动摘要。
	// Summary 里同时带上含 token 的完整链接与本次生效的白名单：链接打全是为了让用户直接复制打开，
	// 白名单则是用户确认「设置文件里那条是否真的被读到」的唯一手段（字段名写错、主机名拼错都不会报错）
	startupSummary := srv.Summary()

	logger.Info(l10n.T("Starting service", nil), "addr", srv.Addr())
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), startupSummary); err != nil {
		_ = srv.Close()
		return fmt.Errorf("%s: %w", l10n.T("Failed to output the service address", nil), err)
	}

	// 拉起浏览器与阻塞都由 webui 负责：OpenBrowser 打开的是带 token 的地址，用户无需手动补凭据
	return srv.Serve()
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
		// 直接 SetGlobal 换指针（而不是往旧实例里搬数据）：所有读写都已统一到带锁的访问器，
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
