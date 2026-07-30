package trash

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// MoveToTrash 将文件或目录移入 FLK 回收站，忠实保留原绝对路径树结构
//
// 回收站路径结构：
//
//	~/.local/share/flk/trash/2026-07-30-120405/home/user/.zshrc
//	                         └── 时间戳 ──┴── 原绝对路径（去前导 /）
//
// 使用 os.Rename（同文件系统，瞬时完成），跨文件系统暂返回明确错误
// 源不存在时返回 *os.PathError（由调用方按需容错）
func MoveToTrash(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析绝对路径失败 %s: %w", path, err)
	}

	// 构造回收站目标路径：trashRoot/TIMESTAMP/absolute/path
	now := time.Now()
	sessionDir := filepath.Join(trashRoot, now.Format("2006-01-02-150405"))

	// 去掉前导 / 后，将原绝对路径作为相对路径拼接到 sessionDir 下
	relPath := strings.TrimPrefix(absPath, string(os.PathSeparator))
	dest := filepath.Join(sessionDir, relPath)

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("创建回收站目录失败: %w", err)
	}

	if err := os.Rename(absPath, dest); err != nil {
		return fmt.Errorf("移至回收站失败 %s -> %s: %w", absPath, dest, err)
	}

	return nil
}
