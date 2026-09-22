package cmd

import "fmt"

// flk 的发布目标定义：仓库坐标、产物命名与备用下载源
// 这些取值来自 flk 自身的发布约定，与升级器的通用能力无关，因此留在命令层而不是 updater 内部，
// 使升级器可以不加修改地被其他项目复用
const (
	// upstreamOwner 与 upstreamRepo 指向承载 Release 的仓库
	upstreamOwner = "Jy-EggRoll"
	upstreamRepo  = "flk"

	// assetNamePrefix 是发布产物的固定文件名前缀，必须与 Taskfile 的构建输出名保持一致
	assetNamePrefix = "flk"

	// downloadProxyPrefix 是直连不稳定时供用户选择的备用下载代理前缀
	// 是否启用完全由用户在升级过程中的确认决定，程序不会静默切换下载源
	downloadProxyPrefix = "https://gh-proxy.org/"
)

// supportedPlatforms 声明 flk 实际发布产物的系统与架构组合，必须与 Taskfile 的 build-all 目标保持同步
// 少列组合会让用户在对应平台上收到"尚未提供发布产物"，多列则不存在的组合会被判为无可用资产并继续寻找旧版本
var supportedPlatforms = map[string]map[string]bool{
	"windows": {"386": true, "amd64": true, "arm64": true},
	"linux":   {"386": true, "amd64": true, "arm": true, "arm64": true},
	"darwin":  {"amd64": true, "arm64": true},
	"freebsd": {"amd64": true, "arm64": true},
}

// flkAssetName 返回指定平台对应的资产文件名前缀
// ok 为 false 表示 flk 不为该平台发布产物，升级器会据此给出明确提示而不会误报"已是最新"
func flkAssetName(goos, goarch string) (string, bool) {
	architectures, known := supportedPlatforms[goos]
	if !known || !architectures[goarch] {
		return "", false
	}

	prefix := fmt.Sprintf("%s-%s-%s", assetNamePrefix, goos, goarch)
	if goos == "windows" {
		// 把扩展名纳入前缀，使匹配结果精确到 Windows 产物本身，
		// 避免将来同时存在带与不带扩展名的同类文件时命中错误的目标
		prefix += ".exe"
	}
	return prefix, true
}
