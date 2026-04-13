package cmd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/spf13/cobra"
)

//go:embed ui/index.html
var indexHTML []byte

var (
	serverCmd = &cobra.Command{
		Use:     "server",
		Aliases: []string{"serve", "srv"},
		Short:   "启动网页管理界面",
		Long:    "启动网页管理界面，提供可视化管理",
		Run:     RunServer,
	}

	appVersion   string
	appBuildTime string
)

var (
	eventMutex  sync.RWMutex
	eventCounts = make(map[string]int64)
)

func init() {
	rootCmd.AddCommand(serverCmd)
	serverCmd.Flags().IntP("port", "p", 8999, "指定端口号")
	serverCmd.Flags().String("host", "127.0.0.1", "指定绑定的 Host (默认 127.0.0.1)")
}

func RunServer(cmd *cobra.Command, args []string) {
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")

	var listener net.Listener
	var err error
	maxTries := 100

	for i := 0; i < maxTries; i++ {
		currentPort := port + i
		addr := fmt.Sprintf("%s:%d", host, currentPort)
		listener, err = net.Listen("tcp", addr)
		if err == nil {
			port = currentPort
			break
		}
		logger.Debug(fmt.Sprintf("端口 %d 被占用，尝试下一个...", currentPort))
	}

	if err != nil {
		logger.Error(fmt.Sprintf("无法找到可用的端口 (尝试了 %d 到 %d): %v", port, port+maxTries-1, err))
		return
	}

	addr := fmt.Sprintf("%s:%d", host, port)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	http.HandleFunc("/api/config", handleConfig)
	http.HandleFunc("/api/store", handleStore)
	http.HandleFunc("/api/devices", handleDevices)
	http.HandleFunc("/api/version", handleVersion)
	http.HandleFunc("/api/cmd/check", handleCheck)
	http.HandleFunc("/api/cmd/fix", handleFix)
	http.HandleFunc("/api/cmd/delete", handleDelete)
	http.HandleFunc("/api/cmd/delete/batch", handleDeleteBatch)
	http.HandleFunc("/api/cmd/create", handleCreate)
	http.HandleFunc("/api/cmd/create/batch", handleCreateBatch)
	http.HandleFunc("/api/events", handleEvents)

	logger.Info("Starting server at http://" + addr)
	fmt.Printf("服务已启动，请在浏览器访问: http://localhost:%d\n", port)

	if runtime.GOOS == "windows" && isDirectRun {
		go func() {
			exec.Command("cmd", "/c", "start", fmt.Sprintf("http://localhost:%d", port)).Start()
		}()
	}

	if err := http.Serve(listener, nil); err != nil {
		logger.Error("Server error: " + err.Error())
	}
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "GET" {
		json.NewEncoder(w).Encode(map[string]string{"workDir": WorkDir})
		return
	}
	if r.Method == "POST" {
		var req struct {
			WorkDir string `json:"workDir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.WorkDir != "" {
			WorkDir = req.WorkDir
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func handleStore(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "GET" {
		w.Write([]byte(store.GlobalManager.ToJSON()))
		return
	}
	if r.Method == "POST" {
		var newData store.RootConfig
		if err := json.NewDecoder(r.Body).Decode(&newData); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		store.GlobalManager.Data = newData
		if err := store.GlobalManager.Save(store.StorePath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		notifyEvent("store-updated")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func handleDevices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	data := store.GlobalManager.Data
	platform := runtime.GOOS
	platformData, exists := data[platform]
	if !exists {
		json.NewEncoder(w).Encode([]string{})
		return
	}

	var devices []string
	for device := range platformData {
		devices = append(devices, device)
	}
	json.NewEncoder(w).Encode(devices)
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"version":   appVersion,
		"buildTime": appBuildTime,
		"goVersion": runtime.Version(),
		"platform":  runtime.GOOS,
	})
}

func handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Device string `json:"device"`
		Type   string `json:"type"`
		Dir    string `json:"dir"`
	}
	if r.Body != nil && r.ContentLength > 0 {
		json.NewDecoder(r.Body).Decode(&req)
	}

	opts := CheckOptions{
		CheckDir: req.Dir,
	}
	if req.Device != "" {
		for _, d := range strings.Split(req.Device, ",") {
			opts.DeviceFilters = append(opts.DeviceFilters, strings.TrimSpace(d))
		}
	}
	if req.Type == "symlink" {
		opts.CheckSymlink = true
	} else if req.Type == "hardlink" {
		opts.CheckHardlink = true
	} else {
		opts.CheckSymlink = true
		opts.CheckHardlink = true
	}

	results, err := performCheck(opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func handleFix(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		All  bool                `json:"all"`
		Item *output.CheckResult `json:"item"`
	}
	if r.Body != nil && r.ContentLength > 0 {
		json.NewDecoder(r.Body).Decode(&req)
	}

	if req.All {
		oldFixAll := fixAll
		fixAll = true
		RunFix(nil, nil)
		fixAll = oldFixAll
	} else if req.Item != nil {
		oldWorkDir := WorkDir
		WorkDir = req.Item.BasePath
		defer func() { WorkDir = oldWorkDir }()

		if req.Item.Type == "symlink" {
			oldReal, oldFake, oldForce, oldDevice := symlinkReal, symlinkFake, createForce, createDevice
			symlinkReal = req.Item.Real
			if !filepath.IsAbs(symlinkReal) {
				symlinkReal = filepath.Join(req.Item.BasePath, symlinkReal)
			}
			symlinkFake = req.Item.Fake
			createForce = true
			createDevice = req.Item.Device
			Symlink(nil, nil)
			symlinkReal, symlinkFake, createForce, createDevice = oldReal, oldFake, oldForce, oldDevice
		} else if req.Item.Type == "hardlink" {
			oldPrim, oldSeco, oldForce, oldDevice := hardlinkPrim, hardlinkSeco, createForce, createDevice
			hardlinkPrim = req.Item.Prim
			if !filepath.IsAbs(hardlinkPrim) {
				hardlinkPrim = filepath.Join(req.Item.BasePath, hardlinkPrim)
			}
			hardlinkSeco = req.Item.Seco
			if !filepath.IsAbs(hardlinkSeco) {
				hardlinkSeco = filepath.Join(req.Item.BasePath, hardlinkSeco)
			}
			createForce = true
			createDevice = req.Item.Device
			Hardlink(nil, nil)
			hardlinkPrim, hardlinkSeco, createForce, createDevice = oldPrim, oldSeco, oldForce, oldDevice
		}
	}

	notifyEvent("links-fixed")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success": true}`))
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req output.CheckResult
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	platform := runtime.GOOS
	mgr := store.GlobalManager
	var entry map[string]string
	if req.Type == "symlink" {
		entry = map[string]string{"real": req.Real, "fake": req.Fake}
	} else {
		entry = map[string]string{"prim": req.Prim, "seco": req.Seco}
	}
	mgr.RemoveMatchingEntry(platform, req.Device, req.Type, req.Path, entry)
	mgr.Save(store.StorePath)

	notifyEvent("link-deleted")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success": true}`))
}

func handleDeleteBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Items []output.CheckResult `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	platform := runtime.GOOS
	mgr := store.GlobalManager
	for _, req := range req.Items {
		var entry map[string]string
		if req.Type == "symlink" {
			entry = map[string]string{"real": req.Real, "fake": req.Fake}
		} else {
			entry = map[string]string{"prim": req.Prim, "seco": req.Seco}
		}
		mgr.RemoveMatchingEntry(platform, req.Device, req.Type, req.Path, entry)
	}
	mgr.Save(store.StorePath)

	notifyEvent("links-deleted")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"success": true, "deleted": %d}`, len(req.Items))))
}

func handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Type   string `json:"type"`
		Real   string `json:"real"`
		Fake   string `json:"fake"`
		Device string `json:"device"`
		Force  bool   `json:"force"`
		Smart  bool   `json:"smart"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	oldDevice := createDevice
	oldForce := createForce
	oldSmart := createSmart

	createDevice = req.Device
	if createDevice == "" {
		createDevice = "all"
	}
	createForce = req.Force
	createSmart = req.Smart

	var err error
	if req.Type == "symlink" {
		oldReal := symlinkReal
		oldFake := symlinkFake
		symlinkReal = req.Real
		symlinkFake = req.Fake
		err = Symlink(nil, nil)
		symlinkReal = oldReal
		symlinkFake = oldFake
	} else if req.Type == "hardlink" {
		oldPrim := hardlinkPrim
		oldSeco := hardlinkSeco
		hardlinkPrim = req.Real
		hardlinkSeco = req.Fake
		err = Hardlink(nil, nil)
		hardlinkPrim = oldPrim
		hardlinkSeco = oldSeco
	}

	createDevice = oldDevice
	createForce = oldForce
	createSmart = oldSmart

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	notifyEvent("link-created")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success": true}`))
}

func handleCreateBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Links []struct {
			Type   string `json:"type"`
			Real   string `json:"real"`
			Fake   string `json:"fake"`
			Device string `json:"device"`
			Force  bool   `json:"force"`
			Smart  bool   `json:"smart"`
		} `json:"links"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	oldDevice := createDevice
	oldForce := createForce
	oldSmart := createSmart

	var successCount int
	for _, link := range req.Links {
		createDevice = link.Device
		if createDevice == "" {
			createDevice = "all"
		}
		createForce = link.Force
		createSmart = link.Smart

		var err error
		if link.Type == "symlink" {
			oldReal := symlinkReal
			oldFake := symlinkFake
			symlinkReal = link.Real
			symlinkFake = link.Fake
			err = Symlink(nil, nil)
			symlinkReal = oldReal
			symlinkFake = oldFake
		} else if link.Type == "hardlink" {
			oldPrim := hardlinkPrim
			oldSeco := hardlinkSeco
			hardlinkPrim = link.Real
			hardlinkSeco = link.Fake
			err = Hardlink(nil, nil)
			hardlinkPrim = oldPrim
			hardlinkSeco = oldSeco
		}

		if err == nil {
			successCount++
		}
	}

	createDevice = oldDevice
	createForce = oldForce
	createSmart = oldSmart

	if successCount > 0 {
		notifyEvent("links-created")
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"success": true, "created": %d}`, successCount)))
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	lastEvent := int64(0)
	for {
		select {
		case <-r.Context().Done():
			return
		default:
			eventMutex.RLock()
			currentCount := eventCounts["last-event"]
			eventMutex.RUnlock()

			if currentCount > lastEvent {
				lastEvent = currentCount
				eventData := map[string]interface{}{
					"timestamp": time.Now().Unix(),
					"type":      "refresh",
				}
				data, _ := json.Marshal(eventData)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}

			time.Sleep(500 * time.Millisecond)
		}
	}
}

func notifyEvent(eventType string) {
	eventMutex.Lock()
	eventCounts["last-event"]++
	eventCounts[eventType] = time.Now().Unix()
	eventMutex.Unlock()
}
