package cmd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/spf13/cobra"
)

//go:embed ui/index.html
var indexHTML []byte

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "打开网页服务器",
	Long:  "打开网页服务器，提供可视化管理界面",
	Run: func(cmd *cobra.Command, args []string) {
		port, _ := cmd.Flags().GetInt("port")
		host, _ := cmd.Flags().GetString("host")
		addr := fmt.Sprintf("%s:%d", host, port)

		// Serve HTML
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(indexHTML)
		})

		// API: Config (Get/Set WorkDir)
		http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				w.Header().Set("Content-Type", "application/json")
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
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]bool{"success": true})
				return
			}
		})

		// API: Get/Set Store Data
		http.HandleFunc("/api/store", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				w.Header().Set("Content-Type", "application/json")
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
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		})

		// API: Check Links
		http.HandleFunc("/api/cmd/check", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			var req struct {
				Device string `json:"device"`
				Type   string `json:"type"` // all, symlink, hardlink
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
		})

		// API: Fix Link (Single or All)
		http.HandleFunc("/api/cmd/fix", func(w http.ResponseWriter, r *http.Request) {
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

			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success": true}`))
		})

		// API: Delete single link
		http.HandleFunc("/api/cmd/delete", func(w http.ResponseWriter, r *http.Request) {
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

			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success": true}`))
		})

		// API: Create Link
		http.HandleFunc("/api/cmd/create", func(w http.ResponseWriter, r *http.Request) {
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
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success": true}`))
		})

		logger.Info("Starting server at http://" + addr)
		fmt.Printf("服务已启动，请在浏览器访问: http://localhost:%d\n", port)

		if runtime.GOOS == "windows" && isDirectRun {
			go func() {
				exec.Command("cmd", "/c", "start", fmt.Sprintf("http://localhost:%d", port)).Start()
			}()
		}

		if err := http.ListenAndServe(addr, nil); err != nil {
			logger.Error("Server error: " + err.Error())
		}
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().IntP("port", "p", 8999, "指定端口号")
	serveCmd.Flags().String("host", "127.0.0.1", "指定绑定的 Host (默认 127.0.0.1)")
}
