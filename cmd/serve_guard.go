package cmd

import (
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/jy-eggroll/eggokit/l10n"
)

// 本文件为「本机 WebUI 服务」提供统一的访问护栏
//
// 背景（改造前的事实）：serve 的 HTTP 服务既无鉴权，也无 Host 校验、Origin/Referer 校验、
// CSRF 防护与 body 大小限制，--host 还能设成 0.0.0.0 而不给任何提示
// 当时端点只接收 JSON，跨站请求难以伪造出有效载荷，风险还算可控；一旦 WebUI 具备「真正操作文件系统」的能力
// （建立链接 / 修复 / 解除链接），DNS rebinding + CSRF 就等于任意文件删除，因此护栏属于必须先落地的前提
//
// 三道防线的分工：
//  1. Host 校验：请求的 Host 必须落在显式白名单内——内置的回环地址、服务实际绑定的地址（用户显式绑定即表示认可），
//     以及用户逐条列出的地址（--allow-host 与设置文件的 allowHosts，两者是并集）。
//     浏览器发出的 Host 由 URL 决定，攻击者域名（DNS 解析到自己机器）
//     无法伪造成白名单里的条目，因此这条同时挡住了 DNS rebinding 与直接跨站访问
//     为什么是显式白名单而不是「绑定了哪个地址就允许哪个地址」：WebUI 能直接操作文件系统与清单，属于高危入口，
//     访问面应由用户逐条确认，而不是随绑定地址隐式扩张
//  2. Origin/Referer 校验（仅非 GET/HEAD）：浏览器在跨站写请求上会带上 Origin，据此判断请求是否与页面同源。
//     缺失这两个头说明客户端不是浏览器（curl、脚本、非浏览器工具），此时放行——它们本来就不受 CSRF 模型约束
//  3. body 大小上限：限制单次请求体，避免超大请求把内存吃满

// maxServeBodyBytes 是 WebUI 服务允许的最大请求体（4 MiB）
// 取值理由：真正的载荷是一份 flk-store.json，几千条记录也只有几十 KB，4 MiB 已留出两个数量级的余量；
// 同时它足够小，单个请求最多占用 4 MiB 内存，攻击者无法用超大 body 拖垮服务
const maxServeBodyBytes = 4 << 20

// loopbackHosts 是始终允许的 Host 主机名（不含端口）
// [::1] 与 ::1 是同一个 IPv6 回环地址的两种写法：浏览器在 Host 头里带方括号，而 net.SplitHostPort
// 会把方括号去掉，两种写法经 normalizeHost 后都会归一到 ::1，这里两种都列出来只为让「允许清单」自解释
var loopbackHosts = []string{"127.0.0.1", "localhost", "::1", "[::1]"}

// guard 为本机 WebUI 服务施加访问护栏；allowedHosts 是回环地址之外额外允许的 Host
//
// 参数 allowedHosts 由调用方组装，包含三部分：
//   - 服务实际绑定的地址：用户显式 --host 192.168.1.5 时，浏览器地址栏里输入的就是它，
//     不放行则用户自己都打不开页面
//   - 用户用 --allow-host 逐条列出的地址（本次运行的临时授权）
//   - 设置文件 allowHosts 字段列出的地址（长期生效的授权）
//     WebUI 属于高危入口（能直接操作文件系统），访问面应当由用户显式授权，而不是随绑定地址隐式扩张
//
// 传入 nil / 空切片时只保留回环地址，即「仅本机可访问」
func guard(h http.Handler, allowedHosts []string) http.Handler {
	allowed := buildAllowedHosts(allowedHosts)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 第 1 道防线：Host 校验
		requestHost := normalizeHost(r.Host)
		if !allowed[requestHost] {
			http.Error(w, l10n.T("Forbidden: the request host is not allowed", nil), http.StatusForbidden)
			return
		}

		// 读请求不改状态，不参与 CSRF 模型，因此 Origin/Referer 与 body 限制都只作用于写请求
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// 第 2 道防线：Origin / Referer 同源校验
			if !sameOriginRequest(r) {
				http.Error(w, l10n.T("Forbidden: the request does not come from the same origin", nil), http.StatusForbidden)
				return
			}

			// 第 3 道防线：body 大小上限
			// 声明了 Content-Length 且超限时直接 413，不必等读满才失败，响应更快也更省资源；
			// 未声明长度（分块传输，ContentLength 为 -1）的请求交给 MaxBytesReader 在读取过程中拦截，
			// 它会返回 *http.MaxBytesError 并让服务器关闭连接，两层覆盖所有客户端
			if r.ContentLength > maxServeBodyBytes {
				http.Error(w, l10n.T("Request body is too large", nil), http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxServeBodyBytes)
		}

		h.ServeHTTP(w, r)
	})
}

// buildAllowedHosts 把回环地址与调用方传入的绑定地址合并成允许集合，元素已归一化
// 空字符串（未指定 host、监听全部网卡）会被跳过：它不是一个可出现在 Host 头里的主机名
func buildAllowedHosts(allowedHosts []string) map[string]bool {
	allowed := make(map[string]bool, len(loopbackHosts)+len(allowedHosts))
	for _, host := range loopbackHosts {
		allowed[normalizeHost(host)] = true
	}
	for _, host := range allowedHosts {
		normalized := normalizeHost(host)
		if normalized == "" {
			continue
		}
		allowed[normalized] = true
	}
	return allowed
}

