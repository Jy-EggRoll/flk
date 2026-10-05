package cmd

/*
本文件承载 WebUI 的全部 HTTP 端点实现，从 cmd/serve_web.go 的 runServe 中分出来

背景：runServe 此前把 8 个端点的闭包逐个内联在函数体里，导致它长达 550 余行、
以「启动服务」为名却混着全部业务逻辑，既难读也难单独测试；
分出来之后 runServe 只负责组网与启动，端点逻辑集中在 serveServer 的方法上

serveServer 是这些端点的接收者：端点之间唯一共享的状态就是 hub 与三把锁，
把它们收进一个结构体，既明确了「这些状态随本次服务实例存活」的边界，
也让 runServe 里不再需要一串闭包捕获
*/

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/logger"
	"github.com/jy-eggroll/flk/internal/config"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
)

// serveServer 持有 WebUI 各端点共享的状态，由 runServe 在每次启动服务时构造一个实例
//
// 为什么把 hub 与三把锁收在这里、而不是继续用 runServe 里的局部变量加闭包捕获：
// 分出端点方法后，这些共享状态需要一个显式的承载者；同时它们的生命周期与「本次服务实例」
// 严格一致这一点，也因此从「靠闭包捕获」变成了结构体字段上的明文约束
type serveServer struct {
	// hub 是 SSE 广播中心，/api/config、/api/repair、/api/unlink、/api/language 都靠它通知其它页面，
	// /api/events 靠它注册连接；与三把锁一样，随本次服务实例存活
	hub *sseHub

	// writeMu 串行化本进程内对 store 的两条写路径：
	//   - POST /api/config：把前端传来的 JSON 替换进内存（Replace）后落盘（Save）
	//   - watchStoreFile 轮询：读磁盘文件（LoadFromFile）后重新发布全局实例（SetGlobal）
	// 为什么光靠 store 自己的锁不够：那把锁只保护单次调用，无法覆盖「Replace 与 Save 之间」以及
	// 「重新加载与发布之间」的空档。轮询若在 POST 落盘完成前读到旧文件，就会把用户刚保存的内容在内存里回滚，
	// 内存与磁盘从此长期分叉（轮询只在文件变化时才重载，不会自己发现这次分叉）
	// 锁的归属：作为 serveServer 的字段、由 runServe 在每次启动时新建（new(sync.Mutex)），
	// 生命周期与本次服务实例严格一致；刻意不用包级变量，避免同进程内再次调用 runServe（测试里就会）时两代服务共用一把锁
	writeMu *sync.Mutex

	// repairMu 串行化本进程内的破坏性修复操作（POST /api/repair）
	//
	// 为什么必须串行：repairResult 的动作是「先删掉派生位置，再按权威副本重建」，
	// 两个并发请求若落在同一条路径上，就会出现 A 刚建好的链接被 B 当成残留删掉、
	// 或者双方各自的删除计划交错执行，最终留下谁也说不清的状态；
	// 修复的读写对象是文件系统而不是 store，因此它不参与 writeMu 那把「清单读写」的锁，
	// 两把锁保护的对象不同，谁先谁后都不会形成环路
	//
	// 锁的归属与 writeMu 一致：作为 serveServer 的字段随本次服务实例存活，刻意不用包级变量，
	// 避免同进程内再次调用 runServe（测试里就会）时两代服务共用一把锁
	repairMu *sync.Mutex

	// languageMu 串行化整段「切换语言」操作（POST /api/language）
	//
	// 为什么必须串行：一次切换由三步组成（换翻译器 → 重译命令树 → 写设置文件），
	// 两个标签页同时切换时会交错成"先切 zh 的写盘被 en 的写盘覆盖、而内存里是 zh"这类
	// 内存与磁盘长期分叉的状态；更麻烦的是失败回滚——A 的回滚会把 B 刚切好的语言改回去，
	// 而 B 已经收到成功响应，页面上却是另一种语言
	//
	// 锁的归属与 writeMu / repairMu 一致：作为 serveServer 的字段随本次服务实例存活，刻意不用包级变量
	//
	// 它与 writeMu 保护的对象不同（设置文件 vs 清单文件），因此不存在锁序问题
	languageMu *sync.Mutex
}

// handleConfig 处理 /api/config：GET 读取 store JSON，POST 保存前端传来的 JSON
func (s *serveServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		// Global() 内部加锁读取全局实例，避免与轮询协程的 SetGlobal 竞争同一个无保护的指针
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

		// 「改内存 → 写磁盘」整体串行化，理由见 writeMu 的声明处
		s.writeMu.Lock()
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
		s.writeMu.Unlock()

		if saveErr != nil {
			http.Error(w, l10n.T("Save failed: {{.Err}}", map[string]any{"Err": saveErr.Error()}), http.StatusInternalServerError)
			return
		}
		// 审计：POST /api/config 是清单唯一的整表改写入口，成功落盘后记下「改写前后条目数的变化」
		// 放在 saveErr 判定之后，只记真正写下去的那一次；失败的情况由响应体的错误信息承载，不再重复记一行日志
		// previous 取的是写盘前的内存快照（深拷贝，落盘失败时还会用它回滚），此处只读不改
		logger.Info(l10n.T("Store manifest replaced", nil),
			"previous", store.CountEntries(previous),
			"count", store.CountEntries(newData))
		// 保存成功后广播变更并回传版本号：前端保存成功时记下这个 rev，
		// 随后的 SSE 事件带上同一个 rev 就会被判定为「本页自己保存」，不做多余重载
		s.hub.notify(rev)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true, "rev": rev})
	default:
		writeMethodNotAllowed(w)
	}
}

