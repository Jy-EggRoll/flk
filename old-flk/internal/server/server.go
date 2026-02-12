// Package server 提供 Web 服务器功能
// 使用 Gin 框架实现 RESTful API 和 Web 界面
// 主要功能包括：
// 1. 显示链接检查结果
// 2. 刷新检查链接状态
// 3. 创建新的符号链接或硬链接
package server

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"file-link-manager/internal/interact"
	"file-link-manager/internal/location"
	"file-link-manager/internal/pathutil"
	"file-link-manager/internal/storage"
)

// Server Web 服务器结构体
// 封装了 Gin 引擎和服务器配置
type Server struct {
	port   int         // 服务器监听端口
	engine *gin.Engine // Gin 引擎，处理 HTTP 请求
}

// LinkInfo 链接信息结构体
// 用于在 Web 界面展示链接详情
type LinkInfo struct {
	Type       string `json:"type"`        // 链接类型："symlink" 或 "hardlink"
	Source     string `json:"source"`      // 源路径（真实文件/主要文件）
	Target     string `json:"target"`      // 目标路径（链接文件/次要文件）
	Status     string `json:"status"`      // 状态："valid"、"invalid" 或具体错误
	StatusText string `json:"status_text"` // 状态的中文描述
	Device     string `json:"device"`      // 设备标识
	Location   string `json:"location"`    // 记录所在的位置（file-link-manager-links.json 的目录）
}

// CheckResponse 检查结果响应结构体
// 用于 API 返回检查结果
type CheckResponse struct {
	Success          bool       `json:"success"`           // 操作是否成功
	Message          string     `json:"message"`           // 提示消息
	Links            []LinkInfo `json:"links"`             // 链接列表
	TotalSymlinks    int        `json:"total_symlinks"`    // 符号链接总数
	ValidSymlinks    int        `json:"valid_symlinks"`    // 有效符号链接数
	InvalidSymlinks  int        `json:"invalid_symlinks"`  // 无效符号链接数
	TotalHardlinks   int        `json:"total_hardlinks"`   // 硬链接总数
	ValidHardlinks   int        `json:"valid_hardlinks"`   // 有效硬链接数
	InvalidHardlinks int        `json:"invalid_hardlinks"` // 无效硬链接数
}

// CreateRequest 创建链接请求结构体
// 用于解析创建链接的 POST 请求
type CreateRequest struct {
	Type   string `json:"type" binding:"required"`   // 链接类型："symlink" 或 "hardlink"
	Source string `json:"source" binding:"required"` // 源路径
	Target string `json:"target" binding:"required"` // 目标路径
	Device string `json:"device"`                    // 设备标识，可选
	Force  bool   `json:"force"`                     // 是否强制覆盖
}

// CreateResponse 创建链接响应结构体
type CreateResponse struct {
	Success bool   `json:"success"` // 操作是否成功
	Message string `json:"message"` // 提示消息
	Output  string `json:"output"`  // 命令输出
}

// FixRequest 修复请求结构体
type FixRequest struct {
	Type   string `json:"type"`   // "symlink", "hardlink", "all"
	Device string `json:"device"` // 设备名称
	Auto   bool   `json:"auto"`   // 是否自动修复
}

// FixResponse 修复响应结构体
type FixResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Output  string `json:"output"`
}

// NewServer 创建一个新的 Web 服务器实例
// port: 服务器监听端口
func NewServer(port int) *Server {
	// 设置 Gin 为发布模式，减少日志输出
	gin.SetMode(gin.ReleaseMode)

	// 创建 Gin 引擎
	// gin.New() 创建一个没有任何中间件的引擎
	// 我们手动添加需要的中间件以获得更好的控制
	engine := gin.New()

	// 添加恢复中间件
	// Recovery 中间件可以捕获 panic 并返回 500 错误，防止服务器崩溃
	engine.Use(gin.Recovery())

	// 添加自定义日志中间件
	engine.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		// 自定义日志格式，使用中文
		return fmt.Sprintf("[Web] %s | %3d | %13v | %15s | %s %s\n",
			param.TimeStamp.Format("2006-01-02 15:04:05"),
			param.StatusCode,
			param.Latency,
			param.ClientIP,
			param.Method,
			param.Path,
		)
	}))

	return &Server{
		port:   port,
		engine: engine,
	}
}

