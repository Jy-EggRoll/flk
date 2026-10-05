package cmd

import (
	"fmt"
	"io"

	"github.com/jy-eggroll/eggokit/l10n"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jy-eggroll/eggokit/logger"
	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/store"
	"github.com/pterm/pterm"
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
	// 开始检查的 Debug：-vv 下先落一行「已进入检查」，把「检查根本没跑」与「检查跑了但结果为空」区分开
	// 只记动作本身、不记过滤条件：device/type/dir 的过滤口径属于命令行语义，排查时看命令行比看日志更直接
	logger.Debug(l10n.T("Checking links", nil))

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

	// 检查统计：总数与无效数一并入日志，让「链接全部正常」与「有坏链接但被输出吞掉」在日志层面可区分
	// 无效数直接复用 filterCheckResults 的过滤结果，不另外手写一遍遍历，避免与过滤口径分叉
	logger.Info(l10n.T("Check complete", nil),
		"count", len(results),
		"invalid", len(filterCheckResults(results, false)))
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

	// 防御性判空：若 InitStore 失败，全局实例可能为 nil，直接解引用会 panic
	mgr := store.Global()
	if mgr == nil {
		return results, nil
	}

	// Snapshot 返回的是深拷贝，遍历期间 serve 的写路径（POST 覆盖、轮询重载）可以安全地并发替换清单，
	// 不会再出现 map 并发读写 panic；返回值已由 store 包保证非 nil（nil 归一成空表），无需再判空
	data := mgr.Snapshot()

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

// recordFields 定义每种链接类型在 store 中的字段名，按「权威副本 → 派生位置」的顺序排列
//
// 这份表是**字段名的唯一真源**：定位记录（buildRecordEntry）与展示路径（recordDisplayPaths）
// 都从它取值。此前字段名在两处各写一遍（本文件按类型取的 switch，加上 serve_web.go 里为排序
// 另建的一份），且注释写明「两者必须同步修改」——那正是熵增的信号：新增一种链接类型时漏改一处，
// 表现是界面上拼不出路径、或定位不到记录，这类偏差不报错，只会静默地少做一件事
//
// 顺序固定为「权威副本在前」而不是交给调用方遍历 map：map 遍历顺序随机，拼不出稳定的可读路径串
var recordFields = map[string][2]string{
	"symlink":  {"real", "fake"},
	"hardlink": {"prim", "seco"},
	"copy":     {"src", "dst"},
}

// recordValues 按「权威副本、派生位置」的顺序取出检查结果里的两个路径值
//
// 为什么不能直接从 recordFields 取值：output.CheckResult 是结构体而不是映射，
// 「字段名 → 字段值」这一步只能靠显式对应。这里刻意不出现任何字段名字符串，
// 名称统一由 recordFields 提供，两处不会再各自演化
// 未知类型返回两个空串：调用方已先用 recordFields 过滤过类型，走到这里说明结构体与表脱节
func recordValues(result output.CheckResult) (string, string) {
	switch result.Type {
	case "symlink":
		return result.Real, result.Fake
	case "hardlink":
		return result.Prim, result.Seco
	case "copy":
		return result.Src, result.Dst
	}
	return "", ""
}

// recordLogArgs 组装「记录级日志」的公共结构化字段：链接类型、设备名与一对路径
//
// 一对路径按 store 的领域字段顺序给出（权威副本在前、派生位置在后），值来自 recordValues，
// 键名固定为约定的 from / to 而不是按类型动态生成 real/fake 等：同一条记录的日志字段集合
// 保持稳定，日志系统才好做聚合；而「权威 → 派生」正是修复与解除两条链路的实际搬运方向
//
// 抽取理由（熵减）：fix / unlink / serve 三处都要「开始 / 成功 / 失败」各打一条带同样字段的日志，
// 若各拼一份，字段顺序与命名一旦调整就会只改一处、漏改另一处，导致同类日志在不同命令里字段不一致
// 潜在影响点：本函数是纯映射，不读全局状态；未知类型时 recordValues 返回两个空串，
// 此处如实透传空值，不额外造错误分支（真出问题也会由后续的 Unknown type 逻辑报错）
func recordLogArgs(result output.CheckResult) []any {
	from, to := recordValues(result)
	return []any{"type", result.Type, "device", result.Device, "from", from, "to", to}
}

// buildRecordEntry 依据检查结果构造在 store 中定位同一条记录的匹配键
//
// 字段映射必须与 performCheck 从存储里读取字段的方式严格对应：symlink→real/fake、hardlink→prim/seco、copy→src/dst
// 未知类型返回 nil，调用方（removeTrackedRecord）必须把空匹配键视为「不匹配任何记录」并跳过，
// 原因见 removeTrackedRecord 的说明，此处不重复
//
// 抽取理由：fix 与 unlink 原先各自维护一份内容相同的 switch，字段约定一旦调整极容易只改一处、漏改另一处，
// 导致两类命令对「同一条记录」的定位方式悄悄分叉，出现「一个命令删得掉、另一个删不掉」的诡异现象
// 潜在影响点：本函数是纯映射，不读全局状态、不落盘；result 必须来自 performCheck（字段是存储中的原值），
// 否则折叠路径形式不一致会导致 RemoveMatchingEntry 匹配失败而静默无操作
func buildRecordEntry(result output.CheckResult) store.Entry {
	fields, ok := recordFields[result.Type]
	if !ok {
		return nil
	}
	first, second := recordValues(result)
	return store.Entry{fields[0]: first, fields[1]: second}
}

// removeTrackedRecord 从全局存储中移除一条追踪记录，返回值表示「是否真的执行了一次移除」
//
// 集中的三件事：
//  1. 构造匹配键：统一走 buildRecordEntry，fix 与 unlink 不再各写一份 switch
//  2. store 判空：全局实例为 nil（InitStore 失败等极端场景）时安全跳过而不解引用 panic，
//     原先 fix 的删除分支直接使用 mgr 缺少这层保护，与 unlink 的处理不一致，此处顺手补齐
//  3. 空匹配键保护：store.RemoveMatchingEntry 用「遍历匹配键、逐字段比对」的方式找目标，
//     匹配键为空（nil 或零长度）时循环体不执行、match 恒为 true，于是会删掉该类型下的第一条记录——
//     即「删错记录」而不是「什么都不删」。未知类型、字段整体缺失都会走到这条路径，
//     因此这里在调用前拦截，宁可不动存储也不能误删
//
// 落盘为什么不在这里做：两处调用方的落盘粒度与错误上报方式不同
//   - fix：一批删除完成后只落盘一次，并把「Save failed」计入 operationErrors，成功则打印「Deletion complete」
//   - unlink：单条删除时只改内存（unlinkResult 的返回值用于判定「解除失败」），
//     批量结束后由 RunUnlink 的 saveStore 闭包调用 saveTrackedStore 统一落盘并上报「Save failed」
//
// 若在本函数里顺手 Save，unlink 的落盘失败就会被 unlinkResult 当成「解除失败」上报（文案与退出码语义都变了），
// fix 也会从「一批一次落盘」变成「一条一次落盘」；因此落盘单独抽成 saveTrackedStore 共享，
// 「构造 entry → 校验 store → 移除」与「校验 store → 落盘」两条链路仍是同一份实现，没有重复
//
// 返回值：两处调用方都不消费它（重构前也没有消费等价的信号，输出决策取决于落盘是否成功），
// 保留返回值是为了让「store 不可用」「空匹配键」这两种安全跳过在调用方与单测中可观测
func removeTrackedRecord(result output.CheckResult) bool {
	// 防御性判空：全局实例可能因 InitStore 失败而为 nil（Global() 已加锁读取，不再有无保护变量的竞态）
	mgr := store.Global()
	if mgr == nil {
		return false
	}

	// 空匹配键会命中该类型下的第一条记录，必须先拦截（详见函数注释第 3 点）
	entry := buildRecordEntry(result)
	if len(entry) == 0 {
		return false
	}

	mgr.RemoveMatchingEntry(runtime.GOOS, result.Device, result.Type, entry)
	return true
}

// saveTrackedStore 把内存中的存储改动落盘到全局存储路径
//
// 抽取理由：fix 原先内联 mgr.Save(store.StorePath)，unlink 原先用 saveStoreAfterUnlink 包一层，
// 两处都是「判空 + Save」这同一件事；现在两个命令共用本函数，落盘路径只有一个来源
//
// 判空返回 nil（而不是错误）的语义：store 不可用时本来就无从落盘，命令不应因此再报一个保存失败，
// 这与重构前 unlink 的 saveStoreAfterUnlink 行为一致，也是 fix 那处新增保护的落点
// 潜在影响点：本函数只负责写盘，不含任何用户可见输出；「Save failed」文案与 operationErrors 的收集
// 仍由各调用方决定，以保持 fix 与 unlink 各自的既有文案与退出码语义
func saveTrackedStore() error {
	mgr := store.Global()
	if mgr == nil {
		return nil
	}
	return mgr.Save(store.StorePath)
}

// parseSelectionIndices 把交互输入中空格分隔的编号解析成 0 基索引，非法项打印警告后跳过
//
// 抽取理由（组4）：fix 的 `d<number>` 删除分支、fix 的普通修复分支、unlink 的解除分支
// 原先各自内联了同一段循环——判断条件（err != nil || idx < 1 || idx > count）、
// 警告文案（"Invalid number {{.Part}}"）与 idx-1 的转换逐字相同，只有「上限取哪个切片长度」不同；
// 三份实现意味着越界口径或警告文案一旦调整，极可能只改一处，出现「fix 拒绝的编号 unlink 却接受」
// 或「同类非法输入在一个命令里报错、在另一个命令里静默」这类很难被发现的交互不一致
//
// 边界设计（三条独立约束，缺一不可）：
//   - 编号语义面向用户是 1 基序号，减一后返回，调用方可直接拿来做下标访问
//   - input 由调用方负责去掉命令前缀：fix 的 `d<number>` 里 `d` 只表示「删除动作」而不是编号的一部分，
//     本函数只认识「空格分隔的数字串」，不掺入任何 d 前缀语义，避免把删除分支的特例写进公共函数
//   - 非法项（非数字 / 0 / 负数 / 超出 count）只跳过自身并打印一条警告，不影响同一行里其它合法编号；
//     警告条数等于非法项条数、顺序按输入先后，与重构前逐项 continue 的行为逐字一致
//
// 返回值形态：全部非法或空输入时返回 nil 而不是空切片，调用方只做 len 判断并跳过本轮，
// 两种形态在调用点完全等价，保留 `var indices []int` 的零值形态只为让「无有效编号」可被单测固定
//
// errOut 必须非 nil：三处调用点都传 cmd.ErrOrStderr()，警告属于交互诊断信息，
// 必须与业务结果（stdout）分流，测试里传 io.Discard 或 buffer 即可
//
// 潜在影响点：本函数会产生用户可见输出（警告走 errOut），改动文案或越界判断都会直接改变
// fix 与 unlink 的交互行为；警告必须在选中项之前按输入顺序打印，顺序变了用户看到的提示顺序也会变
func parseSelectionIndices(input string, count int, errOut io.Writer) []int {
	var indices []int
	for _, part := range strings.Fields(input) {
		idx, err := strconv.Atoi(part)
		if err != nil || idx < 1 || idx > count {
			pterm.Warning.WithWriter(errOut).Println(l10n.T("Invalid number {{.Part}}", map[string]any{"Part": part}))
			continue
		}
		indices = append(indices, idx-1)
	}
	return indices
}

// filterCheckResults 按有效性过滤检查结果，keepValid 为真时保留有效记录，为假时保留无效记录
//
// 抽取理由（组4）：fix 的 checkAndDisplay 只保留 !result.Valid（待修复项），
// unlink 的 checkAndDisplay 只保留 result.Valid（待解除项），两处循环结构一致、仅过滤方向相反；
// 用布尔参数表达方向，比两处各写一份循环更不易在后续字段调整时只改一侧
//
// 返回值刻意使用 make([]output.CheckResult, 0) 而不是 var 声明：
// 空结果必须是「非 nil 空切片」，因为 JSON 模式下 output.PrintCheckResults / PrintCheckResultsFix
// 会直接序列化这个切片，nil 被编码成 null、空切片才是 []，脚本无法稳定解析；
// 这也正是重构前两处 make(...) 的既有行为，改成 nil 会造成用户可见的 JSON 差异
//
// 潜在影响点：本函数是纯过滤，不读全局状态、不产生任何输出；
// 判定只看 Valid 字段（与重构前一致），无效记录的 Error/ErrorType 即便为空也仍按 Valid 归类
func filterCheckResults(results []output.CheckResult, keepValid bool) []output.CheckResult {
	filtered := make([]output.CheckResult, 0)
	for _, result := range results {
		// 用一个等值比较同时覆盖两个方向：keepValid=true 保留有效，false 保留无效，
		// 与重构前 `if result.Valid` / `if !result.Valid` 的判定完全等价
		if result.Valid == keepValid {
			filtered = append(filtered, result)
		}
	}
	return filtered
}
