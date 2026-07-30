package updater

import (
	"os"
	"os/exec"
	"strings"
)

// getGitHubToken 获取当前可用的 GitHub 认证 Token，用于 API 请求规避限速
// 优先级：GITHUB_TOKEN 环境变量 > gh auth token CLI
// 未找到可用 Token 时返回空字符串，调用方无需认证即可继续
func getGitHubToken() string {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}

	return getGHAuthToken()
}

// getGHAuthToken 调用 gh CLI 获取当前登录用户的 Token
// 仅在 gh 已安装且已登录时返回有效的 Token
func getGHAuthToken() string {
	cmd := exec.Command("gh", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
