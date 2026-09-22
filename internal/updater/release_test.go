package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// testPlatformPrefix 是测试中使用的资产前缀
// 升级器只按宿主给定的前缀匹配资产，与真实运行平台无关，因此测试无需关心 runtime.GOOS
const testPlatformPrefix = "flk-test-platform"

// recordedRequest 保存假 API 收到的请求要点，用于断言认证与标识是否正确送达
type recordedRequest struct {
	path          string
	authorization string
	userAgent     string
}

// releaseServer 是模拟 GitHub Release 接口的测试服务器
type releaseServer struct {
	*httptest.Server

	mu       sync.Mutex
	received []recordedRequest
}

// newReleaseServer 构造返回指定 Release 列表的假接口，statusCode 用于模拟异常响应
func newReleaseServer(t *testing.T, releases []Release, statusCode int) *releaseServer {
	t.Helper()

	server := &releaseServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		server.received = append(server.received, recordedRequest{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			userAgent:     r.Header.Get("User-Agent"),
		})
		server.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if statusCode == http.StatusOK {
			if err := json.NewEncoder(w).Encode(releases); err != nil {
				t.Errorf("写入假 Release 响应失败: %v", err)
			}
		}
	}))
	t.Cleanup(server.Close)

	return server
}

// lastRequest 返回最后一次收到的请求，未收到请求时返回零值
func (s *releaseServer) lastRequest() recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.received) == 0 {
		return recordedRequest{}
	}
	return s.received[len(s.received)-1]
}

// requestCount 返回收到的请求次数
func (s *releaseServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.received)
}

// platformAsset 构造一个文件名匹配测试前缀的资产
func platformAsset(tag string) Asset {
	return Asset{Name: testPlatformPrefix, DownloadURL: "https://example.invalid/download/" + tag}
}

// foreignAsset 构造一个文件名不匹配当前测试平台的资产，用于验证平台过滤
func foreignAsset(tag string) Asset {
	return Asset{Name: "flk-other-platform", DownloadURL: "https://example.invalid/download/" + tag}
}

// newTestUpdater 构造一个指向假接口、使用测试资产前缀的升级器
func newTestUpdater(t *testing.T, server *releaseServer, reporter Reporter) *Updater {
	t.Helper()

	updater, err := New(Config{
		Owner:       "owner",
		Repo:        "repo",
		APIEndpoint: server.URL,
		Reporter:    reporter,
		AssetName:   func(string, string) (string, bool) { return testPlatformPrefix, true },
	})
	if err != nil {
		t.Fatalf("构造升级器失败: %v", err)
	}
	return updater
}

// TestCheckSelectsHighestStable 验证正式版通道会跳过开发版标签并选中最高版本
func TestCheckSelectsHighestStable(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "v1.0.0", Assets: []Asset{platformAsset("v1.0.0")}},
		{TagName: "v1.2.0.dev.1", Assets: []Asset{platformAsset("v1.2.0.dev.1")}},
		{TagName: "v1.1.5", Assets: []Asset{platformAsset("v1.1.5")}},
		{TagName: "v1.1.0", Assets: []Asset{platformAsset("v1.1.0")}},
	}, http.StatusOK)

	updater := newTestUpdater(t, server, newRecordingReporter())
	info, err := updater.Check("1.0.0", "2026-01-01T00:00:00Z", ChannelStable)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info == nil {
		t.Fatal("期望发现更新，实际判定为已是最新")
	}
	if info.LatestVersion != "v1.1.5" {
		t.Fatalf("最新版本 = %q，期望 v1.1.5", info.LatestVersion)
	}
	if info.DownloadURL != platformAsset("v1.1.5").DownloadURL {
		t.Fatalf("下载地址 = %q，期望指向 v1.1.5 的资产", info.DownloadURL)
	}
}

