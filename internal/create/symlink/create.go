package symlink

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/pkg/l10n"
)

// 该函数只处理创建逻辑，需要保证传入的路径一定是最正确、最简洁的，函数被调用时，应该优先处理字符串
func Create(realPath, fakePath string, removeOpts safeop.RemoveOptions) error {
	if _, err := os.Stat(realPath); err == nil {
		// 主路径存在才可能建链，这条 Debug 只记录「检查通过、走继续分支」这一事实
		// 文案不写 realPath/fakePath 这类代码标识符，改用项目统一的语义词「权威源」，
		// 具体对象由 path 字段承载
		logger.Debug(l10n.T("The authoritative path exists", nil), "path", realPath)
	} else {
		// 错误由命令层渲染为唯一的 CreateResult，此处直接上抛，避免同一失败同时写入日志和结果流
		return err
	}

	if _, err := os.Lstat(fakePath); err == nil { // 文件/链接/文件夹存在
		// 副路径已存在，先删旧再建新；措辞与主路径那条刻意区分（「派生位置」对「权威源」），
		// 否则两条相同的「已存在」混在一起就分不出是哪一侧
		logger.Debug(l10n.T("The derived path already exists", nil), "path", fakePath)
		if _, removeErr := safeop.RemoveWithConfirm(fakePath, removeOpts); removeErr != nil {
			if errors.Is(removeErr, safeop.ErrOperationCancelled) {
				// 用户看过确认后选了否，是正常交互结果而非异常，按约定用 Debug，不在 -v 时刷屏
				logger.Debug(l10n.T("User cancelled the deletion", nil), "path", fakePath)
				return removeErr
			}
			return removeErr
		}
	} else {
		// 副路径不存在是常规分支，只描述事实，err 一并带上便于确认 Lstat 的具体失败原因
		logger.Debug(l10n.T("The derived path does not exist", nil), "path", fakePath, "error", err)
	}

	if err := pathutil.EnsureDirExists(fakePath); err != nil {
		if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
			// fakePath 的父路径存在但不是目录（是文件或符号链接），删除计划必须沿用命令层注入的 stderr
			// NoTrash 一并透传，保证父路径删除与目标删除采用同一删除策略
			parentPath := filepath.Dir(fakePath)
			if _, removeErr := safeop.RemoveWithConfirm(parentPath, safeop.RemoveOptions{Force: removeOpts.Force, NoTrash: removeOpts.NoTrash, Output: removeOpts.Output}); removeErr != nil {
				if errors.Is(removeErr, safeop.ErrOperationCancelled) {
					// 删父路径时的取消同样属于正常交互结果，级别为 Debug，措辞与其它取消路径统一
					logger.Debug(l10n.T("User cancelled the deletion of the parent path", nil), "path", parentPath)
					return removeErr
				}
				return removeErr
			}
			if retryErr := pathutil.EnsureDirExists(fakePath); retryErr != nil {
				return retryErr
			}
		} else {
			return err
		}
	}

	absRealPath, err := filepath.Abs(realPath)
	if err != nil {
		return err
	}

	if err := os.Symlink(absRealPath, fakePath); err != nil {
		return err
	}
	// 建链成功是本函数唯一真正改变文件系统的动作，补一条 Info 作为可审计的结果记录
	// 键沿用 store 的领域字段 real/fake，与清单里 symlink 记录的字段口径保持一致
	logger.Info(l10n.T("Symbolic link created", nil), "real", absRealPath, "fake", fakePath)
	return nil
}
