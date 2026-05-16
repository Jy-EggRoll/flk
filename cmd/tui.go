package cmd

import (
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/jy-eggroll/flk/internal/tui"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "启动全屏终端用户界面",
	Long:  "启动全屏终端用户界面（基于 Bubble Tea），支持浏览、检查、修复、创建链接等操作",
	RunE: func(cmd *cobra.Command, args []string) error {
		exec := buildTuiExecutor()
		return tui.Start(Version, BuildTime, exec)
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

func buildTuiExecutor() tui.Executor {
	return tui.Executor{
		PerformCheck: func(opts tui.CheckOptions) ([]tui.CheckResult, error) {
			results, err := PerformCheck(CheckOptions{
				DeviceFilters: opts.DeviceFilters,
				CheckSymlink:  opts.CheckSymlink,
				CheckHardlink: opts.CheckHardlink,
				CheckDir:      opts.CheckDir,
			})
			if err != nil {
				return nil, err
			}
			tuiResults := make([]tui.CheckResult, len(results))
			for i, r := range results {
				tuiResults[i] = convertCheckResult(r)
			}
			return tuiResults, nil
		},
		RepairResult: func(result tui.CheckResult, idx int) error {
			orig := convertBackCheckResult(result)
			return RepairResult(orig, idx)
		},
		CreateSymlink: func(real, fake, device string, force, smart bool) error {
			oldReal := symlinkReal
			oldFake := symlinkFake
			oldDevice := createDevice
			oldForce := createForce
			oldSmart := createSmart

			symlinkReal = real
			symlinkFake = fake
			createDevice = device
			createForce = force
			createSmart = smart

			err := Symlink(nil, nil)

			symlinkReal = oldReal
			symlinkFake = oldFake
			createDevice = oldDevice
			createForce = oldForce
			createSmart = oldSmart

			return err
		},
		CreateHardlink: func(prim, seco, device string, force, smart bool) error {
			oldPrim := hardlinkPrim
			oldSeco := hardlinkSeco
			oldDevice := createDevice
			oldForce := createForce
			oldSmart := createSmart

			hardlinkPrim = prim
			hardlinkSeco = seco
			createDevice = device
			createForce = force
			createSmart = smart

			err := Hardlink(nil, nil)

			hardlinkPrim = oldPrim
			hardlinkSeco = oldSeco
			createDevice = oldDevice
			createForce = oldForce
			createSmart = oldSmart

			return err
		},
		RefreshStore: func() error {
			pathutil.SetWorkDir(WorkDir)
			if err := store.InitStore(store.StorePath); err != nil {
				pterm.Error.Println("初始化存储失败: ", err)
				return err
			}
			return nil
		},
	}
}

func convertCheckResult(r output.CheckResult) tui.CheckResult {
	return tui.CheckResult{
		Type:      r.Type,
		Device:    r.Device,
		Path:      r.Path,
		BasePath:  r.BasePath,
		Real:      r.Real,
		Fake:      r.Fake,
		Prim:      r.Prim,
		Seco:      r.Seco,
		Valid:     r.Valid,
		Error:     r.Error,
		ErrorType: r.ErrorType,
	}
}

func convertBackCheckResult(r tui.CheckResult) output.CheckResult {
	return output.CheckResult{
		Type:      r.Type,
		Device:    r.Device,
		Path:      r.Path,
		BasePath:  r.BasePath,
		Real:      r.Real,
		Fake:      r.Fake,
		Prim:      r.Prim,
		Seco:      r.Seco,
		Valid:     r.Valid,
		Error:     r.Error,
		ErrorType: r.ErrorType,
	}
}