// TestCheckStableChannelIgnoresDevOnly 验证正式版之外只剩开发版时判定为已是最新
func TestCheckStableChannelIgnoresDevOnly(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "v1.0.1.dev.3", Assets: []Asset{platformAsset("v1.0.1.dev.3")}},
		{TagName: "v1.0.0", Assets: []Asset{platformAsset("v1.0.0")}},
	}, http.StatusOK)

	updater := newTestUpdater(t, server, newRecordingReporter())
	info, err := updater.Check("1.0.0", "", ChannelStable)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info != nil {
		t.Fatalf("正式版通道不应投递开发版，实际得到 %q", info.LatestVersion)
	}
}

// TestCheckDevChannelPicksHighestDev 验证开发版通道只认更高号的开发版
func TestCheckDevChannelPicksHighestDev(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "v1.1.0", Assets: []Asset{platformAsset("v1.1.0")}},
		{TagName: "v1.0.1.dev.3", Assets: []Asset{platformAsset("v1.0.1.dev.3")}},
		{TagName: "v1.0.1.dev.1", Assets: []Asset{platformAsset("v1.0.1.dev.1")}},
	}, http.StatusOK)

	updater := newTestUpdater(t, server, newRecordingReporter())
	info, err := updater.Check("1.0.0", "", ChannelDev)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info == nil || info.LatestVersion != "v1.0.1.dev.3" {
		t.Fatalf("开发版通道应选中 v1.0.1.dev.3，实际为 %#v", info)
	}
}

// TestCheckStableBuildNotDowngradedToSameDev 验证正式版用户不会被引导到同号开发版
// 这是同号开发版低于正式版这一规则在检查流程中的直接体现
func TestCheckStableBuildNotDowngradedToSameDev(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "v1.2.3", Assets: []Asset{platformAsset("v1.2.3")}},
		{TagName: "v1.2.3.dev.9", Assets: []Asset{platformAsset("v1.2.3.dev.9")}},
	}, http.StatusOK)

	updater := newTestUpdater(t, server, newRecordingReporter())
	info, err := updater.Check("1.2.3", "", ChannelDev)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info != nil {
		t.Fatalf("同号开发版不应被视为更新，实际得到 %q", info.LatestVersion)
	}
}

// TestCheckSkipsUnparsableTags 验证无法识别的标签被跳过而不是被猜测含义
func TestCheckSkipsUnparsableTags(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "latest", Assets: []Asset{platformAsset("latest")}},
		{TagName: "v1.2.3-rc.1", Assets: []Asset{platformAsset("v1.2.3-rc.1")}},
		{TagName: "v1.1.0", Assets: []Asset{platformAsset("v1.1.0")}},
	}, http.StatusOK)

	updater := newTestUpdater(t, server, newRecordingReporter())
	info, err := updater.Check("1.0.0", "", ChannelStable)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info == nil || info.LatestVersion != "v1.1.0" {
		t.Fatalf("应跳过无法解析的标签并选中 v1.1.0，实际为 %#v", info)
	}
}

// TestCheckSkipsVersionsWithoutPlatformAsset 验证缺少当前平台资产的更高版本会被跳过
// 否则用户会收到一个无法下载的"更新"
func TestCheckSkipsVersionsWithoutPlatformAsset(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "v1.2.0", Assets: []Asset{foreignAsset("v1.2.0")}},
		{TagName: "v1.1.0", Assets: []Asset{platformAsset("v1.1.0")}},
		{TagName: "v1.0.0", Assets: []Asset{platformAsset("v1.0.0")}},
	}, http.StatusOK)

	updater := newTestUpdater(t, server, newRecordingReporter())
	info, err := updater.Check("1.0.0", "", ChannelStable)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info == nil || info.LatestVersion != "v1.1.0" {
		t.Fatalf("应跳过缺少平台资产的 v1.2.0，实际为 %#v", info)
	}
}

// TestCheckReportsUpToDate 验证没有更高版本时返回空结果而非错误
func TestCheckReportsUpToDate(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "v1.1.0", Assets: []Asset{platformAsset("v1.1.0")}},
		{TagName: "v1.0.0", Assets: []Asset{platformAsset("v1.0.0")}},
	}, http.StatusOK)

	updater := newTestUpdater(t, server, newRecordingReporter())
	info, err := updater.Check("1.1.0", "", ChannelStable)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info != nil {
		t.Fatalf("当前已是最新，不应返回 %#v", info)
	}
}

