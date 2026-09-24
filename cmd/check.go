package cmd

import (
	"fmt"

	"github.com/jy-eggroll/flk/pkg/l10n"
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
	Short:   l10n.T("Check the status of all symlinks and hardlinks", nil),
	Long:    l10n.T("Check the status of all symlinks and hardlinks", nil),
	RunE:    RunCheck,
}

func init() {
	MarkNeedsStore(checkCmd)
	MarkSupportsJSON(checkCmd)
	rootCmd.AddCommand(checkCmd)
	checkCmd.Flags().StringVarP(&checkDevice, "device", "d", "", l10n.T("Device names to filter by, comma-separated", nil))
	checkCmd.Flags().BoolVar(&checkSymlink, "symlink", false, l10n.T("Check only symbolic links", nil))
	checkCmd.Flags().BoolVar(&checkHardlink, "hardlink", false, l10n.T("Check only hard links", nil))
	checkCmd.Flags().BoolVar(&checkCopy, "copy", false, l10n.T("Check only copies", nil))
	checkCmd.Flags().StringVar(&checkDir, "dir", "", l10n.T("Check only records containing this path", nil))
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

// RunCheck 执行链接检查并把业务结果写入命令标准输出
// 检查或输出失败由 Cobra 统一处理并转换为非零退出；记录无效属于正常业务结果，不应作为命令错误返回
func RunCheck(cmd *cobra.Command, args []string) error {
	deviceFilters := parseDeviceFilters(checkDevice)
	results, err := performCheck(CheckOptions{
		DeviceFilters: deviceFilters,
		CheckSymlink:  checkSymlink,
		CheckHardlink: checkHardlink,
		CheckCopy:     checkCopy,
		CheckDir:      checkDir,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Check failed", nil), err)
	}

	format := output.OutputFormat(outputFormat)
	if err := output.PrintCheckResults(cmd.OutOrStdout(), format, results); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Output failed", nil), err)
	}

	logger.Info(l10n.T("Check complete", nil))
	return nil
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

	// 防御性判空：若 InitStore 失败，GlobalManager 可能为 nil，直接解引用 .Data 会 panic
	if store.GlobalManager == nil {
		return results, nil
	}

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

	// --dir 过滤值预处理：存储中的路径统一为折叠绝对路径（~ 形式），
	// 而用户输入可能是 ~/.config、/root/.config 等各种写法，直接与折叠值做 Contains 往往匹配不上
	// 这里预先算出「折叠后的过滤串」，与存储形式对齐；同时保留原始输入用于宽松的子串匹配
	var foldedDirFilter string
	if options.CheckDir != "" {
		if normalized, err := pathutil.NormalizePath(options.CheckDir); err == nil {
			if folded, ferr := pathutil.FoldHome(normalized); ferr == nil {
				foldedDirFilter = folded
			}
		}
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
				// 存储值为折叠形式，故同时用「折叠后的过滤串」和「原始输入」两种方式做子串匹配，任一命中即保留
				if options.CheckDir != "" {
					matches := false
					for _, v := range entry {
						if strings.Contains(v, options.CheckDir) ||
							(foldedDirFilter != "" && strings.Contains(v, foldedDirFilter)) {
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

// checkCopyValid 校验一条 copy 记录是否有效
// 语义（按需求）：以「文件内容」为准，而非修改时间。
// 只要 src、dst 内容完全一致，即认为该 copy 有效，即便两者的修改时间不同也不算失效；
// 仅当大小不同（SIZE_MISMATCH）或大小相同但内容不同（CONTENT_MISMATCH）时才判为无效。
// 之前的实现仅比较 ModTime，会出现「内容不同但时间恰好相同 → 误判有效」的漏洞
func checkCopyValid(src, dst string) (bool, string, string) {
	expandedSrc, err := pathutil.NormalizePath(src)
	if err != nil {
		return false, l10n.T("Failed to expand the source path {{.Path}}: {{.Err}}", map[string]any{"Path": src, "Err": err.Error()}), "PATH_EXPAND_FAIL"
	}

	expandedDst, err := pathutil.NormalizePath(dst)
	if err != nil {
		return false, l10n.T("Failed to expand the destination path {{.Path}}: {{.Err}}", map[string]any{"Path": dst, "Err": err.Error()}), "PATH_EXPAND_FAIL"
	}

	srcInfo, srcErr := os.Stat(expandedSrc)
	dstInfo, dstErr := os.Stat(expandedDst)

	switch {
	case srcErr != nil && dstErr != nil:
		return false, l10n.T("Both the source and destination files are missing", nil), "BOTH_MISSING"
	case srcErr != nil:
		return false, l10n.T("The source file {{.Path}} does not exist", map[string]any{"Path": src}), "SRC_MISSING"
	case dstErr != nil:
		return false, l10n.T("The destination file {{.Path}} does not exist", map[string]any{"Path": dst}), "DST_MISSING"
	}

	// 先比大小：不同必然内容不同，可快速判定，省去哈希整份文件的开销
	if srcInfo.Size() != dstInfo.Size() {
		return false, l10n.T("The source and destination sizes differ ({{.Src}} vs {{.Dst}})", map[string]any{"Src": srcInfo.Size(), "Dst": dstInfo.Size()}), "SIZE_MISMATCH"
	}

	// 大小相同再逐字节比较内容（通过 sha256 哈希）
	srcHash, err := pathutil.FileHash(expandedSrc)
	if err != nil {
		return false, l10n.T("Failed to hash the source file {{.Path}}: {{.Err}}", map[string]any{"Path": src, "Err": err.Error()}), "SRC_ACCESS_FAIL"
	}
	dstHash, err := pathutil.FileHash(expandedDst)
	if err != nil {
		return false, l10n.T("Failed to hash the destination file {{.Path}}: {{.Err}}", map[string]any{"Path": dst, "Err": err.Error()}), "DST_ACCESS_FAIL"
	}
	if srcHash != dstHash {
		return false, l10n.T("The source and destination contents differ and need to be synchronized", nil), "CONTENT_MISMATCH"
	}

	// 内容一致即视为有效，忽略 ModTime 差异
	return true, "", ""
}

func checkSymlinkValid(real, fake string) (bool, string, string) {
	expandedReal, err := pathutil.NormalizePath(real)
	if err != nil {
		return false, l10n.T("Failed to expand the source path {{.Path}}: {{.Err}}", map[string]any{"Path": real, "Err": err.Error()}), "PATH_EXPAND_FAIL"
	}

	expandedFake, err := pathutil.NormalizePath(fake)
	if err != nil {
		return false, l10n.T("Failed to expand the link path {{.Path}}: {{.Err}}", map[string]any{"Path": fake, "Err": err.Error()}), "PATH_EXPAND_FAIL"
	}

	fakeInfo, err := os.Lstat(expandedFake)
	if err != nil {
		if os.IsNotExist(err) {
			return false, l10n.T("The symbolic link file {{.Path}} does not exist", map[string]any{"Path": fake}), "LINK_MISSING"
		}
		return false, l10n.T("Failed to access the symbolic link file {{.Path}}: {{.Err}}", map[string]any{"Path": fake, "Err": err.Error()}), "LINK_ACCESS_FAIL"
	}

	if fakeInfo.Mode()&os.ModeSymlink == 0 {
		return false, l10n.T("{{.Path}} exists but is not a symbolic link", map[string]any{"Path": fake}), "NOT_SYMLINK"
	}

	target, err := os.Readlink(expandedFake)
	if err != nil {
		return false, l10n.T("Failed to read the target of the symbolic link {{.Path}}: {{.Err}}", map[string]any{"Path": fake, "Err": err.Error()}), "READLINK_FAIL"
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
			return false, l10n.T("The symbolic link target {{.Path}} does not exist", map[string]any{"Path": targetAbs}), "TARGET_MISSING"
		}
		return false, l10n.T("Failed to access the symbolic link target {{.Path}}: {{.Err}}", map[string]any{"Path": targetAbs, "Err": err.Error()}), "TARGET_ACCESS_FAIL"
	}

	expectedInfo, err := os.Stat(expandedReal)
	if err != nil {
		if os.IsNotExist(err) {
			return false, l10n.T("The expected target file {{.Path}} does not exist", map[string]any{"Path": expandedReal}), "EXPECTED_MISSING"
		}
		return false, l10n.T("Failed to access the expected target file {{.Path}}: {{.Err}}", map[string]any{"Path": expandedReal, "Err": err.Error()}), "EXPECTED_ACCESS_FAIL"
	}

	if !os.SameFile(targetInfo, expectedInfo) {
		return false, l10n.T("The file pointed to by the symbolic link {{.Link}} does not match the expected file {{.Real}}", map[string]any{"Link": fake, "Real": real}), "TARGET_MISMATCH"
	}

	return true, "", ""
}

func checkHardlinkValid(prim, seco string) (bool, string, string) {
	expandedPrim, err := pathutil.NormalizePath(prim)
	if err != nil {
		return false, l10n.T("Failed to expand the primary file path {{.Path}}: {{.Err}}", map[string]any{"Path": prim, "Err": err.Error()}), "PATH_EXPAND_FAIL"
	}

	expandedSeco, err := pathutil.NormalizePath(seco)
	if err != nil {
		return false, l10n.T("Failed to expand the hard link path {{.Path}}: {{.Err}}", map[string]any{"Path": seco, "Err": err.Error()}), "PATH_EXPAND_FAIL"
	}

	primInfo, err := os.Stat(expandedPrim)
	if err != nil {
		if os.IsNotExist(err) {
			return false, l10n.T("The primary file {{.Path}} does not exist", map[string]any{"Path": prim}), "PRIM_MISSING"
		}
		return false, l10n.T("Failed to access the primary file {{.Path}}: {{.Err}}", map[string]any{"Path": prim, "Err": err.Error()}), "PRIM_ACCESS_FAIL"
	}

	secoInfo, err := os.Stat(expandedSeco)
	if err != nil {
		if os.IsNotExist(err) {
			return false, l10n.T("The hard link file {{.Path}} does not exist", map[string]any{"Path": seco}), "SECO_MISSING"
		}
		return false, l10n.T("Failed to access the hard link file {{.Path}}: {{.Err}}", map[string]any{"Path": seco, "Err": err.Error()}), "SECO_ACCESS_FAIL"
	}

	if !os.SameFile(primInfo, secoInfo) {
		return false, l10n.T("{{.Seco}} and {{.Prim}} are not hard links to the same file", map[string]any{"Seco": seco, "Prim": prim}), "NOT_SAME_FILE"
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
