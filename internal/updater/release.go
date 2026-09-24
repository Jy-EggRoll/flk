package updater

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/jy-eggroll/flk/pkg/l10n"
)

// releasePageSize 是单次拉取的 Release 条数
// Release 列表按创建时间倒序返回，目标通道的最新版本必然落在首页，
// 因此只取首页即可，既省流量也避免为几百个历史版本做无谓的解析
const releasePageSize = 50

// Release 是 Release 接口中与升级相关的字段子集
// 只声明必要字段，使上游新增字段时不会影响解析
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset 是 Release 附件中的一个可下载文件
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

// UpdateInfo 描述一次可用的更新
type UpdateInfo struct {
	// CurrentVersion 与 CurrentBuildTime 是发起检查时传入的本地版本信息，仅用于展示，不参与比较
	CurrentVersion   string
	CurrentBuildTime string

	// CurrentComparable 表示本地版本是否成功解析并与候选版本完成了比较
	// 为 false 时 LatestVersion 只代表目标通道中的最高版本，不能据此断言本地版本更旧，
	// 展示层必须据此调整措辞，避免把未经比较的结果说成"有更新的版本"
	CurrentComparable bool

	// LatestVersion 是候选版本的标签原文
	LatestVersion string

	// DownloadURL 与 AssetName 描述当前平台应当下载的资产
	DownloadURL string
	AssetName   string
}

// Check 查询目标通道中是否存在比 current 更新的版本
// 返回值约定：info 为 nil 且 err 为 nil 表示没有可升级的目标
//
// current 无法解析时（典型情形是本地源码构建用时间戳充当版本号）不会中断检查，
// 而是警告后退化为"列出目标通道中的最高版本"：
// 用户依然能看到最新发布并决定是否升级，只是没有比较基准，
// 因此 UpdateInfo.CurrentComparable 会被置为 false，由展示层避免使用"更新"这类断言
func (u *Updater) Check(current, buildTime string, channel Channel) (*UpdateInfo, error) {
	currentVersion, comparable := ParseVersion(current)
	if !comparable {
		u.cfg.Reporter.Warn("%s", l10n.T("Current version {{.Version}} is not a supported version number (expected x.y.z or x.y.z.dev.n); skipping version comparison", map[string]any{"Version": current}))
	}

	goos, goarch := runtime.GOOS, runtime.GOARCH
	assetPrefix, supported := u.cfg.AssetName(goos, goarch)
	if !supported {
		return nil, fmt.Errorf("%s", l10n.T("No release artifact is available for {{.OS}}/{{.Arch}}", map[string]any{"OS": goos, "Arch": goarch}))
	}

	releases, err := u.fetchReleases()
	if err != nil {
		return nil, err
	}

	latest, found := pickLatest(releases, currentVersion, channel, assetPrefix, comparable)
	if !found {
		return nil, nil
	}

	return &UpdateInfo{
		CurrentVersion:    current,
		CurrentBuildTime:  buildTime,
		CurrentComparable: comparable,
		LatestVersion:     latest.TagName,
		DownloadURL:       latest.Assets[0].DownloadURL,
		AssetName:         latest.Assets[0].Name,
	}, nil
}

// fetchReleases 拉取仓库最近的 Release 列表
// 认证令牌只影响限速配额：匿名调用每小时额度很低，配置令牌后不影响其他行为
func (u *Updater) fetchReleases() ([]Release, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/releases?per_page=%d",
		u.cfg.APIEndpoint, u.cfg.Owner, u.cfg.Repo, releasePageSize,
	)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l10n.T("Failed to build the Release query request", nil), err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", u.cfg.UserAgent)
	if u.cfg.TokenProvider != nil {
		if token := u.cfg.TokenProvider(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	resp, err := u.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l10n.T("Failed to query the Release", nil), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", l10n.T("Failed to query the Release, status code: {{.Code}}", map[string]any{"Code": resp.StatusCode}))
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("%s: %w", l10n.T("Failed to parse the Release response", nil), err)
	}
	return releases, nil
}

// pickLatest 在候选列表中挑选版本最高、且为当前平台提供资产的那一个
// 筛选规则依次为：
//  1. 通道过滤：正式版通道忽略开发版标签，开发版通道忽略正式版标签
//  2. 只看更新：本地版本可比时，候选必须严格高于本地版本，相等或更低一律跳过
//  3. 平台可用：缺少当前平台资产的版本视为不提供该平台，继续向前寻找
//  4. 无法解析的标签直接跳过，绝不猜测其含义
//
// comparable 为 false 表示本地版本无法解析，此时缺少比较基准，规则 2 被跳过，
// 结果是返回通道内版本最高的可用版本
//
// 其中"正式版用户不会被带到同号开发版"由 Version.Compare 内建的
// "同号开发版低于正式版"规则自然覆盖，无需在此额外特判
func pickLatest(releases []Release, current Version, channel Channel, assetPrefix string, comparable bool) (Release, bool) {
	var best Release
	var bestVersion Version
	var found bool

	for _, release := range releases {
		candidate, ok := ParseVersion(release.TagName)
		if !ok {
			continue
		}
		if channel == ChannelDev && !candidate.IsDev {
			continue
		}
		if channel == ChannelStable && candidate.IsDev {
			continue
		}
		if comparable && candidate.Compare(current) <= 0 {
			continue
		}
		if found && candidate.Compare(bestVersion) <= 0 {
			continue
		}

		asset, ok := matchAsset(release.Assets, assetPrefix)
		if !ok {
			continue
		}

		// 只保留命中的资产，使调用方无需再关心同一 Release 中的其他平台产物
		best = Release{TagName: release.TagName, Assets: []Asset{asset}}
		bestVersion = candidate
		found = true
	}

	return best, found
}

// matchAsset 按前缀匹配资产名
// 采用前缀而非全等匹配，是为了容忍发布产物在平台标识之后附加的额外后缀，
// 而前缀本身由宿主构造并已包含系统与架构，因此不会误匹配到其他平台
func matchAsset(assets []Asset, prefix string) (Asset, bool) {
	for _, asset := range assets {
		if strings.HasPrefix(asset.Name, prefix) {
			return asset, true
		}
	}
	return Asset{}, false
}
