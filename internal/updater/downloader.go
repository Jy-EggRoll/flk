package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pterm/pterm"
)

const ghProxyPrefix = "https://gh-proxy.org/"

// directClient 用于直连下载，建连与响应头超时合计约 10 秒
// 直连报错或整体下载超过 10 秒时会询问用户是否改用代理，绝不静默切换下载源
var directClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 5 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
	},
}

// proxyClient 用于代理下载，代理网络可能更慢，给予更充裕的超时
var proxyClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 15 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
	},
}

// progressPaused 用于在交互式弹窗期间暂停进度条输出
// 防止 goroutine 中 \r 进度条刷掉 pterm 的提示文字
var progressPaused atomic.Bool

// DownloadAndReplace 下载当前平台的新二进制并启动自替换脚本
// output 为可选参数以兼容既有包内调用；未传入或传入 nil 时默认写 stderr，避免升级状态污染 stdout
func DownloadAndReplace(url, filename, destDir string, output ...io.Writer) error {
	writer := outputWriter(output)
	downloadPath := filepath.Join(destDir, filename)
	proxyURL := ghProxyPrefix + url

	token := getGitHubToken()
	if token != "" {
		pterm.Info.WithWriter(writer).Println("已启用 GitHub Token 认证，API 请求不受限速影响")
	} else {
		pterm.Info.WithWriter(writer).Println("未配置 GitHub Token，API 请求受 GitHub 限速影响，请考虑设置 GITHUB_TOKEN")
	}

	if err := downloadWithRetry(proxyURL, url, downloadPath, writer); err != nil {
		return err
	}

	execPath, err := os.Executable()
	if err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		return replaceWindows(downloadPath, execPath, writer)
	}
	return replaceUnix(downloadPath, execPath, writer)
}

// outputWriter 统一解析下载器内部的可选输出目标
// 只使用第一个 writer，既保持旧调用无需参数，也让命令层可以显式注入 Cobra 的 stderr
func outputWriter(output []io.Writer) io.Writer {
	if len(output) > 0 && output[0] != nil {
		return output[0]
	}
	return os.Stderr
}

// downloadWithRetry 先尝试直连下载，10 秒后如果仍未完成，询问用户是否切换到代理
// 无论直连是报错还是超时，都交由用户决定是否切代理，绝不自动回退；output 未提供时默认 stderr
func downloadWithRetry(proxyURL, originalURL, destPath string, output ...io.Writer) error {
	writer := outputWriter(output)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)

	pterm.Info.WithWriter(writer).Println("下载模式：直连下载（10 秒无响应将询问是否切换代理）")
	go func() {
		errCh <- download(ctx, originalURL, destPath, directClient, writer)
	}()

	timer := time.After(10 * time.Second)

	select {
	case err := <-errCh:
		if err != nil {
			return askSwitchProxyDownload(ctx, proxyURL, destPath, err, writer)
		}
		return nil

	case <-timer:
		// 暂停进度条输出，防止与 pterm 弹窗内容交叠；交互提示沿用根层设置的 pterm 默认 stderr
		progressPaused.Store(true)
		useProxy, promptErr := pterm.DefaultInteractiveConfirm.
			WithDefaultValue(true).
			Show("检测到当前下载速度缓慢，是否切换到代理下载模式？")
		progressPaused.Store(false)

		if promptErr != nil || !useProxy {
			pterm.Info.WithWriter(writer).Println("继续等待直连下载完成...")
			err := <-errCh
			if err != nil {
				return askSwitchProxyDownload(context.Background(), proxyURL, destPath, err, writer)
			}
			return nil
		}

		pterm.Info.WithWriter(writer).Println("用户选择切换代理，下载模式：gh-proxy 代理...")
		cancel()
		<-errCh
		return download(context.Background(), proxyURL, destPath, proxyClient, writer)
	}
}

// askSwitchProxyDownload 弹窗询问用户是否切代理，用户同意则走代理，拒绝则返回原错误
// output 只承载非交互状态；确认组件继续使用根生命周期配置的 pterm 默认 stderr
func askSwitchProxyDownload(ctx context.Context, proxyURL, destPath string, origErr error, output ...io.Writer) error {
	writer := outputWriter(output)
	progressPaused.Store(true)
	useProxy, promptErr := pterm.DefaultInteractiveConfirm.
		WithDefaultValue(true).
		Show("直连下载失败，是否切换到代理下载模式？")
	progressPaused.Store(false)

	if promptErr != nil || !useProxy {
		return origErr
	}

	pterm.Info.WithWriter(writer).Println("用户选择切换代理，下载模式：gh-proxy 代理...")
	return download(context.Background(), proxyURL, destPath, proxyClient, writer)
}

