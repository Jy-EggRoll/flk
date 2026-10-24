package cmd

import "github.com/jy-eggroll/eggokit/upgradecli"

// flkTarget 声明 flk 自升级的发布目标，交给 eggokit/upgradecli 的共享升级命令使用。
//
// 这些取值来自 flk 自己的发布约定，与升级器的通用能力无关，所以留在命令层而不是升级库内部。
// 平台表必须与 Taskfile 的 build-all 目标、仓库根的 release.json 保持同步：
// 少列一个组合，对应平台的用户会收到“不提供该平台产物”；多列一个不存在的组合，
// 升级器会把它当成无可用资产而继续向前寻找旧版本。两种偏差都会让用户拿不到本该能装上的新版本。
//
// AssetPrefix 必须与构建输出名 flk-<os>-<arch>[.exe] 一致：改名产物或改名这里的任一处，
// 升级器就再也匹配不到资产，并把“找不到资产”误报成“已是最新”。
var flkTarget = upgradecli.Target{
	Owner: "Jy-EggRoll",
	Repo:  "flk",

	AssetPrefix: "flk",

	Platforms: map[string]map[string]bool{
		"windows": {"386": true, "amd64": true, "arm64": true},
		"linux":   {"386": true, "amd64": true, "arm": true, "arm64": true},
		"darwin":  {"amd64": true, "arm64": true},
		"freebsd": {"amd64": true, "arm64": true},
	},

	// 直连不稳定时供用户选择的备用下载代理前缀，与资产原始地址直接拼接。
	// 是否启用完全由用户在升级过程中的确认决定，程序不会静默换源——
	// 下载结果随后会被直接执行，用户必须清楚二进制来自哪个镜像
	ProxyPrefix: "https://gh-proxy.org/",

	// 请求标识用程序名，便于上游区分流量来源
	UserAgent: "flk-updater",
}
