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
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:     "check",
	Aliases: []string{"ck"},
	Short:   "检查全局软硬链接的生效情况",
	Long:    "检查全局软硬链接的生效情况",
	Run:     RunCheck,
}

func init() {
	rootCmd.AddCommand(checkCmd)
	checkCmd.Flags().StringVarP(&checkDevice, "device", "d", "", "设备名称，用于过滤检查，可用逗号分隔多个设备")
	checkCmd.Flags().BoolVar(&checkSymlink, "symlink", false, "仅检查符号链接")
	checkCmd.Flags().BoolVar(&checkHardlink, "hardlink", false, "仅检查硬链接")
	checkCmd.Flags().BoolVar(&checkCopy, "copy", false, "仅检查复制")
	checkCmd.Flags().StringVar(&checkDir, "dir", "", "仅检查包含该路径的记录")
}

var (
	checkDevice   string
	checkSymlink  bool
	checkHardlink bool
	checkCopy     bool
	checkDir      string
)

// CheckResult 单个链接的检查结果
type CheckResult = output.CheckResult

// RunCheck 执行链接检查并输出结果
func RunCheck(cmd *cobra.Command, args []string) {
	deviceFilters := parseDeviceFilters(checkDevice)
	results, err := performCheck(CheckOptions{
		DeviceFilters: deviceFilters,
		CheckSymlink:  checkSymlink,
		CheckHardlink: checkHardlink,
		CheckCopy:     checkCopy,
		CheckDir:      checkDir,
	})
	if err != nil {
		logger.Error("检查失败 " + err.Error())
		return
	}

	format := output.OutputFormat(outputFormat)
	if err := output.PrintCheckResults(format, results); err != nil {
		logger.Error("输出失败 " + err.Error())
		return
	}

	logger.Info("检查完成")
}

// CheckOptions 检查选项
type CheckOptions struct {
	DeviceFilters []string
	CheckSymlink  bool
	CheckHardlink bool
	CheckCopy     bool
	CheckDir      string
}

func performCheck(options CheckOptions) ([]output.CheckResult, error) {
	platform := runtime.GOOS
	var results []CheckResult

	data := store.GlobalManager.Data
	if data == nil {
		return results, nil
	}

	platformData, exists := data[platform]
	if !exists {
		return results, nil
	}

	if !options.CheckSymlink && !options.CheckHardlink && !options.CheckCopy {
		options.CheckSymlink = true
		options.CheckHardlink = true
		options.CheckCopy = true
	}

	for device, deviceData := range platformData {
		if len(options.DeviceFilters) > 0 && !contains(options.DeviceFilters, device) {
			continue
		}

		for linkType, entries := range deviceData {
			if (linkType == "symlink" && !options.CheckSymlink) ||
				(linkType == "hardlink" && !options.CheckHardlink) ||
				(linkType == "copy" && !options.CheckCopy) {
				continue
			}

			for _, entry := range entries {
				// --dir 过滤：匹配任意路径字段
				if options.CheckDir != "" {
					matches := false
					for _, v := range entry {
						if strings.Contains(v, options.CheckDir) {
							matches = true
							break
						}
					}
					if !matches {
						continue
					}
				}

				result := output.CheckResult{
					Type:   linkType,
					Device: device,
				}

			switch linkType {
			case "symlink":
				result.Real = entry["real"]
				result.Fake = entry["fake"]
				result.Valid, result.Error, result.ErrorType = checkSymlinkValid(result.Real, result.Fake)
			case "hardlink":
				result.Prim = entry["prim"]
				result.Seco = entry["seco"]
				result.Valid, result.Error, result.ErrorType = checkHardlinkValid(result.Prim, result.Seco)
			case "copy":
				result.Src = entry["src"]
				result.Dst = entry["dst"]
				result.Valid, result.Error, result.ErrorType = checkCopyValid(result.Src, result.Dst)
			}

				results = append(results, result)
			}
		}
	}

	return results, nil
}

func checkCopyValid(src, dst string) (bool, string, string) {
	expandedSrc, err := pathutil.NormalizePath(src)
	if err != nil {
		return false, fmt.Sprintf("无法展开源路径 %s: %v", src, err), "PATH_EXPAND_FAIL"
	}

	expandedDst, err := pathutil.NormalizePath(dst)
	if err != nil {
		return false, fmt.Sprintf("无法展开目标路径 %s: %v", dst, err), "PATH_EXPAND_FAIL"
	}

	srcInfo, srcErr := os.Stat(expandedSrc)
	dstInfo, dstErr := os.Stat(expandedDst)

	switch {
	case srcErr != nil && dstErr != nil:
		return false, "源文件和目标文件都不存在", "BOTH_MISSING"
	case srcErr != nil:
		return false, fmt.Sprintf("源文件 %s 不存在", src), "SRC_MISSING"
	case dstErr != nil:
		return false, fmt.Sprintf("目标文件 %s 不存在", dst), "DST_MISSING"
	case !srcInfo.ModTime().Equal(dstInfo.ModTime()):
		return false, "源文件和目标文件的修改时间不一致，需要同步", "MOD_TIME_MISMATCH"
	default:
		return true, "", ""
	}
}

func checkSymlinkValid(real, fake string) (bool, string, string) {
	expandedReal, err := pathutil.NormalizePath(real)
	if err != nil {
		return false, fmt.Sprintf("无法展开源路径 %s: %v", real, err), "PATH_EXPAND_FAIL"
	}

	expandedFake, err := pathutil.NormalizePath(fake)
	if err != nil {
		return false, fmt.Sprintf("无法展开链接路径 %s: %v", fake, err), "PATH_EXPAND_FAIL"
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

	targetInfo, err := os.Stat(targetAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Sprintf("符号链接的目标文件 %s 不存在", targetAbs), "TARGET_MISSING"
		}
		return false, fmt.Sprintf("无法访问符号链接的目标文件 %s: %v", targetAbs, err), "TARGET_ACCESS_FAIL"
	}

	expectedInfo, err := os.Stat(expandedReal)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Sprintf("期望的目标文件 %s 不存在", expandedReal), "EXPECTED_MISSING"
		}
		return false, fmt.Sprintf("无法访问期望的目标文件 %s: %v", expandedReal, err), "EXPECTED_ACCESS_FAIL"
	}

	if !os.SameFile(targetInfo, expectedInfo) {
		return false, fmt.Sprintf("符号链接 %s 指向的文件与期望的文件 %s 不一致", fake, real), "TARGET_MISMATCH"
	}

	return true, "", ""
}

func checkHardlinkValid(prim, seco string) (bool, string, string) {
	expandedPrim, err := pathutil.NormalizePath(prim)
	if err != nil {
		return false, fmt.Sprintf("无法展开主文件路径 %s: %v", prim, err), "PATH_EXPAND_FAIL"
	}

	expandedSeco, err := pathutil.NormalizePath(seco)
	if err != nil {
		return false, fmt.Sprintf("无法展开硬链接路径 %s: %v", seco, err), "PATH_EXPAND_FAIL"
	}

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

func parseDeviceFilters(deviceStr string) []string {
	if deviceStr == "" {
		return nil
	}
	var filters []string
	for _, d := range strings.Split(deviceStr, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			filters = append(filters, d)
		}
	}
	return filters
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}
