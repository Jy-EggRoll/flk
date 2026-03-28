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

		// 占位 API：其他 CLI 命令 (由更强大的模型补全)
		http.HandleFunc("/api/cmd/create", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				w.Write([]byte("create function placeholder"))
			}
		})
		http.HandleFunc("/api/cmd/check", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				w.Write([]byte("check function placeholder"))
			}
		})
		http.HandleFunc("/api/cmd/fix", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				w.Write([]byte("fix function placeholder"))
			}
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