// allowedHostDisplay 返回当前生效白名单的可读展示列表（已归一化、去重、按字典序排序），供启动日志打印
//
// 刻意复用 buildAllowedHosts 而不是自己遍历一遍入参：打印结果与实际判定必须来自同一份实现，
// 否则「打印一套、放行另一套」的偏差会直接误导用户——这类问题在安全相关的白名单上尤其致命。
// 排序是为了让输出稳定可读：map 的遍历顺序随机，同一份配置每次启动打印的顺序都不同会让人误以为配置变了
func allowedHostDisplay(allowedHosts []string) []string {
	allowed := buildAllowedHosts(allowedHosts)
	hosts := make([]string, 0, len(allowed))
	for host := range allowed {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// normalizeHost 把 Host 头或 URL 里的主机部分归一成「小写、无端口、无 IPv6 方括号」的形式，便于比较
//
// 归一化的必要性：同一个回环地址在 Host 头里有多种合法写法（127.0.0.1:8999、[::1]:8999、[::1]、LOCALHOST），
// 逐字比较会把等价写法判成伪造，同时大写写法又可能绕过检查，因此两边（允许清单与请求）都走本函数
// 潜在影响点：Host 头缺失或为空时返回空串，而允许集合里不存在空串，所以空 Host 一律被拒
func normalizeHost(raw string) string {
	host := strings.TrimSpace(raw)
	if host == "" {
		return ""
	}
	// 带端口时 net.SplitHostPort 会顺手去掉 IPv6 的方括号（[::1]:8999 → ::1）
	// 不带端口时它返回错误（missing port），此时沿用原值，再由下面的 Trim 处理 [::1] 这种写法
	if withoutPort, _, err := net.SplitHostPort(host); err == nil {
		host = withoutPort
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

// isLoopbackHost 判断服务绑定的主机是否是回环地址，用于启动时的安全警告
//
// 只认 localhost 字面量与回环 IP，刻意不做 DNS 解析：解析结果依赖运行环境的 hosts 与 DNS，
// 启动阶段的判断不该引入网络查询，更不能因为解析失败而误判成安全
// 潜在影响点：返回 false 不一定代表绑定到了公网（例如自定义域名的 hosts 别名），属于「宁可多警告」的取向
func isLoopbackHost(host string) bool {
	normalized := normalizeHost(host)
	if normalized == "" {
		return false
	}
	if normalized == "localhost" {
		return true
	}
	if ip := net.ParseIP(normalized); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// sameOriginRequest 判断写请求是否来自同源页面
//
// 判定顺序与理由：
//  1. 有 Origin 就以它为准：浏览器对跨站写请求必定带上 Origin，且页面脚本无法伪造它
//  2. Origin 缺失但有 Referer 时用 Referer 兜底：少数老旧客户端不发 Origin，但会发 Referer
//  3. 两者都没有则放行：非浏览器客户端（curl / 脚本 / 自动化）不适用 CSRF 模型，
//     且它们本就不携带任何浏览器凭据，挡下来只会误伤本地工具
//
// 比较的是「主机 + 端口」（含 IPv6 归一），不含 scheme：
//   - 必须比端口：同源的定义是 scheme + host + port 三者相等。只比主机名会放行
//     http://127.0.0.1:1234 这类页面发起的写请求——本机上任意一个别的服务，
//     甚至一个临时打开的本地页面，都能借此操作 flk 的文件与清单。本服务没有鉴权，
//     这条比较是「WebUI 具备文件操作能力」之后唯一挡住 CSRF 的东西
//   - 不必比 scheme：本服务只提供明文 HTTP，不存在「同主机同端口的 https 页面」，
//     补上只会多出一处需要随 TLS 状态维护的判断
//
// 潜在影响点：Origin 为 "null"（沙箱 iframe、file:// 页面）时 url.Parse 得到空 Host，
// 与任何合法请求的 authority 都不相等，因此会被判为跨站而拒绝——这正是期望行为
func sameOriginRequest(r *http.Request) bool {
	requestAuthority := normalizeAuthority(r.Host)
	if origin := r.Header.Get("Origin"); origin != "" {
		return authorityOfURL(origin) == requestAuthority
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		return authorityOfURL(referer) == requestAuthority
	}
	return true
}

// normalizeAuthority 把 Host 头或 URL 的主机部分归一成「小写主机 + 端口」，供同源比较使用
//
// 与 normalizeHost 的唯一区别是端口：normalizeHost 刻意丢掉端口，因为白名单是按主机名授权的
// （用户不会因为换了个端口就重新授权一次）；而同源比较必须保留端口，否则本机另一个端口上的页面
// 会被判成与自己同源。两者不能合并成一份实现，这一点是刻意的
// 不带端口时退回 normalizeHost：裸 IPv6 写法（[::1]）会走这条路径，方括号在那里被去掉
func normalizeAuthority(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(trimmed)
	if err != nil {
		return normalizeHost(trimmed)
	}
	// JoinHostPort 会为 IPv6 补回方括号，因此两端（请求头与 URL）归一后的形态一致
	return net.JoinHostPort(normalizeHost(host), port)
}

// authorityOfURL 取出 URL 字符串里的「主机 + 端口」（已归一化），解析失败时返回空串
// 返回空串而不是原值：任何无法解析的输入都不应该碰巧等于某个合法请求的 authority 而被放行
func authorityOfURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return normalizeAuthority(parsed.Host)
}
