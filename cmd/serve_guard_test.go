package cmd

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// 本文件覆盖 cmd/serve_guard.go 的三道防线：Host 校验、Origin/Referer 同源校验、body 大小上限
// 约定：全部用 httptest 在内存里跑，不监听端口、不写磁盘、不触碰真实的 store 文件
// 每个用例都用「记录是否被放行」的方式断言，而不是看错误文案，避免文案调整导致用例连带失败

// guardTestHandler 返回一个把「是否被放行」记录到 hit 上的终端 handler
// 用 hit 而不是状态码判定：guard 拒绝时返回 403，放行时由本 handler 返回 200，两者都通过 recorder 观察
func guardTestHandler(hit *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hit = true
		w.WriteHeader(http.StatusOK)
	})
}

// TestGuardHostValidation 校验 Host 允许清单：回环地址与显式绑定的 host 放行，其余一律 403
// 防的回归：把「本机才能访问」这道防线放宽（例如允许任意 Host），DNS rebinding 就足以让外部页面拿到本机 WebUI 的控制权
func TestGuardHostValidation(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		allowed     []string
		wantStatus  int
		wantReached bool
	}{
		{name: "回环 IPv4 带端口放行", host: "127.0.0.1:8999", wantStatus: http.StatusOK, wantReached: true},
		{name: "localhost 带端口放行", host: "localhost:8999", wantStatus: http.StatusOK, wantReached: true},
		{name: "localhost 无端口放行", host: "localhost", wantStatus: http.StatusOK, wantReached: true},
		{name: "IPv6 回环带端口放行", host: "[::1]:8999", wantStatus: http.StatusOK, wantReached: true},
		{name: "IPv6 回环无端口（方括号形式）放行", host: "[::1]", wantStatus: http.StatusOK, wantReached: true},
		{name: "大写写法归一后放行", host: "LOCALHOST:8999", wantStatus: http.StatusOK, wantReached: true},
		{name: "显式绑定的 host 放行", host: "192.168.1.5:8999", allowed: []string{"192.168.1.5"}, wantStatus: http.StatusOK, wantReached: true},
		{name: "绑定通配地址时带 0.0.0.0 的 Host 放行", host: "0.0.0.0:8999", allowed: []string{"0.0.0.0"}, wantStatus: http.StatusOK, wantReached: true},
		{name: "伪造域名拒绝（DNS rebinding）", host: "evil.example:8999", wantStatus: http.StatusForbidden},
		{name: "未绑定的局域网 IP 拒绝", host: "192.168.1.5:8999", wantStatus: http.StatusForbidden},
		{name: "空 Host 拒绝", host: "", wantStatus: http.StatusForbidden},
		{name: "绑定清单为空串时不影响回环放行", host: "127.0.0.1:8999", allowed: []string{""}, wantStatus: http.StatusOK, wantReached: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			// httptest 对相对路径的 target 会默认填 example.com，必须显式覆盖成用例给定的 Host
			request.Host = tc.host

			guard(guardTestHandler(&reached), tc.allowed).ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d（响应体 %q）", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if reached != tc.wantReached {
				t.Fatalf("终端 handler 是否被调用 = %v，期望 %v", reached, tc.wantReached)
			}
		})
	}
}

// TestAllowedHostDisplay 校验启动日志里「当前生效白名单」的展示内容
//
// 为什么值得单测：白名单现在有三个来源（绑定地址 / --allow-host / 设置文件 allowHosts），
// 用户只能靠这行日志确认设置文件里的条目是否真的被读到；展示与实际判定必须来自同一份实现，
// 否则会出现「打印一套、实际放行另一套」的误导，这在安全相关的白名单上后果最重
// 断言口径：归一化（大小写、端口、IPv6 方括号）后去重，空串不出现，输出按字典序稳定排序
func TestAllowedHostDisplay(t *testing.T) {
	got := allowedHostDisplay([]string{"192.168.1.5", "LOCALHOST:8999", "", "   ", "[::1]:8999", "192.168.1.5:8999"})
	want := []string{"127.0.0.1", "192.168.1.5", "::1", "localhost"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("白名单展示 = %#v，期望 %#v", got, want)
	}
}

// TestGuardOriginValidation 校验写请求的同源判定：同源放行、跨站拒绝、两个头都缺失时放行
// 防的回归：跨站页面能直接把 JSON 写进本机 store（CSRF），一旦 WebUI 具备文件操作能力即为任意文件删除
func TestGuardOriginValidation(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		host        string
		origin      string
		referer     string
		wantStatus  int
		wantReached bool
	}{
		{
			name: "同源 Origin 放行", method: http.MethodPost, host: "127.0.0.1:8999",
			origin: "http://127.0.0.1:8999", wantStatus: http.StatusOK, wantReached: true,
		},
		{
			name: "同 host 不同端口放行（只比较主机名）", method: http.MethodPost, host: "127.0.0.1:8999",
			origin: "http://127.0.0.1:1234", wantStatus: http.StatusOK, wantReached: true,
		},
		{
			name: "无 Origin 有同源 Referer 放行", method: http.MethodPost, host: "localhost:8999",
			referer: "http://localhost:8999/index.html", wantStatus: http.StatusOK, wantReached: true,
		},
		{
			name: "无 Origin 无 Referer 放行（curl 等非浏览器客户端）", method: http.MethodPost, host: "127.0.0.1:8999",
			wantStatus: http.StatusOK, wantReached: true,
		},
		{
			name: "跨站 Origin 拒绝", method: http.MethodPost, host: "127.0.0.1:8999",
			origin: "http://evil.example", wantStatus: http.StatusForbidden,
		},
		{
			name: "Origin 为 null 拒绝", method: http.MethodPost, host: "127.0.0.1:8999",
			origin: "null", wantStatus: http.StatusForbidden,
		},
		{
			name: "跨站 Referer 拒绝", method: http.MethodPost, host: "127.0.0.1:8999",
			referer: "http://evil.example/page", wantStatus: http.StatusForbidden,
		},
		{
			name: "GET 不参与同源校验", method: http.MethodGet, host: "127.0.0.1:8999",
			origin: "http://evil.example", wantStatus: http.StatusOK, wantReached: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, "/api/config", nil)
			request.Host = tc.host
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				request.Header.Set("Referer", tc.referer)
			}

			guard(guardTestHandler(&reached), nil).ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d（响应体 %q）", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if reached != tc.wantReached {
				t.Fatalf("终端 handler 是否被调用 = %v，期望 %v", reached, tc.wantReached)
			}
		})
	}
}

