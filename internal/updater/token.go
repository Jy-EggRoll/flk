package updater

import (
	"os"
	"os/exec"
	"strings"
	"sync"
)

// defaultTokenEnv 是默认读取令牌的环境变量名
const defaultTokenEnv = "GITHUB_TOKEN"

// EnvTokenProvider 构造一个按固定优先级查找访问令牌的函数，可直接赋给 Config.TokenProvider
// 查找顺序为环境变量、再退化到 gh 命令行的已登录凭据，使已安装 GitHub CLI 的用户无需额外配置；
// 两者都没有时返回空字符串，调用方按匿名访问继续，认证始终是可选项而不是前置条件
//
// 结果会被缓存：查找过程可能启动 gh 子进程，而一次升级流程中令牌会被查询多次，
// 每次重新启动子进程既慢又无意义
func EnvTokenProvider(envName string) func() string {
	if envName == "" {
		envName = defaultTokenEnv
	}

	var once sync.Once
	var token string

	return func() string {
		once.Do(func() {
			if fromEnv := os.Getenv(envName); fromEnv != "" {
				token = fromEnv
				return
			}
			token = ghAuthToken()
		})
		return token
	}
}

// ghAuthToken 读取 gh 命令行当前登录用户的令牌
// 仅在 gh 已安装且已完成登录时返回有效值，其余情况一律静默失败，
// 因为未安装 gh 是完全正常的用户环境，不该产生任何噪音
func ghAuthToken() string {
	output, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
