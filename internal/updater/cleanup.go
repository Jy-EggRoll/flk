package updater

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stagingPrefix 是升级暂存文件的名字前缀
//
// 下载（CreateTemp）与残留清理（CleanupLeftovers）共用这一处定义：
// 两边各写一遍字面量，日后改名时必然只改到一半，剩下的一半就会变成
// 「下载留下的文件永远不被当作残留」
const stagingPrefix = ".upgrade-"

// oldSuffix 是原地改名交接时给旧可执行文件加的后辍
// 同样由交接（replace_windows.go）与残留清理共用
const oldSuffix = ".old"

// staleStagingMaxAge 决定暂存文件「多久没被写过」才算残留
//
// 为什么不无条件删：另一个进程可能正在下载，它的暂存文件会随下载持续写入；
// 此时把文件删掉会让那次升级失败。取十分钟是因为正常的下载即使很慢，
// 也不会十分钟不产生一次写入——真的十分钟没有任何写入，那次下载早已死了
const staleStagingMaxAge = 10 * time.Minute

// CleanupLeftovers 在正常运行开始时清理上一次升级留下的文件
//
// 两类残留：
//   - <可执行文件>.old：原地改名交接产生的旧版本。替换成功的那一刻，它正是当前进程的映像，
//     正在运行的进程删不掉自己的映像，只能等下一次运行——那时旧进程已退出，文件不再被占用
//   - .upgrade-*：下载被中断（进程被杀、断电）时留在安装目录里的暂存文件
//
// 刻意 best-effort、不返回错误也不打印任何东西：清理受阻（文件仍被另一个进程占用、
// 安装目录只读）不该影响这一次正常使用，用户也不需要看到一条与自己无关的启动提示。
// 清理失败留下的文件会在下次运行再试，最多只是继续占着一份空间
//
// 潜在影响点：只处理安装目录这一层，不递归；只认上面两类名字，
// 绝不删除任何其它文件——安装目录里可能放着用户自己的东西
func CleanupLeftovers(execPath string) {
	// 旧版本可能仍被另一个还在运行的老进程占用，删不掉就算了
	_ = os.Remove(execPath + oldSuffix)

	dir := filepath.Dir(execPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), stagingPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < staleStagingMaxAge {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}
