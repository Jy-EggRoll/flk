package cmd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jy-eggroll/flk/internal/logger"
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

		// API: Get Store Data
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

			// 可以接收 JSON 参数用于过滤，当前先实现全量检查
			var opts CheckOptions
			if r.Body != nil && r.ContentLength > 0 {
				json.NewDecoder(r.Body).Decode(&opts)
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
			// 获取所有无效链接并尝试全部修复 (模拟 fix --all)
			// 注意：这会触发 terminal output，后续需重构这部分避免污染控制台，或者暂时允许
			oldFixAll := fixAll
			fixAll = true

			// 直接执行现有的 fix 逻辑 (重用 CLI)
			RunFix(nil, nil)

			fixAll = oldFixAll
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
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			oldDevice := createDevice
			createDevice = req.Device
			if createDevice == "" {
				createDevice = "all"
			}

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

			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success": true}`))
		})

		logger.Info("Starting server at http://" + addr)
		fmt.Printf("服务已启动，请在浏览器访问: http://localhost:%d\n", port)
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
