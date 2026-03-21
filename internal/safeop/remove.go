package safeop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/pterm/pterm"
)

var ErrOperationCancelled = errors.New("操作已取消")

type ConfirmFunc func() (bool, error)

type RemoveOptions struct {
	Force   bool
	Output  io.Writer
	Confirm ConfirmFunc
}

func PlanRemove(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return []string{absPath}, nil
	}

	var paths []string
	err = filepath.WalkDir(absPath, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		paths = append(paths, current)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 保证输出稳定，便于测试与用户阅读
	sort.Strings(paths)
	return paths, nil
}

func RemoveWithConfirm(path string, opts RemoveOptions) ([]string, error) {
	paths, err := PlanRemove(path)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}

	out := opts.Output
	if out == nil {
		out = os.Stdout
	}

	if err := printDeletePlan(out, paths); err != nil {
		return nil, err
	}

	if !opts.Force {
		confirm := opts.Confirm
		if confirm == nil {
			confirm = defaultConfirm
		}
		ok, err := confirm()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrOperationCancelled
		}
	}

	for _, p := range paths {
		if err := pathutil.ValidateSafePath(p); err != nil {
			return nil, err
		}
	}

	if err := os.RemoveAll(path); err != nil {
		return nil, err
	}
	return paths, nil
}

func printDeletePlan(out io.Writer, paths []string) error {
	if _, err := fmt.Fprintln(out, "有以下文件会在恢复过程中被删除:"); err != nil {
		return err
	}
	for _, p := range paths {
		if _, err := fmt.Fprintln(out, p); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "您是否确认[y/N]")
	return err
}

func defaultConfirm() (bool, error) {
	return pterm.DefaultInteractiveConfirm.WithDefaultValue(false).Show("您是否确认[y/N]")
}
