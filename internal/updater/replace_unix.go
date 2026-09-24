//go:build !windows

package updater

import (
	"fmt"
	"os"

	"github.com/jy-eggroll/flk/pkg/l10n"
)

// replaceExecutable 用重命名原子替换目标可执行文件
//
// Unix 允许 rename 覆盖正在运行的可执行文件：内核通过 inode 继续支撑已启动的进程，
// 新文件立即对后续启动生效，因此这里既不需要外部脚本，也不需要等待当前进程退出
//
// 这正是本实现相对"复制 + 延迟"做法的关键优势：
// 复制会以写方式打开运行中的可执行文件而必然得到 ETXTBSY，只能靠睡眠等待进程退出，
// 一旦进程未按时退出（例如升级由长驻的子命令触发）替换就会静默失败
func replaceExecutable(staged, execPath string) error {
	if err := os.Rename(staged, execPath); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to replace the executable (write permission on {{.Path}} may be required)", map[string]any{"Path": execPath}), err)
	}
	return nil
}