// TestCheckToleratesUnparsableCurrentVersion 验证本地版本无法解析时只警告而不中断检查
// 本地源码构建常用时间戳充当版本号，这类用户仍应能看到当前通道的最新发布并决定是否升级
func TestCheckToleratesUnparsableCurrentVersion(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "v1.0.0", Assets: []Asset{platformAsset("v1.0.0")}},
		{TagName: "v1.2.0", Assets: []Asset{platformAsset("v1.2.0")}},
		{TagName: "v1.2.1.dev.3", Assets: []Asset{platformAsset("v1.2.1.dev.3")}},
	}, http.StatusOK)

	reporter := newRecordingReporter()
	updater := newTestUpdater(t, server, reporter)

	// 模拟本地构建的时间戳版本号
	info, err := updater.Check("2026-09-22-13-58-33", "", ChannelStable)
	if err != nil {
		t.Fatalf("本地版本无法解析时不应返回错误: %v", err)
	}
	if info == nil {
		t.Fatal("本地版本无法解析时仍应列出通道内最高版本")
	}
	if info.LatestVersion != "v1.2.0" {
		t.Fatalf("通道内最高版本 = %q，期望 v1.2.0", info.LatestVersion)
	}
	// 没有比较基准时不能断言"有更新"，因此必须标记比较未发生
	if info.CurrentComparable {
		t.Fatal("本地版本无法解析时 CurrentComparable 应为 false")
	}
	if !reporter.sawWarning("跳过版本比较") {
		t.Fatal("本地版本无法解析时应发出警告")
	}
	if server.requestCount() == 0 {
		t.Fatal("本地版本无法解析时仍应查询接口以列出最新版本")
	}
}

// TestCheckUnparsableCurrentVersionRespectsChannel 验证无法比较时依然遵守通道过滤
// 否则本地构建在正式版通道下会拿到开发版标签
func TestCheckUnparsableCurrentVersionRespectsChannel(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "v1.2.0", Assets: []Asset{platformAsset("v1.2.0")}},
		{TagName: "v1.2.1.dev.3", Assets: []Asset{platformAsset("v1.2.1.dev.3")}},
	}, http.StatusOK)

	updater := newTestUpdater(t, server, newRecordingReporter())

	info, err := updater.Check("dev", "", ChannelDev)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info == nil || info.LatestVersion != "v1.2.1.dev.3" {
		t.Fatalf("开发版通道应列出最高开发版，实际为 %#v", info)
	}

	info, err = updater.Check("dev", "", ChannelStable)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info == nil || info.LatestVersion != "v1.2.0" {
		t.Fatalf("正式版通道应列出最高正式版，实际为 %#v", info)
	}
}

