package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// contentServer 返回固定内容的下载服务器
func contentServer(t *testing.T, content string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(content))
	}))
	t.Cleanup(server.Close)

	return server
}

// statusServer 始终以指定状态码响应的下载服务器，用于模拟下载失败
func statusServer(t *testing.T, statusCode int) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
	}))
	t.Cleanup(server.Close)

	return server
}

// hangingServer 在请求被取消前不返回任何数据，用于模拟直连缓慢
// 必须监听请求上下文：否则切换代理时取消请求，服务端 goroutine 会一直挂着不退出
func hangingServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	return server
}

// testUpdaterOptions 汇总测试需要覆盖的配置项
type testUpdaterOptions struct {
	proxyPrefix   string
	slowThreshold time.Duration
	execPath      string
}

// newInstallUpdater 构造一个只依赖测试注入项的升级器
// 可执行文件路径被指向临时文件，因此测试不会触碰真实运行的二进制
func newInstallUpdater(t *testing.T, reporter Reporter, options testUpdaterOptions) *Updater {
	t.Helper()

	updater, err := New(Config{
		Owner:         "owner",
		Repo:          "repo",
		Reporter:      reporter,
		AssetName:     func(string, string) (string, bool) { return testPlatformPrefix, true },
		ProxyPrefix:   options.proxyPrefix,
		SlowThreshold: options.slowThreshold,
		ExecutablePath: func() (string, error) {
			return options.execPath, nil
		},
	})
	if err != nil {
		t.Fatalf("构造升级器失败: %v", err)
	}
	return updater
}

// assertNoStagedFiles 断言目录中没有残留的暂存文件
// 暂存文件在失败路径上必须被清理，否则每次升级失败都会在安装目录留下一个完整大小的二进制
func assertNoStagedFiles(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".upgrade-") {
			t.Fatalf("目录中残留了暂存文件: %s", entry.Name())
		}
	}
}

// TestDownloadWritesExecutableStagedFile 验证下载会写出内容完整且可执行的暂存文件
func TestDownloadWritesExecutableStagedFile(t *testing.T) {
	server := contentServer(t, "new-binary-content")
	reporter := newRecordingReporter()
	updater := newInstallUpdater(t, reporter, testUpdaterOptions{})

	dir := t.TempDir()
	result, err := updater.download(context.Background(), server.URL, dir, directDownloadClient)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	content, err := os.ReadFile(result.path)
	if err != nil {
		t.Fatalf("读取暂存文件失败: %v", err)
	}
	if string(content) != "new-binary-content" {
		t.Fatalf("暂存文件内容 = %q，期望 new-binary-content", string(content))
	}

	// 传输字节数会被上层用于汇总展示，必须与实际内容长度一致
	if want := int64(len("new-binary-content")); result.bytes != want {
		t.Fatalf("传输字节数 = %d，期望 %d", result.bytes, want)
	}

	info, err := os.Stat(result.path)
	if err != nil {
		t.Fatalf("统计暂存文件失败: %v", err)
	}
	// 缺少执行位会让替换后的程序无法启动，因此权限是升级正确性的一部分
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("暂存文件权限 = %v，期望 0755", info.Mode().Perm())
	}

	// 进度展示的生命周期与单次传输绑定，因此 download 自身应当开启并结束它
	progress := reporter.firstProgress()
	if progress == nil {
		t.Fatal("下载应开启进度展示")
	}
	if progress.updates.Load() == 0 {
		t.Fatal("下载过程中应汇报过进度")
	}
	if !progress.done.Load() {
		t.Fatal("单次传输结束后应关闭进度展示")
	}
}

// TestFetchPrintsSummaryAfterProgressEnds 验证汇总信息在进度展示结束之后才输出
// 两者顺序颠倒会让汇总文字接在进度百分比所在行的末尾，与进度条挤在同一行
func TestFetchPrintsSummaryAfterProgressEnds(t *testing.T) {
	server := contentServer(t, "new-binary-content")
	reporter := newRecordingReporter()
	updater := newInstallUpdater(t, reporter, testUpdaterOptions{})

	dir := t.TempDir()
	if _, err := updater.fetch(&UpdateInfo{DownloadURL: server.URL}, dir); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	progressEnd := reporter.indexOfEvent("progress-end")
	summary := reporter.indexOfEvent("Download complete")
	if progressEnd < 0 {
		t.Fatal("传输结束后应关闭进度展示")
	}
	if summary < 0 {
		t.Fatal("下载成功后应输出汇总信息")
	}
	if progressEnd > summary {
		t.Fatalf("汇总信息必须晚于进度结束，实际事件序列: %v", reporter.recordedEvents())
	}
}

