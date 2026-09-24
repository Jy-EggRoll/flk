// Package updater 提供基于 GitHub Release 的自升级能力
//
// 该包刻意不包含任何与具体宿主程序绑定的取值：仓库坐标、资产命名、请求标识、
// 终端界面乃至可执行文件定位方式全部由 Config 注入，因此可以被原样复用到其他项目，
// 宿主侧只需提供"怎么找到我的发布产物"和"怎么和我的用户说话"这两件事
package updater

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/jy-eggroll/flk/pkg/l10n"
	"path/filepath"
	"strings"
	"time"
)

// Channel 指定在哪个发布通道中查找更新
// 用独立类型而不是布尔开关，是为了让调用点自解释，也便于未来扩展更多通道
type Channel int

const (
	// ChannelStable 只考虑正式版标签，开发版永远不会被投递给正式版用户
	ChannelStable Channel = iota

	// ChannelDev 只考虑开发版标签，供希望提前验证的用户主动选择
	ChannelDev
)

// 默认参数：仅在 Config 未显式提供时生效，集中在此便于审查升级器的固有行为
const (
	// defaultAPIEndpoint 是 GitHub 公共 API 根地址
	defaultAPIEndpoint = "https://api.github.com"

	// defaultSlowThreshold 是直连下载多久仍未完成时询问用户是否切换代理下载
	defaultSlowThreshold = 10 * time.Second

	// defaultUserAgent 在宿主未指定时作为请求标识
	defaultUserAgent = "self-updater"
)

// Config 是升级器的全部外部依赖
// 除必填项外的所有字段都有合理默认值，宿主可以只填写仓库坐标、资产命名与界面三项就跑通整个流程
type Config struct {
	// Owner 与 Repo 指向承载 Release 的仓库，必填
	Owner string
	Repo  string

	// AssetName 返回指定平台对应资产的文件名前缀，以及宿主是否为该平台发布产物，必填
	// 它是升级器唯一判断"当前平台有哪些可下载产物"的依据，
	// ok 为 false 时检查更新会返回明确错误，而不是因为找不到资产而误报"已是最新"
	AssetName func(goos, goarch string) (prefix string, ok bool)

	// Reporter 承接全部用户可见输出与确认，必填
	Reporter Reporter

	// APIEndpoint 是 API 根地址，用于对接 GitHub Enterprise 等自建实例
	// 为空时使用 GitHub 公共 API；结尾多余的斜杠会被自动去除
	APIEndpoint string

	// TokenProvider 返回可选的认证令牌，用于突破 API 的匿名调用限速
	// 为 nil 表示始终匿名访问；可直接使用 EnvTokenProvider 构造
	TokenProvider func() string

	// ProxyPrefix 是可选的备用下载代理前缀，与资产原始下载地址直接拼接
	// 为空表示只使用直连，不做任何镜像回退；
	// 该字段存在是因为部分网络环境下直连发布资产极不稳定，而是否换源必须由用户裁决
	ProxyPrefix string

	// SlowThreshold 是直连下载多久仍未完成时询问用户是否切换代理
	// 仅在 ProxyPrefix 非空时生效，小于等于 0 时使用默认值
	SlowThreshold time.Duration

	// ExecutablePath 返回待替换的可执行文件路径，为空时使用 os.Executable
	// 暴露为函数是为了让测试可以指向临时目录中的假二进制，而不必真的替换测试进程自身
	ExecutablePath func() (string, error)

	// HTTPClient 用于查询 Release 元信息，其超时应短于用户的等待预期
	// 为空时使用带总超时的默认客户端；下载资产不使用它，
	// 因为下载需要按直连与代理分别采取不同的超时策略，这属于升级器的固有行为而非外部可调项
	HTTPClient *http.Client

	// UserAgent 是请求标识，建议设为宿主程序名以便上游识别流量来源
	UserAgent string
}

// Updater 是升级器的执行入口
// 构造后所有方法都只读取配置，因此可以安全地在多个 goroutine 中并发调用
type Updater struct {
	cfg Config
}

// New 校验配置并构造升级器
// 必填项缺失属于宿主的接线错误，必须在构造阶段立即失败，
// 而不是等到用户执行升级、下载到一半才暴露
func New(cfg Config) (*Updater, error) {
	if cfg.Owner == "" || cfg.Repo == "" {
		return nil, errors.New(l10n.T("Both Owner and Repo must be configured", nil))
	}
	if cfg.AssetName == nil {
		return nil, errors.New(l10n.T("AssetName must be configured to locate the release artifact for the current platform", nil))
	}
	if cfg.Reporter == nil {
		return nil, errors.New(l10n.T("A Reporter must be configured (use DiscardReporter for unattended scenarios)", nil))
	}

	if cfg.APIEndpoint == "" {
		cfg.APIEndpoint = defaultAPIEndpoint
	}
	cfg.APIEndpoint = strings.TrimRight(cfg.APIEndpoint, "/")

	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	if cfg.SlowThreshold <= 0 {
		cfg.SlowThreshold = defaultSlowThreshold
	}
	if cfg.HTTPClient == nil {
		// http.DefaultClient 没有任何超时，服务端不响应时请求会永久挂起，这里必须显式兜底
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.ExecutablePath == nil {
		cfg.ExecutablePath = os.Executable
	}

	return &Updater{cfg: cfg}, nil
}

// Apply 下载目标版本并替换当前可执行文件
// 流程刻意分成"先在安装目录下载"与"再原子替换"两步：
// 只有完整接收并校验过字节数之后才会触碰真正的可执行文件，
// 从而保证下载中断、磁盘写满等失败都不会让用户失去一个可运行的旧版本
func (u *Updater) Apply(info *UpdateInfo) error {
	if info == nil {
		return errors.New(l10n.T("The upgrade target is empty", nil))
	}

	execPath, err := u.cfg.ExecutablePath()
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to locate the current executable", nil), err)
	}

	// 暂存文件必须与安装目标同目录：替换的最后一步是原子重命名，
	// 跨文件系统时重命名会退化成复制，既不原子，也可能在覆盖运行中的文件时直接失败
	staged, err := u.fetch(info, filepath.Dir(execPath))
	if err != nil {
		return err
	}

	if err := replaceExecutable(staged, execPath); err != nil {
		// 替换失败时不能让暂存文件留在安装目录，否则每次失败都会累积一个无用的大文件
		_ = os.Remove(staged)
		return err
	}
	return nil
}