// handleMeta 处理 /api/meta：返回 store 文件路径、版本标识与构建信息
func (s *serveServer) handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
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
		// noTrash 告知前端「服务启动时带了 --no-trash」：解除弹窗据此把「真实删除」
		// 复选框置为勾选且禁用（见 cmd/ui/app.js 的 openUnlinkModal），
		// 否则用户会看到一个可以取消、而取消后仍会被服务端当成永久删除的开关。
		// 用字符串而不是布尔：本响应是 map[string]string，全字段同类型更简单
		"noTrash": fmt.Sprintf("%t", noTrash),
	}
	if fi, err := os.Stat(normalizedPath); err == nil {
		info["modTime"] = fi.ModTime().Format("2006-01-02 15:04:05")
		info["fileSize"] = formatFileSize(fi.Size())
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleCheck 处理 /api/check：可用性检测，全量检查当前平台所有记录并逐条返回有效/无效结果
// 只读操作故用 GET；performCheck 与本文件同属 cmd 包，直接复用，无需过滤参数（数据量小，前端自行匹配）
// 响应 platform 字段告知前端结果属于哪个平台，前端仅在浏览该平台页签时展示状态徽标
func (s *serveServer) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
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
}

// handleRepair 处理 /api/repair：一键修复无效条目，把清单里的记录真正变成磁盘上的链接
//
// 前因：网页此前只能新增/编辑清单条目与做只读检测，用户在页面上加了一条记录后，
// 必须回到终端执行 flk fix 才能建立链接，这是 WebUI 体验上最大的断点。
// 本端点复用 fix 的 repairResult——它与本文件同属 cmd 包、零 cobra 与 flag 依赖，
// 唯一的包级依赖是 noTrash，而该值在服务启动后是常量，不存在并发改写问题
//
// 与 /api/config 的分工（刻意保持的一条边界）：repairResult 只操作文件系统、不写清单，
// 「清单写入」始终只有 POST /api/config 一条路径；本端点收尾只广播事件、不落盘，
// 因此 store 依然只有一个写入者，不会出现内存与磁盘分叉
func (s *serveServer) handleRepair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
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
	s.repairMu.Lock()
	defer s.repairMu.Unlock()

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
			// 审计：失败的修复用 Warn 记录（带 error），成功的不在这里记，统一放到下面一行 Info，
			// 目的是「一条记录一条结果」，与 CLI fix 的逐条日志口径一致（字段同样走 recordLogArgs）
			logger.Warn(l10n.T("Repair failed", nil), append(recordLogArgs(item), "error", err)...)
			fmt.Fprintln(&buf, l10n.T("Repair failed #{{.Index}}: {{.Err}}", map[string]any{"Index": idx + 1, "Err": err.Error()}))
			outcomes = append(outcomes, repairOutcome{Device: item.Device, Type: item.Type, Paths: paths, Success: false, Error: err.Error()})
			continue
		}
		// 审计：修复的对象与结果（Info），字段走 recordLogArgs，便于和 CLI fix 的日志一起聚合
		logger.Info(l10n.T("Repaired link", nil), recordLogArgs(item)...)
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
	s.hub.notifyRepaired()

	writeActionResponse(w, failed == 0, outcomes, buf.String())
}

