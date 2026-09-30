package cmd

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
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
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		logger.Warn(l10n.T("Failed to read the settings file; its allowHosts list will be ignored", nil), "error", cfgErr)
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

	// serveWriteMu 串行化本进程内对 store 的两条写路径：
	//   - POST /api/config：把前端传来的 JSON 替换进内存（Replace）后落盘（Save）
	//   - watchStoreFile 轮询：读磁盘文件（LoadFromFile）后重新发布全局实例（SetGlobal）
	// 为什么光靠 store 自己的锁不够：那把锁只保护单次调用，无法覆盖「Replace 与 Save 之间」以及
	// 「重新加载与发布之间」的空档。轮询若在 POST 落盘完成前读到旧文件，就会把用户刚保存的内容在内存里回滚，
	// 内存与磁盘从此长期分叉（轮询只在文件变化时才重载，不会自己发现这次分叉）
	// 锁的归属：声明在命令执行栈上，用参数与闭包捕获交给两条路径，生命周期与本次服务进程严格一致；
	// 刻意不用包级变量，避免同进程内再次调用 runServe（测试里就会）时两代服务共用一把锁
	var serveWriteMu sync.Mutex

	// repairMu 串行化本进程内的破坏性修复操作（POST /api/repair）
	//
	// 为什么必须串行：repairResult 的动作是「先删掉派生位置，再按权威副本重建」，
	// 两个并发请求若落在同一条路径上，就会出现 A 刚建好的链接被 B 当成残留删掉、
	// 或者双方各自的删除计划交错执行，最终留下谁也说不清的状态；
	// 修复的读写对象是文件系统而不是 store，因此它不参与 serveWriteMu 那把「清单读写」的锁，
	// 两把锁保护的对象不同，谁先谁后都不会形成环路
	//
	// 锁的归属与 serveWriteMu 一致：声明在命令执行栈上由闭包捕获，生命周期与本次服务实例严格一致，
	// 刻意不用包级变量，避免同进程内再次调用 runServe（测试里就会）时两代服务共用一把锁
	var repairMu sync.Mutex

	// languageMu 串行化整段「切换语言」操作（POST /api/language）
	//
	// 为什么必须串行：一次切换由三步组成（换翻译器 → 重译命令树 → 写设置文件），
	// 两个标签页同时切换时会交错成"先切 zh 的写盘被 en 的写盘覆盖、而内存里是 zh"这类
	// 内存与磁盘长期分叉的状态；更麻烦的是失败回滚——A 的回滚会把 B 刚切好的语言改回去，
	// 而 B 已经收到成功响应，页面上却是另一种语言
	//
	// 锁的归属与 serveWriteMu / repairMu 一致：声明在命令执行栈上由闭包捕获，
	// 生命周期与本次服务实例严格一致，刻意不用包级变量
	//
	// 它与 serveWriteMu 保护的对象不同（设置文件 vs 清单文件），因此不存在锁序问题
	var languageMu sync.Mutex

	// 启动文件变更监听（轮询方式，每秒检查一次文件的修改时间）
	go watchStoreFile(hub, &serveWriteMu)

	mux := http.NewServeMux()

	// 首页：嵌入式 HTML 页面
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// 禁止缓存：页面里注入了当前语言（见 servedConfigHTML），而"切换语言后整页重载"
		// 是前端唯一的对齐手段——若浏览器拿缓存顶上，重载后仍是切换前的语言，
		// 用户会以为切换失败。页面只有几十 KB，禁用缓存没有性能代价
		w.Header().Set("Cache-Control", "no-store")
		w.Write(servedConfigHTML())
	})

	// API：读写 store JSON
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			// Global() 内部加锁读取全局实例，避免与轮询协程的 SetGlobal 竞争同一个裸指针
			mgr := store.Global()
			if mgr == nil {
				w.Write([]byte("{}"))
				return
			}
			w.Write([]byte(mgr.ToJSON()))
		case http.MethodPost:
			var newData store.RootConfig
			if err := json.NewDecoder(r.Body).Decode(&newData); err != nil {
				http.Error(w, l10n.T("JSON parsing failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusBadRequest)
				return
			}

			// 「改内存 → 写磁盘」整体串行化，理由见 serveWriteMu 的声明处
			serveWriteMu.Lock()
			// 防御性判空：InitStore 失败时全局实例可能为 nil，EnsureGlobal 会按需创建空实例承接写入，保证服务不崩溃
			// 这里不能写成「Global() 判空后再赋值」：那两步之间没有锁保护，并发下仍可能覆盖掉别人新建的实例
			mgr := store.EnsureGlobal()
			// 保存失败时必须把内存回滚成写盘前的快照：否则内存是新内容、磁盘是旧内容，
			// 而轮询只在文件变化时才重载，这份分叉会一直留着，直到下次外部改动才被掩盖
			previous := mgr.Snapshot()
			mgr.Replace(newData)
			saveErr := mgr.Save(store.StorePath)
			if saveErr != nil {
				mgr.Replace(previous)
			}
			// rev 在锁内读取：确保它对应的就是刚写下的这份文件（锁外的写路径只有轮询，而轮询只读不写）
			rev := storeRev()
			serveWriteMu.Unlock()

			if saveErr != nil {
				http.Error(w, l10n.T("Save failed: {{.Err}}", map[string]any{"Err": saveErr.Error()}), http.StatusInternalServerError)
				return
			}
			// 保存成功后广播变更并回传版本号：前端保存成功时记下这个 rev，
			// 随后的 SSE 事件带上同一个 rev 就会被判定为「本页自己保存」，不做多余重载
			hub.notify(rev)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"success": true, "rev": rev})
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API：返回文件元信息
	mux.HandleFunc("/api/meta", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		normalizedPath, _ := pathutil.NormalizePath(store.StorePath)
		// rev 是文件版本标识：前端加载页面时记下它，后续用于识别外部修改
		// version / platform 供页脚展示当前运行的构建，取自编译期注入的包级变量（见 cmd/version.go），
		// 不需要额外端点，也不读取 store
		info := map[string]string{
			"storePath": normalizedPath,
			"rev":       storeRev(),
			"version":   Version,
			"platform":  platformLabel(),
		}
		if fi, err := os.Stat(normalizedPath); err == nil {
			info["modTime"] = fi.ModTime().Format("2006-01-02 15:04:05")
			info["fileSize"] = formatFileSize(fi.Size())
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	})

	// API：可用性检测，全量检查当前平台所有记录并逐条返回有效/无效结果
	// 只读操作故用 GET；performCheck 与本文件同属 cmd 包，直接复用，无需过滤参数（数据量小，前端自行匹配）
	// 响应 platform 字段告知前端结果属于哪个平台，前端仅在浏览该平台页签时展示状态徽标
	mux.HandleFunc("/api/check", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		results, err := performCheck(CheckOptions{})
		if err != nil {
			http.Error(w, l10n.T("Check failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusInternalServerError)
			return
		}
		// 空结果时保证序列化为 [] 而非 null，前端无需判空
		if results == nil {
			results = []output.CheckResult{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"platform": runtime.GOOS,
			"results":  results,
		})
	})

	// API：一键修复无效条目，把清单里的记录真正变成磁盘上的链接
	//
	// 前因：网页此前只能新增/编辑清单条目与做只读检测，用户在页面上加了一条记录后，
	// 必须回到终端执行 flk fix 才能建立链接，这是 WebUI 体验上最大的断点。
	// 本端点复用 fix 的 repairResult——它与本文件同属 cmd 包、零 cobra 与 flag 依赖，
	// 唯一的包级依赖是 noTrash，而该值在服务启动后是常量，不存在并发改写问题
	//
	// 与 /api/config 的分工（刻意保持的一条边界）：repairResult 只操作文件系统、不写清单，
	// 「清单写入」始终只有 POST /api/config 一条路径；本端点收尾只广播事件、不落盘，
	// 因此 store 依然只有一个写入者，不会出现内存与磁盘分叉
	mux.HandleFunc("/api/repair", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req repairRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, l10n.T("JSON parsing failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusBadRequest)
			return
		}
		// 请求形态校验前置：既没要求全量、又没给全「设备 + 类型 + 字段」这套定位三元组时，
		// 无法确定要修哪一条，直接报错比默不作声地什么都不做更容易排查
		if !req.All && !req.locateOne() {
			writeActionResponse(w, false, nil, l10n.T("Invalid repair request: either all=true or a device/type/fields tuple is required", nil))
			return
		}

		// 整段处理都在 repairMu 内：从「检查」到「重建」之间若被另一个修复请求插队，
		// 两次操作会基于同一份过期结果对同一条路径下手（详见 repairMu 的声明注释）
		repairMu.Lock()
		defer repairMu.Unlock()

		results, err := performCheck(CheckOptions{})
		if err != nil {
			http.Error(w, l10n.T("Check failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusInternalServerError)
			return
		}
		// 只保留无效项：修复的对象就是「检查结果为无效」的记录，
		// 过滤方向与 fix 的 checkAndDisplay 完全一致（都走 filterCheckResults 的 keepValid=false）
		targets := filterCheckResults(results, false)
		if !req.All {
			targets = filterRecordTargets(targets, req.recordLocator)
			if len(targets) == 0 {
				// 页面停留期间清单被改（外部修改、另一标签页保存、条目已被删除）时会走到这里，
				// 提示用户刷新而不是静默返回「成功 0 条」
				writeActionResponse(w, false, nil, l10n.T("The entry to repair was not found; reload the page and try again", nil))
				return
			}
		}
		if len(targets) == 0 {
			// 全量模式下没有无效项：这是正常结果而不是失败，文案与 flk fix 保持一致
			writeActionResponse(w, true, nil, l10n.T("All links are valid; nothing to repair", nil))
			return
		}

		// buf 必须是显式创建的非 nil writer：repairResult 把它一路透传给 safeop 与 create 包，
		// 而那条链路上 Output 为 nil 时会回退到服务端标准输出/标准错误
		// （见 internal/safeop/remove.go 与 internal/create/shared/backup.go），
		// 于是删除计划与进度会打到服务端终端、网页上却什么都看不到
		var buf bytes.Buffer
		outcomes := make([]repairOutcome, 0, len(targets))
		failed := 0
		for idx, item := range targets {
			paths := recordDisplayPaths(item)
			fmt.Fprintln(&buf, l10n.T("Repairing #{{.Index}}: {{.Device}}/{{.Type}} {{.Paths}}", map[string]any{
				"Index":  idx + 1,
				"Device": item.Device,
				"Type":   item.Type,
				"Paths":  paths,
			}))

			// skipConfirm 固定为 true：网页上已经用确认对话框逐条列出过要修什么，
			// 这里再弹一次确认会落到服务端 stdin（无人值守时直接报错），修复必然失败
			if err := repairResult(item, idx, true, &buf); err != nil {
				failed++
				fmt.Fprintln(&buf, l10n.T("Repair failed #{{.Index}}: {{.Err}}", map[string]any{"Index": idx + 1, "Err": err.Error()}))
				outcomes = append(outcomes, repairOutcome{Device: item.Device, Type: item.Type, Paths: paths, Success: false, Error: err.Error()})
				continue
			}
			fmt.Fprintln(&buf, l10n.T("Repaired #{{.Index}}", map[string]any{"Index": idx + 1}))
			outcomes = append(outcomes, repairOutcome{Device: item.Device, Type: item.Type, Paths: paths, Success: true})
		}
		fmt.Fprintln(&buf, l10n.T("Repair finished: {{.Ok}} succeeded, {{.Failed}} failed", map[string]any{
			"Ok":     len(targets) - failed,
			"Failed": failed,
		}))

		// 广播「链接状态已变化」：修复不改清单，rev 不会变，只发 updated 会被所有页面当成
		// 「本页自己保存」而忽略，页面徽标就停在旧状态（详见 sseHub.notifyRepaired）
		// 即便全部失败也要广播：失败前可能已经删掉了派生位置，文件系统状态确实变了
		hub.notifyRepaired()

		writeActionResponse(w, failed == 0, outcomes, buf.String())
	})

	// API：解除单条记录的链接关系（flk unlink 的网页版），把派生位置上的链接换成一份独立真实内容，
	// 并从清单里删掉这条记录
	//
	// 为什么只支持单条：解除发生在「用户已经确认过这一条」的前提下，见 unlinkRequest 的注释；
	// 网页端不做全量解除，因此这里不接受 all 形态的请求体
	//
	// 与 /api/repair 的两点关键差别：
	//  1. 解除会改清单（移除记录并落盘），而修复只动文件系统。因此本端点是 /api/config 之外
	//     第二个「清单写入者」，必须严格遵守下面的锁顺序，否则轮询重载会把这次内存改动覆盖掉
	//  2. 破坏文件系统的部分是先删除派生位置再复制权威源的真实内容，比修复的风险更高，
	//     所以除 repairMu 之外还要求前端必须先弹确认对话框（noTrash 也由用户在对话框里决定）
	mux.HandleFunc("/api/unlink", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req unlinkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, l10n.T("JSON parsing failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusBadRequest)
			return
		}
		// 定位信息不全就没有唯一目标，直接报错而不是退回「全量解除」那种危险默认值
		if !req.locateOne() {
			writeActionResponse(w, false, nil, l10n.T("Invalid unlink request: a device/type/fields tuple is required", nil))
			return
		}

		// 锁顺序固定为 repairMu → serveWriteMu，两把锁各自只保护一类状态，不会形成环路：
		//   repairMu：串行化所有破坏性文件系统操作。修复与解除共用同一把锁，因为它们动的是同一批路径，
		//     两个并发解除落在同一条路径上，会出现 A 刚复制好的真实内容被 B 当成残留删掉
		//     （解除的动作是「先删派生位置、再按权威源重建」，与修复同族）
		//   serveWriteMu：只在最后「移除记录 + 落盘」这一小段持有。刻意不在文件系统操作期间持有它——
		//     解除可能复制整棵目录（耗时数秒），而它保护的是清单读写与轮询重载，
		//     长时间持有会让保存请求排队、轮询停摆
		repairMu.Lock()
		defer repairMu.Unlock()

		results, err := performCheck(CheckOptions{})
		if err != nil {
			http.Error(w, l10n.T("Check failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusInternalServerError)
			return
		}
		// 只保留有效项：解除的对象就是「检查结果为有效」的记录，与 CLI 的 unlink 口径完全一致
		// （都走 filterCheckResults 的 keepValid=true）；无效记录在文件系统层面已无链接可解，
		// 应交由修复处理，因此定位范围里刻意不含它们
		targets := filterCheckResults(results, true)
		targets = filterRecordTargets(targets, req.recordLocator)
		if len(targets) == 0 {
			// 页面停留期间清单被改、或那条记录本就无效（例如链接已被外部删除）时会走到这里，
			// 提示刷新而不是静默返回失败
			writeActionResponse(w, false, nil, l10n.T("The entry to unlink was not found; reload the page and try again", nil))
			return
		}
		target := targets[0]

		// buf 必须是显式创建的非 nil writer：解除链路会把输出透传给 safeop，
		// 而那条链路上 Output 为 nil 时会回退到服务端标准输出/标准错误
		// （见 internal/safeop/remove.go），于是删除计划与进度会打到服务端终端、网页上却什么都看不到
		var buf bytes.Buffer
		fmt.Fprintln(&buf, l10n.T("Removing #{{.Index}}: {{.Device}}/{{.Type}} {{.Paths}}", map[string]any{
			"Index":  1,
			"Device": target.Device,
			"Type":   target.Type,
			"Paths":  recordDisplayPaths(target),
		}))

		// skipConfirm 固定为 true：网页上已经用确认对话框列出过要解除哪一条、后果如何，
		// 这里再弹一次确认会落到服务端 stdin（无人值守时直接报错），解除必然失败
		// noTrash 取请求里的取值，让「本次是否真实删除」由用户在对话框里决定
		if err := unlinkFilesystem(target, true, req.NoTrash, &buf); err != nil {
			fmt.Fprintln(&buf, l10n.T("Removal failed #{{.Index}}: {{.Err}}", map[string]any{"Index": 1, "Err": err.Error()}))
			// 与 /api/repair 一样广播「链接状态已变化」：解除失败前可能已经删掉了派生位置，
			// 文件系统状态确实变了，页面需要重跑检测才能反映出来
			hub.notifyRepaired()
			writeActionResponse(w, false, nil, buf.String())
			return
		}
		fmt.Fprintln(&buf, l10n.T("Removed #{{.Index}}", map[string]any{"Index": 1}))

		// 「移除记录 + 落盘」整体放进 serveWriteMu：轮询重载读的是磁盘文件，
		// 若这次内存改动没落盘就放开锁，轮询一旦在文件变化时刻重载，
		// 内存里那条已被移除的记录会「复活」，页面上删掉的记录又出现
		serveWriteMu.Lock()
		mgr := store.Global()
		// 写盘失败时要把内存回滚成改前的快照，理由与 POST /api/config 相同：
		// 否则内存里记录已删除、磁盘上还在，而轮询只在文件变化时才重载，这份分叉不会自愈
		var previous store.RootConfig
		if mgr != nil {
			previous = mgr.Snapshot()
		}
		removed := removeTrackedRecord(target)
		saveErr := saveTrackedStore()
		if saveErr != nil && mgr != nil {
			mgr.Replace(previous)
		}
		// rev 在锁内读取：确保它对应的就是刚写下的这份文件
		rev := storeRev()
		serveWriteMu.Unlock()

		// 清单里找不到这条记录（store 不可用，或记录已被外部改写）：文件系统层面的解除已经发生，
		// 但清单没有任何对应变化，如实按失败上报，避免用户以为「清单里也删掉了」
		if !removed {
			fmt.Fprintln(&buf, l10n.T("The entry to unlink was not found; reload the page and try again", nil))
		}
		if saveErr != nil {
			fmt.Fprintln(&buf, l10n.T("Save failed: {{.Err}}", map[string]any{"Err": saveErr.Error()}))
		}
		if removed && saveErr == nil {
			// 清单确实变了，广播 updated（携带新 rev）：其它打开的页面据此重载并看到记录已消失；
			// 本页的前端在响应回来后会自己重载一次，不依赖这次广播的时序
			hub.notify(rev)
		}
		writeActionResponse(w, removed && saveErr == nil, nil, buf.String())
	})

	// API：切换服务端语言（WebUI 页头的语言选择器）
	//
	// 与其它端点最大的不同：它改的是**进程级状态**（l10n 的当前语言）和用户设置文件，
	// 因此三件事必须成对完成，缺一不可：
	//  1. l10n.SetLanguage：让此后所有 T() 都按新语言输出。语言文件只在 Init 时解析一次，
	//     这里只是替换翻译器（见 pkg/l10n 的 SetLanguage 注释）
	//  2. relocalizeCommands：把包初始化阶段定格的命令树文案（Short/Long/flag 说明）重译一遍。
	//     少了这一步就会出现"网页已经变中文、终端里 flk --help 还是英文"的割裂
	//  3. config.SetLanguage：写回 ~/.config/flk/flk-config.json，让重启后仍然是这门语言。
	//     少了这一步，用户切完语言重启又变回去，会以为切换功能时灵时不灵
	//
	// 顺序刻意是"先改内存 → 再落盘 → 最后重译命令树并广播"：
	// 三步里只有落盘会因外部原因失败（磁盘满、权限不足），把它夹在"换翻译器"与"重译命令树"
	// 之间，失败时就能用一次回滚整体还原——此刻命令树还没重译，文案仍是旧语言的，
	// 把翻译器改回去就前后一致；反之若先重译再落盘，回滚就得再重译一次，
	// 而"重译"这一步本身也可能失败，回滚路径会变成一条比正常路径更复杂的链
	//
	// 做成 POST 而不是 GET：它有副作用（改全局状态 + 写用户文件），
	// GET 会被浏览器预取、被链接爬虫乱触发，属于典型的"用错方法制造幽灵 bug"
	mux.HandleFunc("/api/language", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req languageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, l10n.T("JSON parsing failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusBadRequest)
			return
		}
		// 严格校验：不受支持的语言必须拒绝，而不是静默回退默认语言。
		// 静默改写会让用户以为"我选的语言生效了"，实际页面与终端都不会有任何变化
		lang := strings.TrimSpace(req.Language)
		if !l10n.IsSupported(lang, l10n.Supported()) {
			http.Error(w, l10n.T("Unsupported language: {{.Lang}}", map[string]any{"Lang": lang}), http.StatusBadRequest)
			return
		}

		languageMu.Lock()
		defer languageMu.Unlock()

		previous := l10n.Current()
		if err := l10n.SetLanguage(lang); err != nil {
			// 已用 IsSupported 拦过一道，这里只可能是"Init 从未成功"等非预期状态；
			// 复用同一条文案而不是另造一句：对用户来说"这个语言不能用"就是全部信息
			http.Error(w, l10n.T("Unsupported language: {{.Lang}}", map[string]any{"Lang": lang}), http.StatusBadRequest)
			return
		}
		// 归一化之后的标签才是真正生效的那个（如 zh-Hant → zh-CN），
		// 落盘与回传都用它，避免文件里存着一个从未生效过的写法
		resolved := l10n.Current()

		if err := config.SetLanguage(resolved); err != nil {
			// 回滚内存里的语言：命令树此刻还没重译，文案仍是旧语言的，
			// 把翻译器改回去就前后一致——不会出现"接口说我切换失败、页面文案却已变中文"
			if rollbackErr := l10n.SetLanguage(previous); rollbackErr != nil {
				// 回滚失败只可能源于 previous 不在受支持列表里（正常流程下不可达）。
				// 两个错误合并上报而不是另造一条消息：多一条只服务不可达分支的文案，
				// 后续每次翻译维护都要多背一份负担
				err = errors.Join(err, rollbackErr)
			}
			http.Error(w, l10n.T("Save failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusInternalServerError)
			return
		}

		relocalizeCommands()
		// 广播给所有打开的页面（包括发起本次切换的这个标签页）：语言是进程级状态，
		// 每个页面注入的语言与命令树文案都要跟着对齐，整页重载是最省事也最不易出错的同步方式
		hub.notifyLanguage()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true, "language": resolved})
	})

	// API：SSE 事件推送，客户端连接后持续接收文件变更通知
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher.Flush()

		ch := hub.register()
		defer hub.unregister(ch)

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-ch:
				// 事件负载带上 rev，前端据此区分本页保存与外部修改
				// map[string]string 的 json.Marshal 不会失败，下面的 "{}" 只是防御性兜底；
				// 真走到那里，前端会因 rev 为空而把它当成「本页保存」忽略，反而吞掉一次真实的外部变更
				payload, err := json.Marshal(map[string]string{"rev": hub.currentRev()})
				if err != nil {
					payload = []byte("{}")
				}
				// 无论本次唤醒属于哪类事件都先补一条 updated：它携带的是 hub 上的最新版本，
				// 若此次是 repaired/language 唤醒、而期间恰好又有一次清单变更被合并进来，
				// 只发那一条会让本次清单变更的通知彻底丢失（前端的 rev 比对只在收到事件时才进行）
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sseEventUpdated, payload)
				switch event {
				case sseEventRepaired:
					// 链接状态已变化但对清单未作改动，页面收到后只需重跑可用性检测
					fmt.Fprintf(w, "event: %s\ndata: {}\n\n", sseEventRepaired)
				case sseEventLanguage:
					// 服务端语言已切换：页面收到后整页重载，
					// 让页面文案、注入的语言、命令树文案三者一次对齐
					fmt.Fprintf(w, "event: %s\ndata: {}\n\n", sseEventLanguage)
				}
				flusher.Flush()
			}
		}
	})

	// 非回环绑定意味着同网段任何人都能打开这个 WebUI，而 WebUI 可以直接改写清单文件，
	// 因此启动时必须明确警告；绑定回环地址（默认）时保持安静，不打扰用户
	if !isLoopbackHost(host) {
		logger.Warn(l10n.T("The WebUI is bound to a non-loopback address; anyone who can reach this port can read and edit your store file", nil), "host", host)
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
// writeMu 与 POST /api/config 共用（见 runServe 中该锁的声明注释）：
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

// recordEntryFields 按「权威副本 → 派生位置」的顺序列出每种链接类型在 store 中的字段名
//
// 它存在的唯一理由是本文件需要稳定的字段顺序：buildRecordEntry 返回的是 map，遍历顺序随机，
// 拼不出可读的路径对。字段名本身仍以 buildRecordEntry 的映射为准，两者必须同步修改，
// 一旦新增链接类型，这里的缺失只会让路径串为空（不影响修复本身），不会造成文件系统层面的错误动作
var recordEntryFields = map[string][2]string{
	"symlink":  {"real", "fake"},
	"hardlink": {"prim", "seco"},
	"copy":     {"src", "dst"},
}

// recordDisplayPaths 把一条检查结果渲染成「权威副本 → 派生位置」的可读路径串，供 /api/repair 的输出与逐条结果展示
// 未知类型返回空串：修复本身会以「Unknown type」失败，展示层不必再造一个错误分支
func recordDisplayPaths(result output.CheckResult) string {
	entry := buildRecordEntry(result)
	pair, ok := recordEntryFields[result.Type]
	if !ok || len(entry) == 0 {
		return ""
	}
	return entry[pair[0]] + " → " + entry[pair[1]]
}
