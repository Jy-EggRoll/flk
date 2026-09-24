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
	"sync"
	"time"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/spf13/cobra"
)

/*
serveConfigCmd 以网页形式展示 flk-store.json 的内容
通过 SSE 推送文件变更事件，实现浏览器端实时更新
*/

//go:embed ui/config.html
var configHTML []byte

// servedConfigHTML 把当前语言注入 WebUI 页面后返回。
//
// WebUI 是静态资源，其文案不经过 Go 源码的 l10n 提取管线（该管线只扫描 .go），
// 因此页面内自带一份以英文源串为 key 的翻译表，Go 端只负责把当前语言写进
// window.__FLK_LANG__，由前端自行切换；这样后端无需感知页面里有哪些文案。
func servedConfigHTML() []byte {
	return bytes.Replace(configHTML, []byte("__FLK_LANG_VALUE__"), []byte(l10n.Current()), 1)
}

// sseHub 管理 SSE 客户端连接，用于广播文件变更事件
type sseHub struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{clients: make(map[chan struct{}]struct{})}
}

func (h *sseHub) register() chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *sseHub) unregister(ch chan struct{}) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

func (h *sseHub) notify() {
	h.mu.Lock()
	for ch := range h.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()
}

var serveConfigCmd = &cobra.Command{
	Use:     "config",
	Aliases: []string{"cfg", "c"},
	Short:   l10n.T("View the store file in a browser", nil),
	Long:    l10n.T("Start an HTTP server that visualizes the full contents of flk-store.json and reflects local file changes in real time.", nil),
	RunE:    runServeConfig,
}

func init() {
	// Web 配置服务依赖已加载的 store，但尚未定义机器可读的启动结果，因此只声明存储能力、不声明 JSON 能力
	MarkNeedsStore(serveConfigCmd)
	serveCmd.AddCommand(serveConfigCmd)
	serveConfigCmd.Flags().Bool("no-open", false, l10n.T("Do not open the browser automatically", nil))
}

func runServeConfig(cmd *cobra.Command, args []string) error {
	// 从父命令获取网络配置
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")
	noOpen, _ := cmd.Flags().GetBool("no-open")

	// 端口自动顺延：从指定端口开始尝试，被占用则依次 +1，最多尝试 100 次
	listener, usedPort, err := listenWithRetry(host, port, 100)
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Could not find an available port (tried {{.From}} to {{.To}})", map[string]any{"From": port, "To": port + 99}), err)
	}

	addr := fmt.Sprintf("%s:%d", host, usedPort)
	hub := newSSEHub()

	// 启动文件变更监听（轮询方式，每秒检查一次文件的修改时间）
	go watchStoreFile(hub)

	mux := http.NewServeMux()

	// 首页：嵌入式 HTML 页面
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(servedConfigHTML())
	})

	// API：读写 store JSON
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			if store.GlobalManager == nil {
				w.Write([]byte("{}"))
				return
			}
			w.Write([]byte(store.GlobalManager.ToJSON()))
		case http.MethodPost:
			var newData store.RootConfig
			if err := json.NewDecoder(r.Body).Decode(&newData); err != nil {
				http.Error(w, l10n.T("JSON parsing failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusBadRequest)
				return
			}
			// 防御性判空：InitStore 失败时 GlobalManager 可能为 nil，直接赋值 .Data 会 panic
			// 这里按需新建一个空 Manager 承接写入，保证服务不崩溃
			if store.GlobalManager == nil {
				store.GlobalManager = &store.Manager{Data: make(store.RootConfig)}
			}
			store.GlobalManager.Data = newData
			if err := store.GlobalManager.Save(store.StorePath); err != nil {
				http.Error(w, l10n.T("Save failed: {{.Err}}", map[string]any{"Err": err.Error()}), http.StatusInternalServerError)
				return
			}
			hub.notify()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true}`))
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
		info := map[string]string{"storePath": normalizedPath}
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
			case <-ch:
				fmt.Fprintf(w, "event: updated\ndata: {}\n\n")
				flusher.Flush()
			}
		}
	})

	logger.Info(l10n.T("Starting service", nil), "addr", addr)
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), l10n.T("Service started: http://localhost:{{.Port}}", map[string]any{"Port": usedPort})); err != nil {
		_ = listener.Close()
		return fmt.Errorf("%s: %w", l10n.T("Failed to output the service address", nil), err)
	}

	if !noOpen {
		tryOpenBrowser(fmt.Sprintf("http://localhost:%d", usedPort))
	}

	if err := http.Serve(listener, mux); err != nil {
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
func watchStoreFile(hub *sseHub) {
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

		// 文件有变化，重新加载到 GlobalManager
		newMgr, err := store.LoadFromFile(store.StorePath)
		if err != nil {
			logger.Warn(l10n.T("Failed to reload the store file", nil), "error", err)
			continue
		}
		// 防御性判空：GlobalManager 可能因 InitStore 失败而为 nil，避免解引用 panic
		if store.GlobalManager == nil {
			store.GlobalManager = newMgr
		} else {
			store.GlobalManager.Data = newMgr.Data
		}
		hub.notify()
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
