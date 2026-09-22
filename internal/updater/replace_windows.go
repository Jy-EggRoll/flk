//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// replaceExecutable 通过延迟批处理脚本完成替换
//
// Windows 上正在运行的映像文件被内核锁定，既不能覆盖也不能删除，
// 因此必须把替换动作交给一个独立于当前进程的脚本，在进程退出后再执行；
// 脚本承担等待、替换与失败保留三件事，具体内容见 planWindowsUpgrade
//
// 与 Unix 路径的能力差异是平台固有的，不是实现取舍：
// Unix 靠内核允许重命名覆盖运行中的文件，因此完全不需要脚本
func replaceExecutable(staged, execPath string) error {
	plan := planWindowsUpgrade(staged, execPath)
	dir := filepath.Dir(execPath)

	if err := os.WriteFile(plan.ScriptPath, []byte(plan.Script), 0o644); err != nil {
		// 脚本都没能创建，替换注定不会发生，把暂存文件一并清掉避免留下垃圾
		_ = os.Remove(staged)
		return fmt.Errorf("写入升级脚本失败（可能需要对 %s 的写权限）: %w", dir, err)
	}

	cmd := exec.Command("cmd", "/c", plan.ScriptPath)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		_ = os.Remove(staged)
		_ = os.Remove(plan.ScriptPath)
		return fmt.Errorf("启动升级脚本失败: %w", err)
	}

	// 脚本已成功交付，替换会在当前进程退出后发生；
	// 这里只做交付确认，不能等待脚本完成，否则会与"等待本进程退出"互相死锁
	return nil
}
