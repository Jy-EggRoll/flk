package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	storeconfig "github.com/jy-eggroll/flk/internal/store"
	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "检查全局软硬链接的生效情况",
	Long:  "检查全局软硬链接的生效情况",
	Run:   RunCheck,
}

func init() {
	logger.Init(nil)
	rootCmd.AddCommand(checkCmd)
	checkCmd.Flags().StringVar(&checkDevice, "device", "", "设备名称，用于过滤检查")
	checkCmd.Flags().BoolVar(&checkSymlink, "symlink", false, "仅检查符号链接")
	checkCmd.Flags().BoolVar(&checkHardlink, "hardlink", false, "仅检查硬链接")
	checkCmd.Flags().StringVar(&checkDir, "dir", "", "仅检查包含该路径的记录")
}

var (
	checkDevice   string
	checkSymlink  bool
	checkHardlink bool
	checkDir      string
)

// CheckResult 单个链接的检查结果
type CheckResult = output.CheckResult

// RunCheck 执行链接检查并输出结果
func RunCheck(cmd *cobra.Command, args []string) {
	logger.Info("开始检查链接状态...")

	results, err := performCheck(CheckOptions{
		DeviceFilter:  checkDevice,
		CheckSymlink:  checkSymlink,
		CheckHardlink: checkHardlink,
		CheckDir:      checkDir,
	})
	if err != nil {
		logger.Error("检查失败：" + err.Error())
		return
	}

	format := output.OutputFormat(outputFormat)
	if err := output.PrintCheckResults(format, results); err != nil {
		logger.Error("输出失败：" + err.Error())
		return
	}

	logger.Info("检查完成")
}

// CheckOptions 检查选项
type CheckOptions struct {
	DeviceFilter  string
	CheckSymlink  bool
	CheckHardlink bool
	CheckDir      string
}

func performCheck(options CheckOptions) ([]output.CheckResult, error) {
	platform := runtime.GOOS
	var results []CheckResult

	data := storeconfig.GlobalManager.Data
	if data == nil {
		return results, nil
	}

	platformData, exists := data[platform]
	if !exists {
		return results, nil
	}

	if !options.CheckSymlink && !options.CheckHardlink {
		options.CheckSymlink = true
		options.CheckHardlink = true
	}

	for device, deviceData := range platformData {
		if options.DeviceFilter != "" && device != options.DeviceFilter {
			continue
		}

		for linkType, typeData := range deviceData {
			if (linkType == "symlink" && !options.CheckSymlink) ||
				(linkType == "hardlink" && !options.CheckHardlink) {
				continue
			}

			for path, entries := range typeData {
				if options.CheckDir != "" && !strings.Contains(path, options.CheckDir) {
					continue
				}

				basePath, err := pathutil.NormalizePath(path)
				if err != nil {
					basePath = path
				}

				for _, entry := range entries {
					result := output.CheckResult{
						Type:   linkType,
						Device: device,
						Path:   path,
					}

					if linkType == "symlink" {
						result.Real = entry["real"]
						result.Fake = entry["fake"]
						result.Valid, result.Error, result.ErrorType = checkSymlinkValid(result.Real, result.Fake, basePath)
					} else if linkType == "hardlink" {
						result.Prim = entry["prim"]
						result.Seco = entry["seco"]
						result.Valid, result.Error, result.ErrorType = checkHardlinkValid(result.Prim, result.Seco, basePath)
					}

					results = append(results, result)
				}
			}
		}
	}

	return results, nil
}

func checkSymlinkValid(real, fake, basePath string) (bool, string, string) {
	expandedFake, err := pathutil.NormalizePath(fake)
	if err != nil {
		return false, fmt.Sprintf("无法展开符号链接路径 %s: %v", fake, err), "PATH_EXPAND_FAIL"
	}

	fakeInfo, err := os.Lstat(expandedFake)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Sprintf("符号链接文件 %s 不存在", fake), "LINK_MISSING"
		}
		return false, fmt.Sprintf("无法访问符号链接文件 %s: %v", fake, err), "LINK_ACCESS_FAIL"
	}

	if fakeInfo.Mode()&os.ModeSymlink == 0 {
		return false, fmt.Sprintf("%s 存在但不是符号链接", fake), "NOT_SYMLINK"
	}

	target, err := os.Readlink(expandedFake)
	if err != nil {
		return false, fmt.Sprintf("无法读取符号链接 %s 的目标: %v", fake, err), "READLINK_FAIL"
	}

	var targetAbs string
	if filepath.IsAbs(target) {
		targetAbs = target
	} else {
		targetAbs = filepath.Join(filepath.Dir(expandedFake), target)
	}

	var expectedAbs string
	if filepath.IsAbs(real) {
		expectedAbs = real
	} else {
		expectedAbs = filepath.Join(basePath, real)
	}
	if expanded, expandErr := pathutil.NormalizePath(expectedAbs); expandErr == nil {
		expectedAbs = expanded
	}

	targetInfo, err := os.Stat(targetAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Sprintf("符号链接的目标文件 %s 不存在", targetAbs), "TARGET_MISSING"
		}
		return false, fmt.Sprintf("无法访问符号链接的目标文件 %s: %v", targetAbs, err), "TARGET_ACCESS_FAIL"
	}

	expectedInfo, err := os.Stat(expectedAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Sprintf("期望的目标文件 %s 不存在", expectedAbs), "EXPECTED_MISSING"
		}
		return false, fmt.Sprintf("无法访问期望的目标文件 %s: %v", expectedAbs, err), "EXPECTED_ACCESS_FAIL"
	}

	if !os.SameFile(targetInfo, expectedInfo) {
		return false, fmt.Sprintf("符号链接 %s 指向的文件与期望的文件 %s 不一致", fake, real), "TARGET_MISMATCH"
	}

	return true, "", ""
}

func checkHardlinkValid(prim, seco, basePath string) (bool, string, string) {
	var expandedPrim string
	if filepath.IsAbs(prim) {
		expandedPrim = prim
	} else {
		expandedPrim = filepath.Join(basePath, prim)
	}

	expandedSeco := seco

	primInfo, err := os.Stat(expandedPrim)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Sprintf("主文件 %s 不存在", prim), "PRIM_MISSING"
		}
		return false, fmt.Sprintf("无法访问主文件 %s: %v", prim, err), "PRIM_ACCESS_FAIL"
	}

	secoInfo, err := os.Stat(expandedSeco)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Sprintf("硬链接文件 %s 不存在", seco), "SECO_MISSING"
		}
		return false, fmt.Sprintf("无法访问硬链接文件 %s: %v", seco, err), "SECO_ACCESS_FAIL"
	}

	if !os.SameFile(primInfo, secoInfo) {
		return false, fmt.Sprintf("%s 和 %s 不是同一个文件的硬链接", seco, prim), "NOT_SAME_FILE"
	}

	return true, "", ""
}
