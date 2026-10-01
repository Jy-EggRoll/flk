package updater

import (
	"errors"
	"fmt"
	"os"

	"github.com/jy-eggroll/flk/pkg/l10n"
)

// swapExecutable 用「原地改名交接」把 staged 换成 execPath，失败时尽力回滚
//
// 三步：把自己改名为 oldPath（腾出原名）→ 把新文件改成原名 → 完成。
// staged 与 execPath 必须同目录，同目录改名才是原子的，也才不依赖复制。
//
// 为什么 Windows 上需要这套交接：Windows 只禁止覆盖或删除**正在运行的映像**，
// 并不禁止给它改名，因此「先让名，再占用名」是唯一能在进程自己还没退出时就完成替换的做法。
// 旧实现把这一步交给一个批处理脚本，等进程退出后再由脚本复制覆盖；那条路有两个无法回避的问题：
//   - 脚本内容以 UTF-8 落盘，而 cmd.exe 按系统本地代码页读取，安装目录里只要出现非 ASCII 字符
//     （例如「下载缓存」），脚本里的路径就被解码成乱码，复制与保留双双失败
//   - 脚本是「丢出去就不管」的，主进程无从知道替换是否成功，于是失败也会报「升级完成」
//
// 这套交接全程在 Go 里完成，不经过任何外部解释器，因此与代码页、路径字符集、控制台状态都无关，
// 失败也必然如实返回给调用方（调用方会据此清掉暂存文件并报错）
//
// 放在平台无关文件中是为了让这段逻辑能在任意平台上被测试覆盖：
// 「改名交接 + 回滚」的行为可以用普通文件在 Linux 上完整断言，
// 真正依赖 Windows 的只有「能否给运行中的映像改名」这一点
//
// 潜在影响点：本函数无法清理 oldPath——它此刻仍是本进程正在使用的映像，Windows 不允许删除，
// 只能等下一次运行再清。调用方固定传同一个 oldPath，使残留至多一份，不随升级次数累积
func swapExecutable(staged, execPath, oldPath string) error {
	if err := os.Rename(execPath, oldPath); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to replace the executable (write permission on {{.Path}} may be required)", map[string]any{"Path": execPath}), err)
	}

	if err := os.Rename(staged, execPath); err != nil {
		// 新版本没能就位，必须把旧文件改回原名：否则用户手里既没有新版本，
		// 原名下也没有可运行的文件，等于一次失败的升级把程序弄丢了
		if rollbackErr := os.Rename(oldPath, execPath); rollbackErr != nil {
			// 两个错误都要带上：回滚也失败意味着原名下现在是空的，用户必须知道旧文件被挪去了哪里
			return fmt.Errorf("%s: %w", l10n.T("Failed to put the new version in place, and the previous version could not be restored either (the old file is at {{.Old}})", map[string]any{"Old": oldPath}), errors.Join(err, rollbackErr))
		}
		return fmt.Errorf("%s: %w", l10n.T("Failed to put the new version in place; the previous version has been restored", nil), err)
	}

	return nil
}