// Start 启动 Web 服务器
// 这是一个阻塞调用，服务器会一直运行直到出错或被中断
func (s *Server) Start() error {
	// 注册路由
	s.setupRoutes()

	// 构造监听地址
	addr := fmt.Sprintf(":%d", s.port)

	// 打印启动信息
	interact.PrintSuccess("Web 服务器已启动")
	fmt.Printf("访问地址：http://localhost:%d\n", s.port)
	fmt.Println("按 Ctrl+C 停止服务器")
	fmt.Println()

	// 启动一个后台协程，在服务器可用后自动打开默认浏览器，改善用户体验
	go func() {
		url := fmt.Sprintf("http://localhost:%d", s.port) // 生成访问 URL
		// 等待服务器就绪，最多重试若干次
		maxAttempts := 30
		for i := 0; i < maxAttempts; i++ {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", s.port), 200*time.Millisecond)
			if err == nil {
				conn.Close()
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		// 尝试打开浏览器，但忽略返回错误，不影响服务器运行
		_ = openBrowser(url)
	}()

	// 启动服务器（阻塞调用），Run 方法会阻塞当前 goroutine，直到服务器停止
	return s.engine.Run(addr)
}

// openBrowser 尝试在操作系统上打开默认浏览器并访问指定 URL，现在只对 Windows 提供支持
// 这是一个轻量函数，采用常见的平台命令：
//   - windows：cmd /c start "" <url>
//
// 返回错误时仅用于记录或显示，不会阻止服务器运行
func openBrowser(url string) error {
	// 根据操作系统执行不同的命令
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// 在 Windows 上使用 cmd 的 start 命令，注意第一个参数为空字符串用于标题占位
		cmd = exec.Command("cmd", "/c", "start", "", url)
	}
	// 启动进程并返回执行结果，不等待输出
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

// setupRoutes 设置路由
// 定义所有的 HTTP 端点
func (s *Server) setupRoutes() {
	// 首页 - 显示 Web 界面
	s.engine.GET("/", s.handleIndex)

	// API 路由组
	// 使用路由组可以为一组路由添加共同的前缀和中间件
	api := s.engine.Group("/api")
	{
		// 检查链接状态
		api.GET("/check", s.handleCheck)

		// 刷新检查（执行检查并返回结果）
		api.POST("/refresh", s.handleRefresh)

		// 创建链接
		api.POST("/create", s.handleCreate)

		// 修复链接
		api.POST("/fix", s.handleFix)

		// 获取位置列表
		api.GET("/locations", s.handleLocations)
	}
}

// handleIndex 处理首页请求
// 返回嵌入的 HTML 页面
func (s *Server) handleIndex(c *gin.Context) {
	// 使用 c.Data 直接返回 HTML 内容
	// 第一个参数是 HTTP 状态码
	// 第二个参数是 Content-Type
	// 第三个参数是响应体
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(indexHTML))
}

// handleCheck 处理检查请求
// 返回当前所有链接的状态
func (s *Server) handleCheck(c *gin.Context) {
	// 调用内部检查函数
	response := s.performCheck()

	// 返回 JSON 响应
	c.JSON(http.StatusOK, response)
}

// handleRefresh 处理刷新请求
// 执行检查并返回结果（与 handleCheck 相同，但使用 POST 方法）
func (s *Server) handleRefresh(c *gin.Context) {
	// 调用内部检查函数
	response := s.performCheck()

	// 返回 JSON 响应
	c.JSON(http.StatusOK, response)
}

// handleCreate 处理创建链接请求
// 使用 ShellExecute 以管理员权限执行创建命令
func (s *Server) handleCreate(c *gin.Context) {
	// 解析请求体
	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, CreateResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误：%v", err),
		})
		return
	}

	// 验证参数
	if req.Type != "symlink" && req.Type != "hardlink" {
		c.JSON(http.StatusBadRequest, CreateResponse{
			Success: false,
			Message: "链接类型必须是 symlink 或 hardlink",
		})
		return
	}

	// 验证路径不为空
	req.Source = strings.TrimSpace(req.Source)
	req.Target = strings.TrimSpace(req.Target)
	if req.Source == "" || req.Target == "" {
		c.JSON(http.StatusBadRequest, CreateResponse{
			Success: false,
			Message: "源路径和目标路径不能为空",
		})
		return
	}

	// 构建命令参数
	var output string
	var err error

	if req.Type == "symlink" {
		// 创建符号链接
		// 在 Windows 上需要管理员权限，使用 ShellExecute 提升权限
		output, err = s.executeWithElevation(req.Type, req.Source, req.Target, req.Device, req.Force)
	} else {
		// 创建硬链接
		// 硬链接不需要管理员权限
		output, err = s.executeCommand(req.Type, req.Source, req.Target, req.Device, req.Force)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateResponse{
			Success: false,
			Message: fmt.Sprintf("创建链接失败：%v", err),
			Output:  output,
		})
		return
	}

	c.JSON(http.StatusOK, CreateResponse{
		Success: true,
		Message: "链接创建成功",
		Output:  output,
	})
}