// TestCheckRejectsUnsupportedPlatform 验证宿主未声明支持的平台会得到明确错误
func TestCheckRejectsUnsupportedPlatform(t *testing.T) {
	server := newReleaseServer(t, []Release{}, http.StatusOK)

	updater, err := New(Config{
		Owner:       "owner",
		Repo:        "repo",
		APIEndpoint: server.URL,
		Reporter:    newRecordingReporter(),
		AssetName:   func(string, string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("构造升级器失败: %v", err)
	}

	if _, err := updater.Check("1.0.0", "", ChannelStable); err == nil {
		t.Fatal("平台不受支持时应返回错误")
	}
}

// TestCheckSendsTokenAndUserAgent 验证认证令牌与请求标识正确送达
func TestCheckSendsTokenAndUserAgent(t *testing.T) {
	server := newReleaseServer(t, []Release{}, http.StatusOK)

	updater, err := New(Config{
		Owner:         "owner",
		Repo:          "repo",
		APIEndpoint:   server.URL,
		Reporter:      newRecordingReporter(),
		AssetName:     func(string, string) (string, bool) { return testPlatformPrefix, true },
		TokenProvider: func() string { return "secret-token" },
		UserAgent:     "test-agent",
	})
	if err != nil {
		t.Fatalf("构造升级器失败: %v", err)
	}

	if _, err := updater.Check("1.0.0", "", ChannelStable); err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}

	received := server.lastRequest()
	if received.authorization != "Bearer secret-token" {
		t.Fatalf("Authorization = %q，期望 Bearer secret-token", received.authorization)
	}
	if received.userAgent != "test-agent" {
		t.Fatalf("User-Agent = %q，期望 test-agent", received.userAgent)
	}
	if received.path != "/repos/owner/repo/releases" {
		t.Fatalf("请求路径 = %q，期望 /repos/owner/repo/releases", received.path)
	}
}

// TestCheckReportsAPIError 验证接口异常会以错误形式上抛而不是被当成无更新
func TestCheckReportsAPIError(t *testing.T) {
	server := newReleaseServer(t, nil, http.StatusForbidden)

	updater := newTestUpdater(t, server, newRecordingReporter())
	_, err := updater.Check("1.0.0", "", ChannelStable)
	if err == nil {
		t.Fatal("接口返回 403 时应返回错误")
	}
}

// TestNewRejectsIncompleteConfig 验证必填项缺失会在构造阶段立即失败
// 这类错误属于宿主接线问题，越早暴露越好，而不是等用户执行升级时才出现
func TestNewRejectsIncompleteConfig(t *testing.T) {
	validAssetName := func(string, string) (string, bool) { return testPlatformPrefix, true }

	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "缺少 Owner", cfg: Config{Repo: "repo", AssetName: validAssetName, Reporter: DiscardReporter()}},
		{name: "缺少 Repo", cfg: Config{Owner: "owner", AssetName: validAssetName, Reporter: DiscardReporter()}},
		{name: "缺少 AssetName", cfg: Config{Owner: "owner", Repo: "repo", Reporter: DiscardReporter()}},
		{name: "缺少 Reporter", cfg: Config{Owner: "owner", Repo: "repo", AssetName: validAssetName}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := New(testCase.cfg); err == nil {
				t.Fatal("配置不完整时应返回错误")
			}
		})
	}
}

// TestNewAppliesDefaults 验证可选配置会被补上合理默认值，使宿主只需提供最少信息即可工作
func TestNewAppliesDefaults(t *testing.T) {
	updater, err := New(Config{
		Owner:     "owner",
		Repo:      "repo",
		Reporter:  DiscardReporter(),
		AssetName: func(string, string) (string, bool) { return testPlatformPrefix, true },
		// APIEndpoint 结尾多余的斜杠应被去除，避免拼出双斜杠路径
		APIEndpoint: "https://api.github.com/",
	})
	if err != nil {
		t.Fatalf("构造升级器失败: %v", err)
	}

	if updater.cfg.APIEndpoint != "https://api.github.com" {
		t.Fatalf("APIEndpoint = %q，期望去除结尾斜杠", updater.cfg.APIEndpoint)
	}
	if updater.cfg.UserAgent != defaultUserAgent {
		t.Fatalf("UserAgent = %q，期望默认值 %q", updater.cfg.UserAgent, defaultUserAgent)
	}
	if updater.cfg.SlowThreshold != defaultSlowThreshold {
		t.Fatalf("SlowThreshold = %v，期望默认值 %v", updater.cfg.SlowThreshold, defaultSlowThreshold)
	}
	if updater.cfg.HTTPClient == nil {
		t.Fatal("HTTPClient 应被补上带超时的默认客户端")
	}
	if updater.cfg.ExecutablePath == nil {
		t.Fatal("ExecutablePath 应默认指向 os.Executable")
	}
}

// TestDiscardReporterIsSilentAndPermissive 验证静默实现不产生输出且在确认点放行
// 无人值守场景直接复用升级器时依赖这两个性质
func TestDiscardReporterIsSilentAndPermissive(t *testing.T) {
	reporter := DiscardReporter()

	// 只需验证不 panic 且确认放行；静默性由无输出保证
	reporter.Info("格式 %s", "参数")
	reporter.Success("完成")

	confirmed, err := reporter.Confirm("是否继续")
	if err != nil || !confirmed {
		t.Fatalf("静默实现应在确认点放行，实际 confirmed=%v err=%v", confirmed, err)
	}

	progress := reporter.Progress("下载进度")
	progress.Update(1, 2)
	progress.Done()
}
