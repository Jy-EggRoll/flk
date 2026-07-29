package cmd

import (
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
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/spf13/cobra"
)

/*
serveConfigCmd 以网页形式展示 flk-store.json 的内容
通过 SSE 推送文件变更事件，实现浏览器端实时更新
*/

//go:embed ui/config.html
var configHTML []byte

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
	Short:   "以网页形式展示配置文件",
	Long:    "启动 HTTP 服务器，以可视化方式展示 flk-store.json 的完整内容，并实时响应本地文件的变更。",
	RunE:    runServeConfig,
}

func init() {
	serveCmd.AddCommand(serveConfigCmd)
	serveConfigCmd.Flags().Bool("no-open", false, "不自动打开浏览器")
}

func runServeConfig(cmd *cobra.Command, args []string) error {
	// 从父命令获取网络配置
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")
	noOpen, _ := cmd.Flags().GetBool("no-open")

	// 端口自动顺延：从指定端口开始尝试，被占用则依次 +1，最多尝试 100 次
	listener, usedPort, err := listenWithRetry(host, port, 100)
	if err != nil {
		return fmt.Errorf("无法找到可用的端口 (尝试了 %d 到 %d): %w", port, port+99, err)
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
		w.Write(configHTML)
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
				http.Error(w, "JSON 解析失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			store.GlobalManager.Data = newData
			if err := store.GlobalManager.Save(store.StorePath); err != nil {
				http.Error(w, "保存失败: "+err.Error(), http.StatusInternalServerError)
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

	logger.Info("启动服务", "addr", addr)
	fmt.Printf("服务已启动: http://localhost:%d\n", usedPort)

	if !noOpen {
		tryOpenBrowser(fmt.Sprintf("http://localhost:%d", usedPort))
	}

	if err := http.Serve(listener, mux); err != nil {
		return fmt.Errorf("服务运行失败: %w", err)
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
		logger.Debug(fmt.Sprintf("端口 %d 被占用，尝试下一个", port))
	}
	return nil, 0, fmt.Errorf("端口 %d-%d 均被占用", startPort, startPort+maxAttempts-1)
}

// watchStoreFile 轮询检查 store 文件的修改时间，有变化时刷新全局存储并通知 SSE 客户端
func watchStoreFile(hub *sseHub) {
	normalizedPath, err := pathutil.NormalizePath(store.StorePath)
	if err != nil {
		logger.Error("无法解析存储路径: " + err.Error())
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
			logger.Warn("重新加载存储文件失败: " + err.Error())
			continue
		}
		store.GlobalManager.Data = newMgr.Data
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
		logger.Debug("自动打开浏览器失败: " + err.Error())
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