// repeatedByteReader 无限重复同一字节，用于在不分配 4 MiB 缓冲的前提下构造超大请求体
type repeatedByteReader struct {
	remaining int64
	b         byte
}

func (r *repeatedByteReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = r.b
	}
	r.remaining -= n
	return int(n), nil
}

// TestGuardRequestBodyLimit 校验 body 上限：声明长度的超大请求被 413 拒绝，
// 未声明长度的超大请求在读取时被 MaxBytesReader 截断，正常大小的请求体原样透传
// 防的回归：移除上限后，单个请求就能吃掉任意内存
func TestGuardRequestBodyLimit(t *testing.T) {
	const host = "127.0.0.1:8999"

	t.Run("声明 Content-Length 超限返回 413", func(t *testing.T) {
		var reached bool
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/config", nil)
		request.Host = host
		// 只声明长度而不真的传输数据：guard 依据声明值直接拒绝，不必读满整个 body
		request.ContentLength = maxServeBodyBytes + 1
		request.Body = io.NopCloser(&repeatedByteReader{remaining: 1, b: 'x'})

		guard(guardTestHandler(&reached), nil).ServeHTTP(recorder, request)

		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("状态码 = %d，期望 %d", recorder.Code, http.StatusRequestEntityTooLarge)
		}
		if reached {
			t.Fatal("超限请求不应进入终端 handler")
		}
	})

	t.Run("未声明长度时由 MaxBytesReader 截断", func(t *testing.T) {
		// 终端 handler 真实消费 body，模拟 /api/config 的解码过程，把读取错误抛出来供断言
		var readErr error
		var reached bool
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			_, readErr = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		})

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/config", nil)
		request.Host = host
		// 分块传输的等价形态：长度未知（-1）但实际数据超过上限
		request.ContentLength = -1
		request.Body = io.NopCloser(&repeatedByteReader{remaining: maxServeBodyBytes + 1, b: 'x'})

		guard(next, nil).ServeHTTP(recorder, request)

		if !reached {
			t.Fatal("长度未知的请求应进入终端 handler，由 MaxBytesReader 在读取时拦截")
		}
		var maxBytesErr *http.MaxBytesError
		if !errors.As(readErr, &maxBytesErr) {
			t.Fatalf("读取错误 = %v，期望 *http.MaxBytesError", readErr)
		}
	})

	t.Run("正常大小请求体原样透传", func(t *testing.T) {
		const payload = `{"linux":{"dev":{"symlink":[{"fake":"~/f","real":"~/r"}]}}}`
		var got string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("读取 body 失败: %v", err)
				return
			}
			got = string(raw)
			w.WriteHeader(http.StatusOK)
		})

		recorder := httptest.NewRecorder()
		// 先传 nil body 再自行赋值：httptest 只对 *strings.Reader 等已知类型自动填 ContentLength，
		// 这里显式声明长度，模拟浏览器提交 JSON 的真实形态
		request := httptest.NewRequest(http.MethodPost, "/api/config", nil)
		request.Host = host
		request.Body = io.NopCloser(strings.NewReader(payload))
		request.ContentLength = int64(len(payload))

		guard(next, nil).ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200", recorder.Code)
		}
		if got != payload {
			t.Fatalf("透传的 body = %q，期望 %q", got, payload)
		}
	})
}

// TestIsLoopbackHost 校验绑定地址的回环判定，它决定启动时是否打印安全警告
// 防的回归：把 0.0.0.0 / :: / 局域网 IP 误判成回环，用户把服务暴露到网络上却完全不知情
func TestIsLoopbackHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{host: "127.0.0.1", want: true},
		{host: "localhost", want: true},
		{host: "LOCALHOST", want: true},
		{host: "::1", want: true},
		{host: "[::1]", want: true},
		{host: "127.0.0.1:8999", want: true},
		{host: "0.0.0.0", want: false},
		{host: "::", want: false},
		{host: "192.168.1.5", want: false},
		{host: "flk.local", want: false},
		{host: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			if got := isLoopbackHost(tc.host); got != tc.want {
				t.Fatalf("isLoopbackHost(%q) = %v，期望 %v", tc.host, got, tc.want)
			}
		})
	}
}
