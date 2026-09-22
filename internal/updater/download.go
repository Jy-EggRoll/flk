package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// downloadChunkSize 是单次读取的字节数，32KB 兼顾吞吐与进度刷新频率
const downloadChunkSize = 32 * 1024

// 直连与代理使用两套超时策略
// 直连要求快速失败，以便尽早把"是否切换代理"的决定权交还给用户；
// 代理链路本身更慢，若沿用直连的短超时会导致正常但缓慢的下载被反复判定为失败
var (
	directDownloadClient = &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: 5 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
		},
	}

	proxyDownloadClient = &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 15 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
		},
	}
)

// transferResult 描述一次成功传输的规模与耗时
// 汇总信息不在这里输出：它必须等到进度展示结束之后才能打印，
// 否则文字会紧接在进度行末尾，与进度条挤在同一行
type transferResult struct {
	path    string
	bytes   int64
	elapsed time.Duration
}

// transferOutcome 在直连 goroutine 与等待结果的主流程之间传递一次尝试的成败
type transferOutcome struct {
	result transferResult
	err    error
}

// fetch 把目标资产下载到 dir 下的暂存文件并返回其路径
// 返回暂存文件而非最终路径，是为了让调用方在确认完整接收之后再执行替换，
// 使下载过程中的任何失败都不会触碰现有的可执行文件
func (u *Updater) fetch(info *UpdateInfo, dir string) (string, error) {
	result, err := u.transfer(info, dir)
	if err != nil {
		return "", err
	}

	// 速率按整体平均计算而不是瞬时值：慢速网络下瞬时速率抖动剧烈，
	// 换算出的数字会让用户误以为下载速度在反复变化
	speed := float64(0)
	if result.elapsed > 0 {
		speed = float64(result.bytes) / result.elapsed.Seconds()
	}
	u.cfg.Reporter.Info("下载完成：%s，耗时 %s，平均速率 %s",
		FormatSize(result.bytes), FormatDuration(result.elapsed), FormatSpeed(speed))

	return result.path, nil
}

// transfer 按配置选择下载路径
// 未配置代理时不存在换源选项，直连就是唯一路径，失败即失败
func (u *Updater) transfer(info *UpdateInfo, dir string) (transferResult, error) {
	if u.cfg.ProxyPrefix == "" {
		return u.download(context.Background(), info.DownloadURL, dir, directDownloadClient)
	}
	return u.downloadWithProxyFallback(info.DownloadURL, dir)
}

// downloadWithProxyFallback 先直连下载，慢于阈值或直接失败时询问用户是否改用代理
// 无论超时还是报错都交由用户裁决，绝不静默换源：
// 自动回退会让用户失去对"二进制究竟来自哪个镜像"的判断，而下载结果随后会被直接执行
func (u *Updater) downloadWithProxyFallback(url, dir string) (transferResult, error) {
	u.cfg.Reporter.Info("下载模式：直连（%s 内未完成将询问是否切换代理）", u.cfg.SlowThreshold)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 缓冲为 1：被取消的直连 goroutine 也必须能把结果写出去，否则它会永久阻塞在发送上
	results := make(chan transferOutcome, 1)

	go func() {
		result, err := u.download(ctx, url, dir, directDownloadClient)
		results <- transferOutcome{result: result, err: err}
	}()

	select {
	case outcome := <-results:
		if outcome.err == nil {
			return outcome.result, nil
		}
		return u.switchToProxy(url, dir, outcome.err)

	case <-time.After(u.cfg.SlowThreshold):
		confirmed, confirmErr := u.cfg.Reporter.Confirm("直连下载缓慢，是否切换到代理下载？")
		if confirmErr != nil {
			// 询问失败时保持直连，等价于用户选择继续等待；
			// 但必须说明询问本身失败的原因，否则用户无从理解为什么没有走代理方案
			u.cfg.Reporter.Info("无法征求是否切换代理的意见（%v），继续使用直连下载", confirmErr)
			confirmed = false
		}
		if !confirmed {
			// 直连仍是首选路径，不能因为一次询问未获同意就丢弃正在进行的下载
			u.cfg.Reporter.Info("继续等待直连下载完成...")
			outcome := <-results
			if outcome.err == nil {
				return outcome.result, nil
			}
			return transferResult{}, outcome.err
		}

		// 切换前必须等直连彻底退出：否则两路下载会同时刷新同一个进度，暂存文件也会互相干扰
		u.cfg.Reporter.Info("已切换到代理下载")
		cancel()
		<-results
		return u.download(context.Background(), u.cfg.ProxyPrefix+url, dir, proxyDownloadClient)
	}
}