// handleFix 处理修复链接请求
func (s *Server) handleFix(c *gin.Context) {
	var req FixRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, FixResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误：%v", err),
		})
		return
	}

	// 获取当前可执行文件路径
	exePath, err := os.Executable()
	if err != nil {
		c.JSON(http.StatusInternalServerError, FixResponse{
			Success: false,
			Message: fmt.Sprintf("获取可执行文件路径失败：%v", err),
		})
		return
	}

	// 构造命令参数
	args := []string{"fix"}

	if req.Type == "symlink" {
		args = append(args, "--symlink")
	} else if req.Type == "hardlink" {
		args = append(args, "--hardlink")
	}

	if req.Device != "" {
		args = append(args, "--device", req.Device)
	}

	// Web 接口强制使用自动模式，因为无法进行交互
	args = append(args, "--auto")

	// 执行命令
	cmd := exec.Command(exePath, args...)

	// 捕获输出
	output, err := cmd.CombinedOutput()

	if err != nil {
		c.JSON(http.StatusOK, FixResponse{
			Success: false,
			Message: fmt.Sprintf("执行修复失败：%v", err),
			Output:  string(output),
		})
		return
	}

	c.JSON(http.StatusOK, FixResponse{
		Success: true,
		Message: "修复完成",
		Output:  string(output),
	})
}

// handleLocations 处理获取位置列表请求
func (s *Server) handleLocations(c *gin.Context) {
	// 创建位置管理器
	locMgr, err := location.NewManager()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("创建位置管理器失败：%v", err),
		})
		return
	}

	// 加载位置记录
	if err := locMgr.Load(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("加载位置记录失败：%v", err),
		})
		return
	}

	// 获取当前操作系统的位置
	osType := pathutil.GetCurrentOS()
	locations := locMgr.GetLocations(osType)

	c.JSON(http.StatusOK, gin.H{
		"success":   true,
		"locations": locations,
		"os_type":   osType,
	})
}

// performCheck 执行链接检查
// 遍历所有已记录的位置，检查每个位置的链接状态
func (s *Server) performCheck() CheckResponse {
	response := CheckResponse{
		Success: true,
		Links:   []LinkInfo{},
	}

	// 创建位置管理器
	locMgr, err := location.NewManager()
	if err != nil {
		response.Success = false
		response.Message = fmt.Sprintf("创建位置管理器失败：%v", err)
		return response
	}

	// 加载位置记录
	if err := locMgr.Load(); err != nil {
		response.Success = false
		response.Message = fmt.Sprintf("加载位置记录失败：%v", err)
		return response
	}

	// 获取当前操作系统的位置
	osType := pathutil.GetCurrentOS()
	locations := locMgr.GetLocations(osType)

	// 如果没有记录的位置，检查当前目录
	if len(locations) == 0 {
		cwd, err := os.Getwd()
		if err == nil {
			st := storage.NewStorage(cwd)
			if st.FileExists() {
				locations = append(locations, cwd)
			}
		}
	}

	if len(locations) == 0 {
		response.Message = "没有找到已记录的位置"
		return response
	}

	// 遍历所有位置进行检查
	for _, loc := range locations {
		// 创建 storage
		st := storage.NewStorage(loc)

		// 检查文件是否存在
		if !st.FileExists() {
			continue
		}

		// 加载 storage
		if err := st.Load(); err != nil {
			continue
		}

		// 获取符号链接记录
		symlinks := st.GetSymlinks(osType)
		for _, record := range symlinks {
			// 将相对路径转换为绝对路径
			realPath, err := pathutil.ToAbsolute(loc, record.RealRelative)
			if err != nil {
				continue
			}
			fakePath := record.FakeAbsolute

			// 检查链接状态
			status, statusText := s.checkSymlinkStatus(realPath, fakePath)

			// 添加到结果列表
			response.Links = append(response.Links, LinkInfo{
				Type:       "symlink",
				Source:     realPath,
				Target:     fakePath,
				Status:     status,
				StatusText: statusText,
				Device:     record.Device,
				Location:   loc,
			})

			// 统计
			response.TotalSymlinks++
			if status == "valid" {
				response.ValidSymlinks++
			} else {
				response.InvalidSymlinks++
			}
		}

		// 获取硬链接记录
		hardlinks := st.GetHardlinks(osType)
		for _, record := range hardlinks {
			// 将相对路径转换为绝对路径
			primPath, err := pathutil.ToAbsolute(loc, record.PrimaryRelative)
			if err != nil {
				continue
			}
			secoPath := record.SecondaryAbsolute

			// 检查链接状态
			status, statusText := s.checkHardlinkStatus(primPath, secoPath)

			// 添加到结果列表
			response.Links = append(response.Links, LinkInfo{
				Type:       "hardlink",
				Source:     primPath,
				Target:     secoPath,
				Status:     status,
				StatusText: statusText,
				Device:     record.Device,
				Location:   loc,
			})

			// 统计
			response.TotalHardlinks++
			if status == "valid" {
				response.ValidHardlinks++
			} else {
				response.InvalidHardlinks++
			}
		}
	}

	response.Message = fmt.Sprintf("检查完成：符号链接 %d/%d 有效，硬链接 %d/%d 有效",
		response.ValidSymlinks, response.TotalSymlinks,
		response.ValidHardlinks, response.TotalHardlinks)

	return response
}