// handleUnlink 处理 /api/unlink：解除单条记录的链接关系（flk unlink 的网页版），
// 把派生位置上的链接换成一份独立真实内容，并从清单里删掉这条记录
//
// 为什么只支持单条：解除发生在「用户已经确认过这一条」的前提下，见 unlinkRequest 的注释；
// 网页端不做全量解除，因此这里不接受 all 形态的请求体
//
// 与 /api/repair 的两点关键差别：
//  1. 解除会改清单（移除记录并落盘），而修复只动文件系统。因此本端点是 /api/config 之外
//     第二个「清单写入者」，必须严格遵守下面的锁顺序，否则轮询重载会把这次内存改动覆盖掉
//  2. 破坏文件系统的部分是先删除派生位置再复制权威源的真实内容，比修复的风险更高，
//     所以除 repairMu 之外还要求前端必须先弹确认对话框（noTrash 也由用户在对话框里决定）
func (s *serveServer) handleUnlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
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

	// 锁顺序固定为 repairMu → writeMu，两把锁各自只保护一类状态，不会形成环路：
	//   repairMu：串行化所有破坏性文件系统操作。修复与解除共用同一把锁，因为它们动的是同一批路径，
	//     两个并发解除落在同一条路径上，会出现 A 刚复制好的真实内容被 B 当成残留删掉
	//     （解除的动作是「先删派生位置、再按权威源重建」，与修复同族）
	//   writeMu：只在最后「移除记录 + 落盘」这一小段持有。刻意不在文件系统操作期间持有它——
	//     解除可能复制整棵目录（耗时数秒），而它保护的是清单读写与轮询重载，
	//     长时间持有会让保存请求排队、轮询停摆
	s.repairMu.Lock()
	defer s.repairMu.Unlock()

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
	//
	// noTrash 取「服务级 --no-trash」与「弹窗复选框」的并集，而不是让后者覆盖前者：
	// 启动时带上 --no-trash 的人意图是「这台服务一律不用回收站」，常见起因是回收站对他
	// 不可用（跨分区、空间不够）。若被弹窗默认的不勾选盖掉，等于他特意关掉的开关又被悄悄
	// 打开；而一旦回收站真的不可用，失败信息只会说「移入回收站失败」，很难让人联想到是这里被覆盖。
	// 前端会把复选框置为勾选且禁用，让这条约定在界面上也看得见
	if err := unlinkFilesystem(target, true, noTrash || req.NoTrash, &buf); err != nil {
		// 审计：文件系统层面的解除失败（可能已删掉派生位置），用 Warn 记录并带上 error 与 recordLogArgs 字段
		logger.Warn(l10n.T("Removal failed", nil), append(recordLogArgs(target), "error", err)...)
		fmt.Fprintln(&buf, l10n.T("Removal failed #{{.Index}}: {{.Err}}", map[string]any{"Index": 1, "Err": err.Error()}))
		// 与 /api/repair 一样广播「链接状态已变化」：解除失败前可能已经删掉了派生位置，
		// 文件系统状态确实变了，页面需要重跑检测才能反映出来
		s.hub.notifyRepaired()
		writeActionResponse(w, false, nil, buf.String())
		return
	}
	fmt.Fprintln(&buf, l10n.T("Removed #{{.Index}}", map[string]any{"Index": 1}))

	// 「移除记录 + 落盘」整体放进 writeMu：轮询重载读的是磁盘文件，
	// 若这次内存改动没落盘就放开锁，轮询一旦在文件变化时刻重载，
	// 内存里那条已被移除的记录会「复活」，页面上删掉的记录又出现
	s.writeMu.Lock()
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
	s.writeMu.Unlock()

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
		s.hub.notify(rev)
	}

	// 审计：把「解除了哪一条」（recordLogArgs 的 type/device/from,to）与最终结果落进日志
	// 三类结局分别对应：清单移除后又落盘失败（带 saveErr）、清单里根本没找到这条记录、
	// 以及真正的成功；文件系统层面的失败已在上面提前 return，不在此重复
	switch {
	case saveErr != nil:
		logger.Warn(l10n.T("Removal failed", nil), append(recordLogArgs(target), "error", saveErr)...)
	case !removed:
		logger.Warn(l10n.T("Removal failed", nil), recordLogArgs(target)...)
	default:
		logger.Info(l10n.T("Removed the link relationship", nil), recordLogArgs(target)...)
	}
	writeActionResponse(w, removed && saveErr == nil, nil, buf.String())
}

// handleLanguage 处理 /api/language：切换服务端语言（WebUI 页头的语言选择器）
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
func (s *serveServer) handleLanguage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
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

	s.languageMu.Lock()
	defer s.languageMu.Unlock()

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
	s.hub.notifyLanguage()

	// 审计：语言是进程级状态，切换成功后必须留下记录；目标语言用归一化后的 resolved 而不是请求里的原始写法，
	// 因为 zh-Hant 这类变体会被收敛成 zh-CN，记下真正生效的那个才与落盘、回传一致
	// 目标语言为什么并入文案：约定的键名白名单没有语言类键，故走文案模板渲染
	logger.Info(l10n.T("Language switched", nil), "language", resolved)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "language": resolved})
}

// handleEvents 处理 /api/events：SSE 事件推送，客户端连接后持续接收文件变更通知
func (s *serveServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	// 与其它端点一致地只接受 GET：EventSource 固定用 GET 建连，
	// 不校验方法会让 POST / OPTIONS 也进入下面的长连接循环，白白占住一个连接
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, l10n.T("Streaming is not supported by this connection", nil), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ch := s.hub.register()
	defer s.hub.unregister(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-ch:
			// 事件负载带上 rev，前端据此区分本页保存与外部修改
			// map[string]string 的 json.Marshal 不会失败，下面的 "{}" 只是防御性回退；
			// 真走到那里，前端会因 rev 为空而把它当成「本页保存」忽略，反而吞掉一次真实的外部变更
			payload, err := json.Marshal(map[string]string{"rev": s.hub.currentRev()})
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
}
