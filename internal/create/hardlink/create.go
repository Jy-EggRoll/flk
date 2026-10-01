package hardlink

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/pkg/l10n"
)

// 该函数只处理创建逻辑，需要保证传入的路径一定是最正确、最简洁的，函数被调用时，应该优先处理字符串
func Create(primPath, secoPath string, removeOpts safeop.RemoveOptions) error {
	if _, err := os.Stat(primPath); err == nil {
		// 主路径存在才可能建链，这条 Debug 只记录「检查通过、走继续分支」这一事实
		// 文案不写 primPath/secoPath 这类代码标识符，改用项目统一的语义词「权威源」，
		// 具体对象由 path 字段承载
		logger.Debug(l10n.T("The authoritative path exists", nil), "path", primPath)
	} else {
		// 错误由命令层统一渲染，内部只上抛，避免结构化日志重复报告同一失败
		return err
	}

	// Windows: hardlink 不能跨盘（跨卷/跨文件系统）。提前给出明确错误，避免 --force 误删目标路径。
	if runtime.GOOS == "windows" {
		primVol := strings.ToUpper(filepath.VolumeName(primPath))
		secoVol := strings.ToUpper(filepath.VolumeName(secoPath))
		if primVol != "" && secoVol != "" && primVol != secoVol {
			return errors.New(l10n.T("Creating a hard link across filesystems is not allowed", nil))
		}
	}
	if _, err := os.Lstat(secoPath); err == nil { // 文件/链接/文件夹存在
		// 副路径已存在，先删旧再建新；措辞与主路径那条刻意区分（「派生位置」对「权威源」），
		// 否则两条相同的「已存在」混在一起就分不出是哪一侧
		logger.Debug(l10n.T("The derived path already exists", nil), "path", secoPath)
		if _, removeErr := safeop.RemoveWithConfirm(secoPath, removeOpts); removeErr != nil {
			if errors.Is(removeErr, safeop.ErrOperationCancelled) {
				// 用户看过确认后选了否，是正常交互结果而非异常，按约定用 Debug，不在 -v 时刷屏
				logger.Debug(l10n.T("User cancelled the deletion", nil), "path", secoPath)
				return removeErr
			}
			return removeErr
		}
	} else {
		// 副路径不存在是常规分支，只描述事实，err 一并带上便于确认 Lstat 的具体失败原因
		logger.Debug(l10n.T("The derived path does not exist", nil), "path", secoPath, "error", err)
	}

	if err := pathutil.EnsureDirExists(secoPath); err != nil {
		if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
			// secoPath 的父路径存在但不是目录（是文件或符号链接），删除计划必须沿用命令层注入的 stderr
			// NoTrash 一并透传，保证父路径删除与目标删除采用同一删除策略
			parentPath := filepath.Dir(secoPath)
			if _, removeErr := safeop.RemoveWithConfirm(parentPath, safeop.RemoveOptions{Force: removeOpts.Force, NoTrash: removeOpts.NoTrash, Output: removeOpts.Output}); removeErr != nil {
				if errors.Is(removeErr, safeop.ErrOperationCancelled) {
					// 删父路径时的取消同样属于正常交互结果，级别为 Debug，措辞与其它取消路径统一
					logger.Debug(l10n.T("User cancelled the deletion of the parent path", nil), "path", parentPath)
					return removeErr
				}
				return removeErr
			}
			if retryErr := pathutil.EnsureDirExists(secoPath); retryErr != nil {
				return retryErr
			}
		} else {
			return err
		}
	}

	if err := os.Link(primPath, secoPath); err != nil {
		return err
	}
	// 建链成功是本函数唯一真正改变文件系统的动作，补一条 Info 作为可审计的结果记录
	// 键沿用 store 的领域字段 prim/seco，与清单里 hardlink 记录的字段口径保持一致
	logger.Info(l10n.T("Hard link created", nil), "prim", primPath, "seco", secoPath)
	return nil
}