// checkSymlinkStatus 检查符号链接状态
// 返回: (状态, 状态描述)
func (s *Server) checkSymlinkStatus(realPath, fakePath string) (string, string) {
	// 检查真实路径是否存在
	if _, err := os.Stat(realPath); os.IsNotExist(err) {
		return "invalid", "源文件不存在"
	}

	// 检查链接路径是否存在
	linfo, err := os.Lstat(fakePath)
	if os.IsNotExist(err) {
		return "invalid", "链接文件不存在"
	}
	if err != nil {
		return "invalid", fmt.Sprintf("检查链接失败：%v", err)
	}

	// 检查是否为符号链接
	if linfo.Mode()&os.ModeSymlink == 0 {
		return "invalid", "不是符号链接"
	}

	// 检查符号链接目标
	target, err := os.Readlink(fakePath)
	if err != nil {
		return "invalid", fmt.Sprintf("读取链接目标失败：%v", err)
	}

	// 规范化路径进行比较
	targetAbs, _ := filepath.Abs(target)
	realAbs, _ := filepath.Abs(realPath)

	// 在 Windows 上忽略大小写
	if runtime.GOOS == "windows" {
		targetAbs = strings.ToLower(targetAbs)
		realAbs = strings.ToLower(realAbs)
	}

	if targetAbs != realAbs {
		return "invalid", fmt.Sprintf("链接目标不匹配，期望：%s，实际：%s", realPath, target)
	}

	return "valid", "正常"
}

// checkHardlinkStatus 检查硬链接状态
// 返回: (状态, 状态描述)
func (s *Server) checkHardlinkStatus(primPath, secoPath string) (string, string) {
	// 检查主要文件是否存在
	primInfo, err := os.Stat(primPath)
	if os.IsNotExist(err) {
		return "invalid", "主要文件不存在"
	}
	if err != nil {
		return "invalid", fmt.Sprintf("检查主要文件失败：%v", err)
	}

	// 检查次要文件是否存在
	secoInfo, err := os.Stat(secoPath)
	if os.IsNotExist(err) {
		return "invalid", "次要文件不存在"
	}
	if err != nil {
		return "invalid", fmt.Sprintf("检查次要文件失败：%v", err)
	}

	// 检查是否为同一个 inode（硬链接的特征）
	if !os.SameFile(primInfo, secoInfo) {
		return "invalid", "两个文件不是硬链接关系"
	}

	return "valid", "正常"
}

// executeCommand 执行创建链接命令（不需要提升权限）
func (s *Server) executeCommand(linkType, source, target, device string, force bool) (string, error) {
	// 获取当前可执行文件路径
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取可执行文件路径失败：%w", err)
	}

	// 构建命令参数
	args := []string{"create", linkType}

	if linkType == "symlink" {
		args = append(args, "--real", source, "--fake", target)
	} else {
		args = append(args, "--prim", source, "--seco", target)
	}

	if device != "" {
		args = append(args, "--device", device)
	}

	if force {
		args = append(args, "--force")
	}

	// 执行命令
	cmd := exec.Command(exePath, args...)
	output, err := cmd.CombinedOutput()

	return string(output), err
}

// executeWithElevation 使用提升权限执行命令（Windows 专用）
// 在 Windows 上使用 ShellExecute 弹出 UAC 提示
func (s *Server) executeWithElevation(linkType, source, target, device string, force bool) (string, error) {
	// 获取当前可执行文件路径
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取可执行文件路径失败：%w", err)
	}

	// 构建命令参数
	var args []string
	args = append(args, "create", linkType)

	if linkType == "symlink" {
		args = append(args, "--real", source, "--fake", target)
	} else {
		args = append(args, "--prim", source, "--seco", target)
	}

	if device != "" {
		args = append(args, "--device", device)
	}

	if force {
		args = append(args, "--force")
	}

	// 非 Windows 系统，直接执行
	cmd := exec.Command(exePath, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