// switchToProxy 在直连失败后询问用户是否改用代理
// 用户拒绝或询问失败时返回直连的原始错误而非询问错误：
// 用户真正需要知道的是下载为什么失败，而不是弹窗本身出了什么问题
func (u *Updater) switchToProxy(url, dir string, directErr error) (transferResult, error) {
	confirmed, confirmErr := u.cfg.Reporter.Confirm("直连下载失败，是否切换到代理下载？")
	if confirmErr != nil {
		// 必须说明询问失败的原因：否则用户只会看到一条网络错误，
		// 完全不知道程序本来准备了代理方案，也就失去了自行重试的线索
		u.cfg.Reporter.Info("无法征求是否切换代理的意见（%v）", confirmErr)
		return transferResult{}, directErr
	}
	if !confirmed {
		return transferResult{}, directErr
	}

	u.cfg.Reporter.Info("已切换到代理下载")
	return u.download(context.Background(), u.cfg.ProxyPrefix+url, dir, proxyDownloadClient)
}

// download 执行一次完整下载，成功时返回暂存文件路径与本次传输的规模耗时
// 汇总信息刻意不在这里输出：它必须等进度展示结束之后才能打印，详见 fetch
// 任何失败路径都会删除暂存文件，保证不会在安装目录里留下半截内容；
// 下载地址来自 Release 接口响应或宿主配置的代理前缀，属于既定信任边界，不在此做额外目标校验
func (u *Updater) download(ctx context.Context, url, dir string, client *http.Client) (transferResult, error) {
	// 进度展示的生命周期与单次传输严格绑定：从直连切换到代理属于两次独立传输，
	// 各自重新计量才能算出正确的速率与剩余时间，
	// 否则会把两段传输的字节数与跨越两段的总耗时混在一起，得出毫无意义的数字
	progress := u.cfg.Reporter.Progress("下载进度")
	defer progress.Done()

	u.cfg.Reporter.Info("开始下载: %s", url)
	started := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return transferResult{}, fmt.Errorf("构造下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", u.cfg.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return transferResult{}, fmt.Errorf("请求下载地址失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return transferResult{}, fmt.Errorf("下载失败，状态码: %d", resp.StatusCode)
	}

	// 暂存文件名由 CreateTemp 随机生成而不采用上游返回的资产名：
	// 资产名来自外部响应，直接当作文件名会把路径穿越风险引入安装目录
	staged, err := os.CreateTemp(dir, ".upgrade-*")
	if err != nil {
		return transferResult{}, fmt.Errorf("在安装目录创建暂存文件失败（可能需要对 %s 的写权限）: %w", dir, err)
	}

	// 失败清理集中在此处，避免每个返回点都要手写一遍关闭与删除；
	// 成功时保留暂存文件交给替换步骤，重复的 Close 只会返回 ErrClosed，可以安全忽略
	succeeded := false
	defer func() {
		_ = staged.Close()
		if !succeeded {
			_ = os.Remove(staged.Name())
		}
	}()

	total := resp.ContentLength
	written := int64(0)
	buffer := make([]byte, downloadChunkSize)

	for {
		select {
		case <-ctx.Done():
			return transferResult{}, fmt.Errorf("下载已取消: %w", ctx.Err())
		default:
		}

		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := staged.Write(buffer[:n]); writeErr != nil {
				return transferResult{}, fmt.Errorf("写入暂存文件失败: %w", writeErr)
			}
			written += int64(n)
			progress.Update(written, total)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return transferResult{}, fmt.Errorf("读取下载数据失败: %w", readErr)
		}
	}

	if err := staged.Close(); err != nil {
		return transferResult{}, fmt.Errorf("关闭暂存文件失败: %w", err)
	}

	// Content-Length 是弱校验：分块传输时它缺失，只能接受已接收的字节；
	// 一旦存在长度信息而实际字节数不符，必然意味着连接中途断开，此时绝不能把不完整内容当作新版本安装
	if total > 0 && written != total {
		return transferResult{}, fmt.Errorf("下载不完整: 已接收 %d 字节，期望 %d 字节", written, total)
	}

	if err := os.Chmod(staged.Name(), 0o755); err != nil {
		return transferResult{}, fmt.Errorf("设置可执行权限失败: %w", err)
	}

	succeeded = true
	return transferResult{path: staged.Name(), bytes: written, elapsed: time.Since(started)}, nil
}

// FormatSize 以人类可读形式表示字节数，进位使用 1024 而非 1000 以贴近文件管理器显示
// 导出是因为展示层的进度条需要同一套刻度，重复实现会让两处显示出现不一致的进位口径
func FormatSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// FormatDuration 输出秒级或分秒级时长，分钟以上才拆出分钟段，避免出现无意义的 0m 前缀
// 该函数同时用于传输耗时与界面上的剩余时间展示
func FormatDuration(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// FormatSpeed 输出平均下载速率，保留一位小数以便观察带宽量级
func FormatSpeed(bytesPerSecond float64) string {
	switch {
	case bytesPerSecond >= 1024*1024:
		return fmt.Sprintf("%.1f MB/s", bytesPerSecond/(1024*1024))
	case bytesPerSecond >= 1024:
		return fmt.Sprintf("%.1f KB/s", bytesPerSecond/1024)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSecond)
	}
}
