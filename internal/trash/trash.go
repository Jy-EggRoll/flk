package trash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/pkg/l10n"
)

// trashRoot 是 FLK 回收站的根目录，遵循 XDG 数据目录规范
// 用户通过「假删除」移入的文件/目录都存放于此
var trashRoot string

func init() {
	trashRoot = resolveTrashRoot()
}

// resolveTrashRoot 确定回收站根目录
// 优先 $XDG_DATA_HOME/flk/trash，回退到 ~/.local/share/flk/trash
func resolveTrashRoot() string {
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg != "" {
		return filepath.Join(xdg, "flk", "trash")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// 极端情况：连家目录都拿不到，退到当前目录下的 .flk-trash
		return ".flk-trash"
	}
	return filepath.Join(home, ".local", "share", "flk", "trash")
}

// renameFunc 间接一层 os.Rename，只为让测试能稳定注入「跨文件系统」这一失败形态
//
// 真实的 EXDEV 需要两个不同的文件系统，在 CI 容器里未必存在（/dev/shm 可能缺失或与 / 同设备），
// 而这条降级路径恰恰是 Windows 用户默认会走到的（回收站固定在 C:，被删文件可能在 D:），
// 属于必须被测试覆盖的主路径，因此留出这个注入点
var renameFunc = os.Rename

// MoveToTrash 将文件或目录移入 FLK 回收站，忠实保留原绝对路径树结构
//
// 回收站路径结构：
//
//	~/.local/share/flk/trash/2026-07-30-120405/home/user/.zshrc
//	                         └── 时间戳 ──┴── 原绝对路径（去前导 /）
//
// 两种搬运方式，优先用代价小的那种：
//  1. os.Rename：同文件系统内瞬时完成，不产生额外磁盘占用
//  2. 复制 → 删除源：rename 失败时的兜底。回收站根目录固定在用户家目录所在卷（Windows 上即 C:），
//     而被删文件可能在另一个卷（D:）或另一个挂载点，此时 rename 必然失败（跨设备）。
//     这条兜底让「假删除」在所有卷上都能成立，而不是只有家目录所在卷可用
//
// 源不存在时返回 *os.PathError（由调用方按需容错）
func MoveToTrash(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to resolve the absolute path {{.Path}}", map[string]any{"Path": path}), err)
	}

	// 构造回收站目标路径：trashRoot/TIMESTAMP/absolute/path
	now := time.Now()
	sessionDir := filepath.Join(trashRoot, now.Format("2006-01-02-150405"))

	// 去掉盘符（Windows）和前导路径分隔符，将原绝对路径作为相对路径拼接到 sessionDir 下
	relPath := strings.ReplaceAll(absPath, ":", "")
	relPath = strings.TrimPrefix(relPath, string(os.PathSeparator))
	dest := filepath.Join(sessionDir, relPath)

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to create the trash directory", nil), err)
	}

	renameErr := renameFunc(absPath, dest)
	if renameErr == nil {
		// 首选路径（同设备 rename）成功是这个函数唯一的正常出口，补一条 Debug 记录「从哪搬到哪」
		// 与下面「rename 失败改走复制」那条互为对照：有成功记录才能一眼看出本次到底走了哪条路径
		logger.Debug(l10n.T("Moved to the trash", nil), "from", absPath, "to", dest)
		return nil
	}

	// 不区分 rename 失败的具体原因：跨设备、目标所在卷只读等等都会走到这里，
	// 而兜底动作（复制再删除）对「源与目标不同设备」这一最常见情形是唯一可行的做法，
	// 对其他原因也只是多一次注定失败的尝试，代价远小于为每种 errno 维护一份平台分支
	logger.Debug(l10n.T("Rename failed; falling back to copying into the trash", nil), "from", absPath, "to", dest, "error", renameErr)

	if copyErr := copyIntoTrash(absPath, dest); copyErr != nil {
		// 两个原因都要带上：只报复制失败会让人以为「复制本身有问题」，
		// 而真实起因往往是跨设备（复制是补救手段），排错时必须能看到这一层
		return fmt.Errorf("%s: %w", l10n.T("Failed to move to the trash {{.From}} -> {{.To}}", map[string]any{"From": absPath, "To": dest}), errors.Join(renameErr, copyErr))
	}
	return nil
}

// copyIntoTrash 以「先复制、再删除源」的方式完成移入回收站
//
// 顺序不可颠倒：必须先确认回收站里已有完整一份，才允许动源。反过来先删源再复制，
// 中途失败就会两头都没有——「假删除」当场变成真丢数据，且没有任何副本可救
//
// 失败处理取向：
//   - 复制失败：清掉回收站里的半成品并返回错误，源保持原样。半成品留着会让事后手工恢复的人
//     误以为这里有一份完整备份
//   - 删除源失败：不清理回收站里那份，直接返回错误。此时源可能已被部分删除，
//     那份复制很可能是唯一的完整数据，宁可留一份多余备份也不能把仅存的数据删掉
//
// 残留代价（刻意接受）：复制失败时只清理了 dest 本身，其父目录可能留下一串空目录；
// 要精确清理就得知道本次会话目录是否被其它删除共享，为这点美观引入目录归属追踪不划算，
// 而空目录不占数据、也不影响手工恢复
func copyIntoTrash(src, dest string) error {
	if err := pathutil.Copy(src, dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	return os.RemoveAll(src)
}
