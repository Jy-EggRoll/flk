//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// upgradeScriptDelay 是脚本等待当前进程退出的秒数
// Windows 无法替换正在运行的可执行文件（映像文件被内核锁定），只能等进程结束后由外部脚本完成，
// 该延时是给进程退出的窗口，取值过小会让替换赶在进程结束前执行而失败
const upgradeScriptDelay = 2

// replaceExecutable 通过延迟批处理脚本完成替换
//
// Windows 上正在运行的映像文件被锁定，既不能覆盖也不能删除，
// 因此必须把替换动作交给一个独立于当前进程的脚本，在进程退出后再执行。
// 脚本承担三件事：等待退出、替换、报告结果
//
// 与 Unix 路径的能力差异是平台固有的，不是实现取舍：
// Unix 靠内核允许 rename 覆盖运行中文件而完全不需要脚本
func replaceExecutable(staged, execPath string) error {
	dir := filepath.Dir(execPath)
	base := strings.TrimSuffix(filepath.Base(execPath), ".exe")
	scriptPath := filepath.Join(dir, base+"-upgrade.bat")

	// 升级失败时把新版本留在旁边而不是删除：用户至少能拿到下载好的文件并手动完成替换，
	// 若直接删除，用户会既失去新版本也不知道失败原因
	failureKeepPath := execPath + ".new"

	// 提示信息使用英文：批处理由 cmd 默认代码页解释，写入 UTF-8 中文会出现乱码，
	// 而脚本运行时机在父进程退出之后，无法再把结果交回程序自行渲染
	script := fmt.Sprintf(`@echo off
timeout /t %d /nobreak >nul
copy /Y "%s" "%s" >nul 2>&1
if errorlevel 1 (
  move /Y "%s" "%s" >nul 2>&1
  echo Upgrade failed: the running executable could not be replaced.
  echo The downloaded version was kept as "%s".
  echo Please close the program and replace it manually.
  del "%%~f0" >nul 2>&1
  exit /b 1
)
del "%s" >nul 2>&1
del "%%~f0" >nul 2>&1
`, upgradeScriptDelay, staged, execPath, staged, failureKeepPath, failureKeepPath, staged)

	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		// 脚本都没能创建，替换注定不会发生，把暂存文件一并清掉避免留下垃圾
		_ = os.Remove(staged)
		return fmt.Errorf("写入升级脚本失败（可能需要对 %s 的写权限）: %w", dir, err)
	}

	cmd := exec.Command("cmd", "/c", scriptPath)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		_ = os.Remove(staged)
		_ = os.Remove(scriptPath)
		return fmt.Errorf("启动升级脚本失败: %w", err)
	}

	// 脚本已成功交付，替换会在当前进程退出后发生；
	// 这里只做交付确认，不能等待脚本完成，否则会与"等待本进程退出"互相死锁
	return nil
}