// download 把单次 HTTP 下载写入同目录临时文件，完整校验后再原子重命名到目标路径
// 状态与逐块进度始终写入注入 writer，未注入时由 outputWriter 回退到 stderr
func download(ctx context.Context, url, destPath string, client *http.Client, output ...io.Writer) error {
	writer := outputWriter(output)
	pterm.Info.WithWriter(writer).Printfln("正在下载: %s", url)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "flk-updater")

	startTime := time.Now()

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败，状态码: %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(destPath), "flk-*")
	if err != nil {
		return err
	}
	defer tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	downloaded := int64(0)
	buf := make([]byte, 32*1024)
	total := resp.ContentLength

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("下载已取消: %w", ctx.Err())
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := tmpFile.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("写入临时文件失败: %w", writeErr)
			}
			downloaded += int64(n)
			if total > 0 && !progressPaused.Load() {
				_, _ = fmt.Fprintf(writer, "\r下载进度: %.1f%%", float64(downloaded)/float64(total)*100)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("读取下载数据失败: %w", readErr)
		}
	}

	elapsed := time.Since(startTime)
	_, _ = fmt.Fprintln(writer)

	if total > 0 && downloaded != total {
		return fmt.Errorf("下载不完整: 已下载 %d 字节，期望 %d 字节", downloaded, total)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	if err := os.Rename(tmpFile.Name(), destPath); err != nil {
		return err
	}
	os.Chmod(destPath, 0755)

	pterm.Info.WithWriter(writer).Printfln("下载完成: %s（%s，耗时 %s，平均速率 %s）", destPath,
		formatSize(downloaded), formatDuration(elapsed), formatSpeed(float64(downloaded)/elapsed.Seconds()))
	return nil
}

func formatSize(bytes int64) string {
	if bytes > 1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
	if bytes > 1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%d B", bytes)
}

func formatDuration(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func formatSpeed(bytesPerSec float64) string {
	if bytesPerSec > 1024*1024 {
		return fmt.Sprintf("%.1f MB/s", bytesPerSec/(1024*1024))
	}
	if bytesPerSec > 1024 {
		return fmt.Sprintf("%.1f KB/s", bytesPerSec/1024)
	}
	return fmt.Sprintf("%.0f B/s", bytesPerSec)
}

// replaceWindows 写入批处理脚本并异步启动，等待当前进程由 main 的统一退出边界自然结束后再替换二进制
// output 未提供时默认 stderr；脚本启动失败必须返回，不能在更新器内部终止整个进程
func replaceWindows(newPath, execPath string, output ...io.Writer) error {
	writer := outputWriter(output)
	dir := filepath.Dir(execPath)
	base := strings.TrimSuffix(filepath.Base(execPath), ".exe")
	batPath := filepath.Join(dir, base+"-upgrade.bat")

	bat := fmt.Sprintf(`@echo off
timeout /t 1 /nobreak >nul
copy /Y "%s" "%s" >nul
del "%s"
start "" "%s"
del "%%~f0"
`, newPath, execPath, newPath, execPath)

	if err := os.WriteFile(batPath, []byte(bat), 0644); err != nil {
		os.Remove(newPath)
		return err
	}

	pterm.Info.WithWriter(writer).Println("正在启动升级程序...")

	cmd := exec.Command("cmd", "/c", batPath)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 Windows 升级脚本失败: %w", err)
	}
	return nil
}

// replaceUnix 写入 Shell 脚本并异步启动，等待当前进程由 main 的统一退出边界自然结束后再替换二进制
// output 未提供时默认 stderr；成功启动只表示替换任务已交付，函数应自然返回而不是调用 os.Exit
func replaceUnix(newPath, execPath string, output ...io.Writer) error {
	writer := outputWriter(output)
	dir := filepath.Dir(execPath)
	scriptPath := filepath.Join(dir, ".flk-upgrade.sh")

	script := fmt.Sprintf(`#!/bin/bash
sleep 0.5
cp "%s" "%s"
rm "%s"
"%s"
rm "$0"
`, newPath, execPath, newPath, execPath)

	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		os.Remove(newPath)
		return err
	}

	pterm.Info.WithWriter(writer).Println("正在启动升级程序...")

	cmd := exec.Command(scriptPath)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 Unix 升级脚本失败: %w", err)
	}
	return nil
}