// TestDownloadRemovesStagedFileOnFailure 验证下载失败时不留下任何暂存文件
func TestDownloadRemovesStagedFileOnFailure(t *testing.T) {
	server := statusServer(t, http.StatusNotFound)
	updater := newInstallUpdater(t, newRecordingReporter(), testUpdaterOptions{})

	dir := t.TempDir()
	if _, err := updater.download(context.Background(), server.URL, dir, directDownloadClient); err == nil {
		t.Fatal("接口返回 404 时应返回错误")
	}
	assertNoStagedFiles(t, dir)
}

// TestDownloadRejectsTruncatedBody 验证响应体少于声明的长度时报错
// 分块内容的完整性是保证"下载到的就是发布产物"的最低要求
func TestDownloadRejectsTruncatedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write([]byte("truncated"))
	}))
	t.Cleanup(server.Close)

	updater := newInstallUpdater(t, newRecordingReporter(), testUpdaterOptions{})
	dir := t.TempDir()

	if _, err := updater.download(context.Background(), server.URL, dir, directDownloadClient); err == nil {
		t.Fatal("响应体不完整时应返回错误")
	}
	assertNoStagedFiles(t, dir)
}

// TestApplyReplacesExecutable 验证完整升级流程：下载新版本并原子替换目标文件
// 这是升级器最核心的端到端契约
func TestApplyReplacesExecutable(t *testing.T) {
	server := contentServer(t, "new-binary-content")
	reporter := newRecordingReporter()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk")
	if err := os.WriteFile(execPath, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatalf("准备假二进制失败: %v", err)
	}

	updater := newInstallUpdater(t, reporter, testUpdaterOptions{execPath: execPath})
	info := &UpdateInfo{
		CurrentVersion: "1.0.0",
		LatestVersion:  "1.1.0",
		DownloadURL:    server.URL,
		AssetName:      testPlatformPrefix,
	}
	if err := updater.Apply(info); err != nil {
		t.Fatalf("升级失败: %v", err)
	}

	content, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("读取替换后的文件失败: %v", err)
	}
	if string(content) != "new-binary-content" {
		t.Fatalf("替换后内容 = %q，期望 new-binary-content", string(content))
	}

	fileInfo, err := os.Stat(execPath)
	if err != nil {
		t.Fatalf("统计替换后的文件失败: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o755 {
		t.Fatalf("替换后权限 = %v，期望 0755", fileInfo.Mode().Perm())
	}

	assertNoStagedFiles(t, dir)

	progress := reporter.firstProgress()
	if progress == nil {
		t.Fatal("应开启下载进度")
	}
	if !progress.done.Load() {
		t.Fatal("下载结束后应关闭进度展示")
	}
}

// TestApplyKeepsOriginalOnDownloadFailure 验证下载失败不会破坏现有的可执行文件
// 升级失败必须留下一个仍可运行的旧版本，这是自升级的最低安全底线
func TestApplyKeepsOriginalOnDownloadFailure(t *testing.T) {
	server := statusServer(t, http.StatusInternalServerError)

	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk")
	if err := os.WriteFile(execPath, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatalf("准备假二进制失败: %v", err)
	}

	updater := newInstallUpdater(t, newRecordingReporter(), testUpdaterOptions{execPath: execPath})
	info := &UpdateInfo{LatestVersion: "1.1.0", DownloadURL: server.URL}
	if err := updater.Apply(info); err == nil {
		t.Fatal("下载失败时 Apply 应返回错误")
	}

	content, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("读取原文件失败: %v", err)
	}
	if string(content) != "old-binary-content" {
		t.Fatalf("下载失败不应改动原文件，实际内容 = %q", string(content))
	}
	assertNoStagedFiles(t, dir)
}

// TestApplyFallsBackToProxyAfterDirectFailure 验证直连失败后经用户同意可改用代理下载
func TestApplyFallsBackToProxyAfterDirectFailure(t *testing.T) {
	direct := statusServer(t, http.StatusBadGateway)
	proxy := contentServer(t, "proxied-binary-content")

	reporter := newRecordingReporter()
	reporter.confirmAnswer = true

	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk")
	if err := os.WriteFile(execPath, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatalf("准备假二进制失败: %v", err)
	}

	updater := newInstallUpdater(t, reporter, testUpdaterOptions{
		execPath: execPath,
		// 代理前缀与实际地址直接拼接，这正是镜像服务常见的 URL 组织形式
		proxyPrefix: proxy.URL + "/",
	})
	info := &UpdateInfo{LatestVersion: "1.1.0", DownloadURL: direct.URL + "/asset"}
	if err := updater.Apply(info); err != nil {
		t.Fatalf("经代理升级失败: %v", err)
	}

	content, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("读取替换后的文件失败: %v", err)
	}
	if string(content) != "proxied-binary-content" {
		t.Fatalf("替换后内容 = %q，期望来自代理的内容", string(content))
	}

	questions := reporter.askedQuestions()
	if len(questions) == 0 || !strings.Contains(questions[0], "Direct download failed") {
		t.Fatalf("直连失败后应征求用户意见，实际询问 %v", questions)
	}
}

