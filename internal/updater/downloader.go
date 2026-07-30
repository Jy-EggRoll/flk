package updater

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
)

const ghProxyPrefix = "https://gh-proxy.org/"

func DownloadAndReplace(url, filename, destDir string) error {
	downloadPath := filepath.Join(destDir, filename)

	proxyURL := ghProxyPrefix + url
	if err := downloadWithRetry(proxyURL, url, downloadPath); err != nil {
		return err
	}

	execPath, err := os.Executable()
	if err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		return replaceWindows(downloadPath, execPath)
	}
	return replaceUnix(downloadPath, execPath)
}

func downloadWithRetry(proxyURL, originalURL, destPath string) error {
	logger.Info("正在尝试代理下载...")
	if err := download(proxyURL, destPath); err != nil {
		logger.Info("代理下载失败，尝试原始链接...")
		if err := download(originalURL, destPath); err != nil {
			return err
		}
	}
	return nil
}

func download(url, destPath string) error {
	logger.Info("正在下载: %s", url)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "flk-updater")

	// 使用带超时的 http.Client，避免服务端不响应时永久挂起（http.DefaultClient 无超时）
	resp, err := downloadClient.Do(req)
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
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			// 之前未检查写入错误，磁盘满或临时文件异常时会静默丢数据，最终安装损坏的二进制
			if _, writeErr := tmpFile.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("写入临时文件失败: %w", writeErr)
			}
			downloaded += int64(n)
			if total > 0 {
				fmt.Printf("\r下载进度: %.1f%%", float64(downloaded)/float64(total)*100)
			}
		}
		if readErr != nil {
			// 之前对任何 readErr 都直接 break 当作成功，会把网络中断当成下载完成
			// 现在只有 io.EOF 才是正常结束，其余错误一律向上返回
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("读取下载数据失败: %w", readErr)
		}
	}

	fmt.Println()

	// 若服务端声明了 Content-Length，校验实际下载字节数，防止安装被截断的文件
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

	logger.Info("下载完成: %s", destPath)
	return nil
}

func replaceWindows(newPath, execPath string) error {
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

	logger.Info("正在启动升级程序...")

	cmd := exec.Command("cmd", "/c", batPath)
	cmd.Dir = dir
	cmd.Start()

	os.Exit(0)
	return nil
}

func replaceUnix(newPath, execPath string) error {
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

	logger.Info("正在启动升级程序...")

	cmd := exec.Command(scriptPath)
	cmd.Dir = dir
	cmd.Start()

	os.Exit(0)
	return nil
}
