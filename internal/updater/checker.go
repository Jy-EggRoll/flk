package updater

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
)

const (
	Owner      = "Jy-EggRoll"
	Repo       = "flk"
	APIBaseURL = "https://api.github.com/repos"
)

var supportedPlatforms = map[string]map[string]bool{
	"windows": {"386": true, "amd64": true, "arm64": true},
	"linux":   {"386": true, "amd64": true, "arm": true, "arm64": true},
	"darwin":  {"amd64": true, "arm64": true},
	"freebsd": {"amd64": true, "arm64": true},
}

type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

type UpdateInfo struct {
	CurrentVersion   string
	CurrentBuildTime string
	LatestVersion    string
	DownloadURL      string
	AssetName        string
}

type Version struct {
	major int
	minor int
	patch int
	dev   int
	isDev bool
}

var (
	releaseRegex = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)
	devRegex     = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)\.dev\.(\d+)$`)
)

func CheckForUpdate(currentVersion, buildTime string, checkDev bool) (*UpdateInfo, error) {
	releases, err := fetchAllReleases()
	if err != nil {
		return nil, err
	}

	goos, goarch := runtime.GOOS, runtime.GOARCH
	if !isSupported(goos, goarch) {
		return nil, fmt.Errorf("平台 %s/%s 不支持", goos, goarch)
	}

	latest := findLatest(releases, currentVersion, checkDev, goos, goarch)
	if latest == nil {
		return nil, nil
	}

	return &UpdateInfo{
		CurrentVersion:   currentVersion,
		CurrentBuildTime: buildTime,
		LatestVersion:    latest.TagName,
		DownloadURL:      latest.Assets[0].DownloadURL,
		AssetName:        latest.Assets[0].Name,
	}, nil
}

func fetchAllReleases() ([]Release, error) {
	url := fmt.Sprintf("%s/%s/%s/releases?per_page=50", APIBaseURL, Owner, Repo)

	// 优先使用用户的 GitHub 令牌（环境变量 GITHUB_TOKEN/GH_TOKEN 或本地 gh CLI 已登录）
	// 认证后速率限制从每小时 60 次（匿名，按共享公网 IP 计，易在公用机器上被限速 403）
	// 提升到 5000 次（按用户计），可彻底规避公用机器 IP 被限速的问题
	token := resolveGitHubToken()
	if token != "" {
		logger.Debug("使用 GitHub 令牌进行认证更新检查")
	}

	// 更新检查也走代理回退：先试 gh-proxy 加速的 API，失败再直连官方 API
	// 注意：GitHub 对匿名 API 限制每小时 60 次（按公网 IP 计），超出返回 403，
	// 代理可分散 IP 压力；若仍 403 则给出速率限制友好提示而非笼统报错
	proxyURL := ghProxyPrefix + url
	releases, err := fetchReleasesFromURL(proxyURL, token)
	if err != nil {
		logger.Info("代理检查更新失败，尝试直连官方 API...")
		return fetchReleasesFromURL(url, token)
	}
	return releases, nil
}

// resolveGitHubToken 解析用于 GitHub API 认证的令牌，按以下优先级：
// 1. 环境变量 GITHUB_TOKEN / GH_TOKEN（与 gh 自身读取方式一致）
// 2. 本地已登录的 gh CLI：通过 `gh auth token` 获取（令牌存于系统钥匙串时不落盘，安全）
// 返回空字符串表示无可用令牌，此时退回匿名请求
func resolveGitHubToken() string {
	if t := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); t != "" {
		return t
	}
	if t := strings.TrimSpace(os.Getenv("GH_TOKEN")); t != "" {
		return t
	}
	if ghExe, err := exec.LookPath("gh"); err == nil {
		if out, err := exec.Command(ghExe, "auth", "token").Output(); err == nil {
			if t := strings.TrimSpace(string(out)); t != "" {
				return t
			}
		}
	}
	return ""
}

// fetchReleasesFromURL 从指定 URL 拉取发布列表，并对 403 速率限制给出更友好的错误
// token 非空时会带上 Authorization: Bearer 头进行认证请求
func fetchReleasesFromURL(url, token string) ([]Release, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "flk-updater")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden {
			// GitHub 匿名 API 超限返回 403，读取 x-ratelimit-reset 给出倒计时提示
			if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
				return nil, fmt.Errorf("GitHub API 速率限制已耗尽，请于 %s (UTC) 后重试，或稍后再试；也可设置 GITHUB_TOKEN 或登录 gh CLI 提升限额", reset)
			}
			return nil, fmt.Errorf("GitHub API 返回 403（可能是速率限制或被拒绝），请稍后重试；也可设置 GITHUB_TOKEN 或登录 gh CLI 提升限额")
		}
		return nil, fmt.Errorf("请求失败，状态码: %d", resp.StatusCode)
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("解析失败: %w", err)
	}
	return releases, nil
}

func findLatest(releases []Release, current string, checkDev bool, goos, goarch string) *Release {
	currentVer := parseVersion(current)

	var best *Release
	var bestVer Version

	for i := range releases {
		tag := releases[i].TagName
		relVer := parseVersion(tag)

		if checkDev && !relVer.isDev {
			continue
		}
		if !checkDev && relVer.isDev {
			continue
		}

		if !currentVer.isDev && relVer.isDev && relVer.major == currentVer.major && relVer.minor == currentVer.minor && relVer.patch == currentVer.patch {
			continue
		}

		if relVer.lessOrEqual(currentVer) {
			continue
		}

		if best == nil || relVer.greaterThan(bestVer) {
			asset := findAsset(releases[i].Assets, goos, goarch)
			if asset != nil {
				releases[i].Assets = []Asset{*asset}
				best = &releases[i]
				bestVer = relVer
			}
		}
	}

	return best
}

func parseVersion(v string) Version {
	v = strings.TrimPrefix(v, "v")

	if m := devRegex.FindStringSubmatch(v); m != nil {
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		patch, _ := strconv.Atoi(m[3])
		dev, _ := strconv.Atoi(m[4])
		return Version{major: major, minor: minor, patch: patch, dev: dev, isDev: true}
	}

	if m := releaseRegex.FindStringSubmatch(v); m != nil {
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		patch, _ := strconv.Atoi(m[3])
		return Version{major: major, minor: minor, patch: patch, dev: 0, isDev: false}
	}

	return Version{isDev: true}
}

func (v Version) lessOrEqual(other Version) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	if v.patch != other.patch {
		return v.patch < other.patch
	}
	if v.isDev != other.isDev {
		return v.isDev
	}
	if v.isDev {
		return v.dev <= other.dev
	}
	return true
}

func (v Version) greaterThan(other Version) bool {
	if v.major != other.major {
		return v.major > other.major
	}
	if v.minor != other.minor {
		return v.minor > other.minor
	}
	if v.patch != other.patch {
		return v.patch > other.patch
	}
	if v.isDev != other.isDev {
		return v.isDev
	}
	if v.isDev {
		return v.dev > other.dev
	}
	return false
}

func isSupported(goos, goarch string) bool {
	archs, ok := supportedPlatforms[goos]
	return ok && archs[goarch]
}

func findAsset(assets []Asset, goos, goarch string) *Asset {
	prefix := fmt.Sprintf("flk-%s-%s", goos, goarch)
	if goos == "windows" {
		prefix += ".exe"
	}

	for i := range assets {
		if strings.HasPrefix(assets[i].Name, prefix) {
			return &assets[i]
		}
	}
	return nil
}
