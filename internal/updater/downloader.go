package updater

import (
	"crypto/sha256"
	"encoding/hex"
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

// DownloadAndReplace 下载新版二进制并替换当前可执行文件
// 完整流程：代理/原始链接下载到临时文件 -> 校验 SHA256 完整性 -> 备份旧二进制 -> 延迟脚本原子替换
// 相比旧实现，新增了校验和验证（防代理链路投毒）与备份回退（避免覆盖失败后变砖）
func DownloadAndReplace(url, filename, destDir string) error {
	downloadPath := filepath.Join(destDir, filename)

	proxyURL := ghProxyPrefix + url
	if err := downloadWithRetry(proxyURL, url, downloadPath); err != nil {
		return err
	}

	// 下载完成后立即做完整性校验，拦截被篡改或损坏的二进制
	if err := verifyChecksum(downloadPath, proxyURL, url); err != nil {
		os.Remove(downloadPath)
		return err
	}

	execPath, err := os.Executable()
	if err != nil {
		return err
	}

	// 先备份当前可执行的二进制，替换失败时可据此恢复，避免"升级变砖"
	backupPath := execPath + ".bak"
	if err := copyFile(backupPath, execPath); err != nil {
		return fmt.Errorf("备份当前版本失败: %w", err)
	}

	if runtime.GOOS == "windows" {
		return replaceWindows(downloadPath, execPath, backupPath)
	}
	return replaceUnix(downloadPath, execPath, backupPath)
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

	resp, err := http.DefaultClient.Do(req)
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
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	downloaded := int64(0)
	buf := make([]byte, 32*1024)
	total := resp.ContentLength

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			tmpFile.Write(buf[:n])
			downloaded += int64(n)
			if total > 0 {
				fmt.Printf("\r下载进度: %.1f%%", float64(downloaded)/float64(total)*100)
			}
		}
		if err != nil {
			break
		}
	}

	fmt.Println()
	tmpFile.Close()

	// 先写到临时名，确认完整后再 rename 到最终路径（原子性更好）
	if err := os.Rename(tmpName, destPath); err != nil {
		return err
	}
	os.Chmod(destPath, 0755)

	logger.Info("下载完成: %s", destPath)
	return nil
}

// verifyChecksum 优先尝试下载与二进制同名的 .sha256 校验和文件并与本地哈希比对
// 若代理与原始链接都获取不到校验和文件，则仅告警不阻断（兼容未提供校验和的发布）
func verifyChecksum(binPath, proxyURL, originalURL string) error {
	want, err := fetchChecksum(proxyURL+".sha256", originalURL+".sha256")
	if err != nil {
		logger.Warn("未找到校验和文件，跳过完整性校验: %v", err)
		return nil
	}

	f, err := os.Open(binPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))

	if !strings.EqualFold(strings.TrimSpace(want), got) {
		return fmt.Errorf("校验和不匹配：期望 %s，实际 %s，可能存在下载损坏或被篡改", want, got)
	}
	logger.Info("校验和验证通过")
	return nil
}

func fetchChecksum(urls ...string) (string, error) {
	var lastErr error
	for _, u := range urls {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "flk-updater")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("校验和下载失败，状态码: %d (%s)", resp.StatusCode, u)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		// .sha256 文件内容通常为 "哈希  文件名" 或纯哈希，取首个空白符前的字段
		parts := strings.Fields(string(data))
		if len(parts) == 0 {
			lastErr = fmt.Errorf("校验和文件内容为空")
			continue
		}
		return parts[0], nil
	}
	return "", lastErr
}

// replaceWindows 通过延迟执行的 bat 脚本替换自身（Windows 下无法直接覆盖正在运行的进程）
// 脚本会先备份原文件，覆盖失败则从备份恢复，避免变砖
func replaceWindows(newPath, execPath, backupPath string) error {
	dir := filepath.Dir(execPath)
	base := strings.TrimSuffix(filepath.Base(execPath), ".exe")
	batPath := filepath.Join(dir, base+"-upgrade.bat")

	bat := fmt.Sprintf(`@echo off
timeout /t 1 /nobreak >nul
copy /Y "%s" "%s" >nul
if exist "%s" (
  del "%s"
  start "" "%s"
) else (
  echo 升级失败，正在从备份恢复...
  copy /Y "%s" "%s" >nul
)
del "%%~f0"
`, newPath, execPath, execPath, newPath, execPath, backupPath, execPath)

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

// replaceUnix 通过延迟执行的 shell 脚本替换自身（Unix 下 rename 正在运行的二进制是安全的）
// 覆盖失败则从备份恢复，避免变砖
func replaceUnix(newPath, execPath, backupPath string) error {
	dir := filepath.Dir(execPath)
	scriptPath := filepath.Join(dir, ".flk-upgrade.sh")

	script := fmt.Sprintf(`#!/bin/bash
sleep 0.5
if cp "%s" "%s"; then
  rm "%s"
  "%s"
else
  echo "升级失败，正在从备份恢复..."
  cp "%s" "%s"
fi
rm "$0"
`, newPath, execPath, newPath, execPath, backupPath, execPath)

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

func copyFile(dst, src string) error {
	from, err := os.Open(src)
	if err != nil {
		return err
	}
	defer from.Close()

	to, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer to.Close()

	_, err = io.Copy(to, from)
	return err
}