// TestApplyKeepsOriginalWhenProxyDeclined 验证用户拒绝换源时如实报告直连错误且不改动原文件
func TestApplyKeepsOriginalWhenProxyDeclined(t *testing.T) {
	direct := statusServer(t, http.StatusBadGateway)
	proxy := contentServer(t, "proxied-binary-content")

	reporter := newRecordingReporter()
	reporter.confirmAnswer = false

	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk")
	if err := os.WriteFile(execPath, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatalf("准备假二进制失败: %v", err)
	}

	updater := newInstallUpdater(t, reporter, testUpdaterOptions{
		execPath:    execPath,
		proxyPrefix: proxy.URL + "/",
	})
	info := &UpdateInfo{LatestVersion: "1.1.0", DownloadURL: direct.URL + "/asset"}
	if err := updater.Apply(info); err == nil {
		t.Fatal("用户拒绝换源且直连失败时 Apply 应返回错误")
	}

	content, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("读取原文件失败: %v", err)
	}
	if string(content) != "old-binary-content" {
		t.Fatalf("拒绝换源不应改动原文件，实际内容 = %q", string(content))
	}
}

// TestApplySwitchesToProxyWhenDirectIsSlow 验证直连超过阈值时询问用户并切换到代理
// 该路径是慢速网络下升级能够完成的关键，也是"绝不静默换源"约定的具体体现
func TestApplySwitchesToProxyWhenDirectIsSlow(t *testing.T) {
	direct := hangingServer(t)
	proxy := contentServer(t, "proxied-binary-content")

	reporter := newRecordingReporter()
	reporter.confirmAnswer = true

	dir := t.TempDir()
	execPath := filepath.Join(dir, "flk")
	if err := os.WriteFile(execPath, []byte("old-binary-content"), 0o755); err != nil {
		t.Fatalf("准备假二进制失败: %v", err)
	}

	updater := newInstallUpdater(t, reporter, testUpdaterOptions{
		execPath:      execPath,
		proxyPrefix:   proxy.URL + "/",
		slowThreshold: 100 * time.Millisecond,
	})
	info := &UpdateInfo{LatestVersion: "1.1.0", DownloadURL: direct.URL + "/asset"}
	if err := updater.Apply(info); err != nil {
		t.Fatalf("超时切换到代理后升级失败: %v", err)
	}

	content, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("读取替换后的文件失败: %v", err)
	}
	if string(content) != "proxied-binary-content" {
		t.Fatalf("替换后内容 = %q，期望来自代理的内容", string(content))
	}

	questions := reporter.askedQuestions()
	if len(questions) == 0 || !strings.Contains(questions[0], "Direct download is slow") {
		t.Fatalf("直连缓慢时应征求用户意见，实际询问 %v", questions)
	}
}

// TestApplyReturnsDirectErrorWhenPromptFails 验证无法完成询问时如实报告直连错误
// 询问失败的场景（如非交互终端）不能让升级悄悄"成功"，也不能掩盖真实的失败原因
func TestApplyReturnsDirectErrorWhenPromptFails(t *testing.T) {
	direct := statusServer(t, http.StatusBadGateway)
	proxy := contentServer(t, "proxied-binary-content")

	reporter := newRecordingReporter()
	reporter.confirmErr = context.Canceled

	updater := newInstallUpdater(t, reporter, testUpdaterOptions{
		execPath:    filepath.Join(t.TempDir(), "flk"),
		proxyPrefix: proxy.URL + "/",
	})
	info := &UpdateInfo{LatestVersion: "1.1.0", DownloadURL: direct.URL + "/asset"}
	if err := updater.Apply(info); err == nil {
		t.Fatal("无法询问用户且直连失败时 Apply 应返回错误")
	}
}

// TestApplyWithoutProxyStaysDirectOnly 验证未配置代理时不产生任何换源询问
func TestApplyWithoutProxyStaysDirectOnly(t *testing.T) {
	direct := statusServer(t, http.StatusBadGateway)

	reporter := newRecordingReporter()
	reporter.confirmAnswer = true

	updater := newInstallUpdater(t, reporter, testUpdaterOptions{
		execPath: filepath.Join(t.TempDir(), "flk"),
	})
	info := &UpdateInfo{LatestVersion: "1.1.0", DownloadURL: direct.URL + "/asset"}
	if err := updater.Apply(info); err == nil {
		t.Fatal("直连失败时应返回错误")
	}

	if questions := reporter.askedQuestions(); len(questions) != 0 {
		t.Fatalf("未配置代理时不应询问换源，实际询问 %v", questions)
	}
}
