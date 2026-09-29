package pathutil

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var workDir string

// SetWorkDir 设置工作目录，用于相对路径解析
func SetWorkDir(dir string) {
	workDir = dir
}

type ExistsButNotDirectoryError struct {
	Path string
}

func (e *ExistsButNotDirectoryError) Error() string {
	return l10n.T("Path {{.Path}} exists but is not a directory; with --force the existing file will be deleted and replaced with an intermediate directory", map[string]any{"Path": e.Path})
}

func (e *ExistsButNotDirectoryError) Is(target error) bool {
	_, ok := target.(*ExistsButNotDirectoryError)
	if !ok {
		return false
	}
	return true
}

// FoldHome 函数，接收原始路径字符串，返回将用户主目录替换为~的简化路径
// 注意：折叠必须按「路径段」边界判断，不能简单用 strings.HasPrefix(normPath, home)
// 反例：home=/root 时，/rootother/config 会被误判为以家目录为前缀，错误折叠成 ~other/config
// 因此仅当 normPath 恰等于 home，或 normPath 以 home+分隔符 开头时才折叠
func FoldHome(path string) (string, error) { // 定义 fold

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// 之前此处用 normPath, _ := NormalizePath(path) 吞掉了错误，导致非法 ~ 前缀等错误被静默
	// 现在显式向上传播错误，避免得到一个不可信的路径继续参与折叠
	normPath, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	// normPath 恰好就是家目录本身，直接折叠为 ~
	if normPath == home {
		return "~", nil
	}
	// 仅当以 home + 路径分隔符 为前缀时才折叠，保证是家目录的真正子项而非同名前缀目录
	if strings.HasPrefix(normPath, home+string(os.PathSeparator)) {
		return "~" + normPath[len(home):], nil // 保留分隔符及其后内容，拼接到 ~ 之后
	}
	return normPath, nil
}

// ExpandHome ，接收字符串类型的路径参数，返回处理后的路径字符串和错误对象
func ExpandHome(path string) (string, error) {
	// 如果路径不以 ~ 开头，直接返回
	if !strings.HasPrefix(path, "~") { // 判断输入的路径字符串是否不以波浪号(~)开头，strings.HasPrefix 用于检测字符串前缀
		return path, nil // 若路径不以~开头，直接返回原路径和 nil（表示无错误）
	}

	// 获取用户主目录
	home, err := os.UserHomeDir() // 调用 os 包的 UserHomeDir 函数获取当前用户的主目录路径，返回主目录字符串和错误对象
	if err != nil {               // 判断获取用户主目录的操作是否产生错误
		return "", err // 若获取主目录出错，返回空字符串和该错误对象
	}

	// 如果只是 ~，直接返回主目录
	if path == "~" { // 判断输入的路径是否严格等于单个波浪号（~）
		return home, nil // 若路径仅为 ~，返回获取到的用户主目录和 nil（表示无错误）
	}

	// 如果是 ~/... 格式，拼接路径
	// filepath.Join 自动处理不同操作系统的路径分隔符，但是不会将路径清理到最简形态
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") { // 判断路径是否以~/（Unix/Linux/Mac 系统）或~\（Windows 系统）开头
		return filepath.Join(home, path[2:]), nil // 使用 filepath.Join 拼接主目录和~后的路径（path[2:]截取从索引 2 开始的子串，去掉~和分隔符），返回拼接后的路径和 nil（表示无错误）
	}

	// 若以上条件都不满足（如~后接非分隔符的情况，例如 "~foo"），属于非法路径前缀，必须返回明确错误
	// 之前此处返回 ("", nil)，会静默得到空路径并被后续逻辑当作当前目录使用，属于严重隐患，故改为显式报错
	return "", fmt.Errorf("%s", l10n.T("Invalid ~ path prefix: {{.Path}}", map[string]any{"Path": path}))
}

func NormalizePath(path string) (string, error) { // 定义 NormalizePath 函数，接收字符串类型的路径参数，返回规范化后的路径字符串和错误对象
	expanded, err := ExpandHome(path) // 调用 ExpandHome 函数展开路径中的波浪号（~），接收展开后的路径和错误对象
	if err != nil {                   // 判断展开波浪号的操作是否产生错误
		return "", err // 若展开波浪号出错，返回空字符串和该错误对象
	}

	// 如果路径不是绝对路径，且设置了工作目录，则相对于工作目录
	if !filepath.IsAbs(expanded) && workDir != "" {
		expanded = filepath.Join(workDir, expanded)
	}

	cleaned := filepath.Clean(expanded) // 调用 filepath.Clean 函数清理展开后的路径，解析路径中的.和..、合并冗余分隔符，生成最简路径

	return cleaned, nil // 返回清理后的规范化路径和 nil
}

func ToAbsolute(normalizePath string) (string, error) {
	absPath, err := filepath.Abs(normalizePath)
	if err != nil {
		return "", err
	}
	return absPath, nil
}

// IsProtectedPath 判断 path 是否命中两个「绝不允许作为整体删除或移动目标」的路径之一：
// 根目录本身（Unix 的 /、Windows 的 C:\ 等盘符根）与当前用户家目录本身
//
// 为什么放在 pathutil 而不是删除侧：删除口（internal/safeop）与备份口
// （internal/create/shared 的 pathutil.Copy）面对的是同一个问题——把一个巨大的、语义上
// 不该整体挪动的目录当成普通目标。判断口径属于「路径语义」，归属地就是本包，因此在此处
// 实现一次、由各调用方复用，避免同一个围栏在两个包里各写一份再逐渐跑偏
//
// 判断口径（刻意保持极简，不引入任何黑名单、配置文件或可扩展规则表）：
//   - 先 filepath.Abs 再 filepath.Clean，让 "./"、"~/" 末尾斜杠、"a/../b" 这类写法
//     归一化到同一形式后再比较，否则尾部斜杠就能绕过围栏
//   - 根目录用跨平台通式 filepath.Dir(abs) == abs 判定："/" 与 "C:\" 的父目录都是它自己，
//     因此不必按 GOOS 分支，也不必硬编码任何路径字符串
//   - 家目录由 os.UserHomeDir 现场取得；Windows 文件系统大小写不敏感，
//     C:\Users\Foo 与 c:\users\foo 是同一个目录，故用 strings.EqualFold；Unix 区分大小写，
//     若也忽略大小写会把 /home/User 这种「家目录的同名不同目录」误判为家目录
//
// 刻意不做的两件事：
//   - 不解析符号链接（不用 filepath.EvalSymlinks）。os.RemoveAll 对符号链接只删除链接自身、
//     不跟随到目标，所以删掉一个指向 / 的链接本身就是无害的；反过来展开链接会把
//     「链接位于临时目录、目标恰好是家目录」这种无害场景误判成危险目标
//   - 不保护家目录的子项（~/.ssh、~/.config、~/.bashrc 等一律返回 false），
//     flk 的用户本来就要能自由删除家目录内的内容，围栏只挡「家目录本身整体消失」
//
// 无副作用：只读文件系统之外的输入（调用 filepath.Abs 与 os.UserHomeDir），不触碰任何文件；
// filepath.Abs 或 os.UserHomeDir 出错时返回 false，即「不作保护性拦截」——此时路径是否安全
// 本就无从判断，交由调用方按既有错误路径处理，好过在这里吞掉错误或伪造一个安全结论
func IsProtectedPath(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)

	// 根目录本身：父目录等于自己（"/" -> "/"，"C:\" -> "C:\"）
	if filepath.Dir(abs) == abs {
		return true
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	// 家目录同样需要 Clean：某些平台或环境变量下可能带尾部斜杠，不归一化会漏判
	home = filepath.Clean(home)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(abs, home)
	}
	return abs == home
}

// SamePath 判断两个路径在归一化后是否指向同一位置
//
// 为什么放在 pathutil：与 IsProtectedPath、IsSubPath 一样，这是纯粹的「路径语义」，不涉及任何业务；
// 放在这里可以保证全项目只有一份大小写与归一化口径，调用方（备份口）不必自己再写一遍
// filepath.Abs + filepath.Clean + Windows 大小写判断，避免同一份口径在两个包里各写一份后逐渐跑偏
//
// 判断口径：
//   - 先 filepath.Abs 再 filepath.Clean，让 "./x"、"x/"、"x/../x" 这类写法归一化到同一形式后再比较，
//     否则同一个文件用尾部分隔符或 ".." 绕一圈就能被判成「不同路径」
//   - Windows 文件系统大小写不敏感，C:\Foo 与 c:\foo 是同一个位置，故用 strings.EqualFold；
//     Unix 区分大小写，忽略大小写会把 /home/User 这种「同名不同目录」误判为同一路径
//
// 刻意不做的两件事（与同包 IsProtectedPath 的口径一致，理由见该函数注释）：
//   - 不解析符号链接（不用 filepath.EvalSymlinks）。展开链接会把「链接位于临时目录、指向真实数据」
//     这种合法场景误判成同一路径，而调用方（备份口）本来也不跟随链接
//   - 不在这里展开 ~ 前缀：与 IsProtectedPath 一致，~ 的展开由 NormalizePath/ExpandHome 负责，
//     调用方（cmd 层）在进入本函数前已完成归一化
//
// 潜在影响点：两个都取不到绝对路径（理论上只在极端环境下发生）时返回 false，即「不判定为同一路径」——
// 与 IsProtectedPath 出错即放行的口径一致：此时无法得出可信结论，交由调用方按既有错误路径处理，
// 好过在这里吞掉错误或伪造一个结论
//
// 无副作用：只读取调用方传入的字符串并访问进程当前工作目录，不触碰任何文件
func SamePath(a, b string) bool {
	aAbs, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	bAbs, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	aAbs = filepath.Clean(aAbs)
	bAbs = filepath.Clean(bAbs)

	if runtime.GOOS == "windows" {
		return strings.EqualFold(aAbs, bAbs)
	}
	return aAbs == bAbs
}

// IsSubPath 判断 child 是否位于 parent 内部（严格后代，不含 parent 自身）
//
// 这解决的是「复制时把自己装进自己」这一类灾难点：当 child 是 parent 内部的目录时，
// 把 child 复制到 parent 会让源目录在复制过程中不断长大，形成自引用无限递归直到磁盘写满
// （典型触发是 flk create symlink -r ~/repo/sub -f ~/repo）
//
// 为什么放在 pathutil：判断口径属于「路径语义」，与 IsProtectedPath 是同一族问题——两者都是
// 「两个路径之间的关系是否允许本次操作」。归属地在此，调用方（备份口）只负责把结论包装成错误，
// 不重复实现一遍字符串比较
//
// 判断口径：
//   - 先 filepath.Abs 再 filepath.Clean，让 "./"、尾部斜杠、"a/../b" 这类写法归一化后再判断，
//     否则 "parent/sub/.." 之类的等价写法就能绕过围栏
//   - 用 filepath.Rel 按「路径段」判断，禁止用字符串前缀比较：前缀比较会把 /a/bc 误判为位于 /a/b 内部
//     （这是本函数最关键的实现约束），同理相对结果里的 "..foo" 这种合法目录名也不能用 HasPrefix(rel, "..")
//     判断，必须要求它恰好是 ".." 或以 ".." + 分隔符 开头
//   - parent 与 child 为同一路径时返回 false（严格后代不含自身）；同一路径是另一种危险形态，
//     由调用方用 SamePath 单独判断，不在本函数里混为一谈
//   - Windows 文件系统大小写不敏感，与 IsProtectedPath、SamePath 采用同一口径（SamePath 内部已处理），
//     因此不会出现 /Foo/bar 与 /foo 在 Windows 上被判成「无关路径」的漏洞
//
// 刻意不做的一件事：不解析符号链接（不用 filepath.EvalSymlinks），与同包 IsProtectedPath 的口径一致，
// 理由同该函数注释：备份/删除口对符号链接本身操作而不跟随到目标，展开链接会误伤
// 「链接位于临时目录、指向真实数据」的合法场景
//
// 潜在影响点：本函数只做纯字符串与结构判断，不检查路径是否真实存在，也不解析 ~ 前缀
// （~ 的展开由 NormalizePath/ExpandHome 负责，与 IsProtectedPath 一致）。
// 取不到绝对路径或 filepath.Rel 报错时返回 false，即「不判定为子路径」并交回调用方处理，
// 与 IsProtectedPath 出错即放行的口径保持一致
//
// 无副作用：不触碰任何文件，只访问进程当前工作目录用于相对路径解析
func IsSubPath(parent, child string) bool {
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	parentAbs = filepath.Clean(parentAbs)
	childAbs = filepath.Clean(childAbs)

	// 同一路径不是「严格后代」：否则 IsSubPath 会与 SamePath 的语义重叠，调用方也就无法把
	// 「source 与 target 相同」和「source 在 target 内部」区分成两种不同的错误文案
	if SamePath(parentAbs, childAbs) {
		return false
	}

	// Rel 是本函数唯一可信的「按路径段」比较方式：它返回从 parent 到 child 的相对路径，
	// 同盘路径必能算出结果，前缀相似的兄弟目录（/a/b 与 /a/bc）会得到 "../bc" 这类结果而不会误判
	rel, err := filepath.Rel(parentAbs, childAbs)
	if err != nil {
		return false
	}

	// rel 为 "." 表示同一路径（SamePath 已拦下，这里是跨平台大小写差异下的兜底）；
	// rel 恰好为 ".." 或以 ".." + 分隔符 开头，说明 child 在 parent 之外
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}

	// Rel 在两侧盘符不同（Windows）时会报错而不是返回绝对路径，这里再兜一层，
	// 保证任何情况下都不会把「不在内部」的路径判成子路径
	if filepath.IsAbs(rel) {
		return false
	}

	return true
}

// ErrCopyPathOverlap 表示一次复制请求的「源与目标关系」非法——源与目标是同一路径，
// 或目标位于源内部（任意深度），复制已在触碰文件系统之前被围栏拦下
//
// 为什么哨兵落在 pathutil（本包）而不是某个业务包：
//   - 这是「路径工具自身的正确性」问题，与业务无关。任何调用方无论出于什么目的把目标放进源里，
//     复制都会边写边把目标读回源中：CopyDir 先 MkdirAll 建出目标，紧接着 ReadDir(源) 就会读到
//     自己刚建出来的目标并递归下钻，每层多一级同名子目录，直到路径超长报错或磁盘写满；
//     源与目标同一路径时更直接——CopyFile 用 os.Create(目标) 先把文件截断，随后从已被截断的
//     同一文件读取，文件被清零。损害不可逆，事后报错毫无意义
//   - 判断口径（SamePath/IsSubPath）本就沉淀在本包，围栏放在同处可保证「一份口径、一处拒绝」，
//     而不是每个调用点各写一遍 fileName 比较再逐渐跑偏
//
// 与 safeop.ErrProtectedPath 的区别（两者必须并存，不可合并）：
//   - ErrProtectedPath 说的是「某个路径本身不允许作为操作对象」（根目录本身或家目录本身），
//     只涉及一个路径，与另一个路径无关
//   - ErrCopyPathOverlap 说的是「两个路径之间的相对关系不允许」，单独看两个路径可能都平平无奇，
//     危险完全来自它们的相对位置。合并会让调用方再也无法区分「用户给了一个荒谬的路径」
//     与「用户给了两个位置关系互相矛盾的路径」，日后想分别调整文案或把其中一种降级为警告就会互相牵连
//
// 与备份口原先的 shared.ErrBackupPathOverlap 是同一件事（本次合并，旧哨兵已删除）：
// 备份口调用的就是 pathutil.Copy(target, source)，它拦下的「source 位于 target 内部，或两者相同」
// 换算到 Copy 的视角恰好是「目标位于源内部，或源与目标相同」——同一对路径、同一个谓词、
// 同一种损害机制。两个哨兵并存只会让调用方在 errors.Is 时多写一遍判断，调整口径时必须同时改两处，
// 因此按「同一件事只保留一处」合并到本包，备份口、unlink、fix 全部复用这一个
//
// 与 ErrOperationCancelled、ErrProtectedPath 一样，它表示一次「有意的安全拒绝」而非执行失败，
// 调用方用 errors.Is 判定，判定逻辑与面向用户的文案互不耦合
var ErrCopyPathOverlap = errors.New("copy path overlap")

// copyPathOverlapKind 区分两种「源目标重叠」形态，只用于选择文案，不影响 errors.Is 的判定结果
//
// 刻意用命名常量而不是 bool 参数：调用点写成 newCopySelfPathError/newCopySubPathError 后，
// 一眼就能看出拒绝的是哪种形态，不必回头查 true 到底代表「相同」还是「包含」
type copyPathOverlapKind int

const (
	// copyPathOverlapSelf：源与目标是同一路径
	copyPathOverlapSelf copyPathOverlapKind = iota
	// copyPathOverlapInner：目标位于源内部
	copyPathOverlapInner
)

// copyPathOverlapError 承载被拒绝的两个路径与重叠形态，并把面向用户的文案交给 l10n
//
// 与 safeop.protectedPathError 同样的做法：不写成 fmt.Errorf("%w: %s", ErrCopyPathOverlap, 文案)，
// 避免 err.Error() 里出现 "copy path overlap: ..." 这种多余的英文前缀——
// 命令层的 failure() 会把它原样呈现给用户，与「所有用户可见文案都走 l10n」的要求不符
type copyPathOverlapError struct {
	kind copyPathOverlapKind // 重叠形态，决定使用哪条 l10n 文案
	src  string              // 复制的源，已归一化为绝对路径
	dst  string              // 复制的目标，已归一化为绝对路径
}

func (e *copyPathOverlapError) Error() string {
	switch e.kind {
	case copyPathOverlapSelf:
		// 同一路径时只给一个路径即可（两个值本来就相等），少一个占位符也少一处可能的困惑
		return l10n.T("Refusing to copy {{.Dst}} onto itself: the source and destination paths are the same", map[string]any{"Dst": e.dst})
	default:
		// 文案里必须同时给出两个路径，且顺序必须与真实的复制方向一致——复制是 src → dst，
		// 所以只能写成 "copy {{.Src}} into {{.Dst}}"，绝不能反过来：
		// 命中的形态是「dst 位于 src 内部」，用户看到「把源复制进它自己的子目录」才能立刻明白
		// 自己把目标指到源里面去了；写反会得到 "copy <目标> into <源>" 这种与事实相反的方向描述，
		// 让用户以为复制是从目标往源里做，从而改错命令（这是本函数修正过一次的真实缺陷）
		//
		// 只说「路径不合法」而不指出谁在谁里面、朝哪个方向复制，用户同样无法据此改正命令
		return l10n.T("Refusing to copy {{.Src}} into {{.Dst}}: the destination path is inside the source path, so copying would keep growing into itself", map[string]any{"Src": e.src, "Dst": e.dst})
	}
}

// Is 让 errors.Is(err, ErrCopyPathOverlap) 成立
// 判定用的哨兵与展示用的文案因此互不耦合：以后调整措辞或新增语言都不会影响任何调用方的判定
func (e *copyPathOverlapError) Is(target error) bool {
	return target == ErrCopyPathOverlap
}

// displayCopyPath 把展示用路径统一归一化为绝对路径，让 "~/sub/" 之类写法不会原样出现在错误信息里
//
// 取不到绝对路径时回退到原路径：调用方既然已经判定命中重叠形态，说明 SamePath/IsSubPath 内部
// 成功取到过绝对路径（取不到会直接放行），因此这里不会影响拦截结论
func displayCopyPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return path
}

// newCopySelfPathError 构造「源与目标是同一路径」的错误
func newCopySelfPathError(path string) error {
	display := displayCopyPath(path)
	return &copyPathOverlapError{kind: copyPathOverlapSelf, src: display, dst: display}
}

// newCopySubPathError 构造「目标位于源内部」的错误
func newCopySubPathError(src, dst string) error {
	return &copyPathOverlapError{kind: copyPathOverlapInner, src: displayCopyPath(src), dst: displayCopyPath(dst)}
}

// CheckCopyPaths 判断一次复制请求的源与目标关系是否合法：命中非法关系时返回可被
// errors.Is(err, ErrCopyPathOverlap) 识别的错误，合法时返回 nil
//
// 为什么导出而不是只藏在 Copy 里：Copy 内部的守卫能保证「不会真的复制」，但拦不住
// 「调用方已经先改了状态」——cmd/unlink.go 是先 safeop.Delete(derived) 再 Copy，等 Copy 报错时
// 派生位置上的链接/数据已经删除；cmd/fix.go 的回填同样希望在任何文件系统变更之前失败。
// 调用方在动手之前调用本函数，即可得到与底层守卫完全一致的口径与文案，
// 不必自己再拼一遍 SamePath/IsSubPath（那正是「同一份口径写两遍」的典型来源）
//
// 参数顺序与 Copy 一致（src 在前、dst 在后），刻意不提供 (dst, src) 变体：
// 项目里 CopyFile(dst, src) 与 CopyDir(src, dst) 顺序相反已经是个历史坑，
// 新增 API 一律对齐对外的 Copy，避免又多一个需要记住的方向
//
// 命中口径（两条，都只做纯字符串判断，既不检查路径是否存在、也不解析符号链接）：
//   - SamePath(src, dst)：源与目标同一路径。CopyFile 会用 os.Create(dst) 先截断目标再读源，
//     同一文件时等于把文件清零；CopyDir 则会逐项覆盖自身
//   - IsSubPath(src, dst)：目标位于源内部（严格后代，不含相等）。CopyDir 会先把目标目录建出来，
//     随后 ReadDir(src) 读到自己刚建的目标并递归下钻，形成自引用无限递归
//
// 潜在影响点：目标位于源之外、或目标恰为源的父目录一律放行——那是正常用法
// （把一份数据并入上层目录做备份/归并），围栏绝不能拦，这正是前两处「方向相反」的
// 备份用法能继续工作的前提
//
// 无副作用：只读取两个字符串并访问进程当前工作目录，不触碰任何文件
func CheckCopyPaths(src, dst string) error {
	if SamePath(src, dst) {
		return newCopySelfPathError(dst)
	}
	if IsSubPath(src, dst) {
		return newCopySubPathError(src, dst)
	}
	return nil
}

// EnsureDirExists 确保目录存在，如果不存在则创建
func EnsureDirExists(path string) error {
	// 获取目录路径（如果 path 是文件路径，则获取其父目录）
	dir := filepath.Dir(path)

	// 检查目录是否已存在
	// 使用 Lstat，这样可以检测到符号链接本身（不跟随符号链接）
	info, err := os.Lstat(dir)
	if err == nil {
		// 路径存在，检查是否为目录或符号链接
		// 如果是符号链接或存在但不是目录，返回特殊错误，调用方在 --force 模式下会处理它
		if info.Mode()&os.ModeSymlink != 0 {
			return &ExistsButNotDirectoryError{Path: dir}
		}
		if !info.IsDir() {
			return &ExistsButNotDirectoryError{Path: dir}
		}
		return nil
	}

	// 目录不存在，创建目录（包括所有必要的父目录）
	// 0755 权限：所有者可读写执行，组和其他用户可读执行，属于泛用权限
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return nil
}

// FileHash 计算文件内容的 sha256 十六进制摘要，用于 copy 记录的内容一致性校验
// 采用流式读取，避免一次性把大文件读进内存
func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CopyFile 复制单个文件，保留权限
func CopyFile(dst, src string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	from, err := os.Open(src)
	if err != nil {
		return err
	}
	defer from.Close()

	to, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer to.Close()

	_, err = io.Copy(to, from)
	if err != nil {
		return err
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	return os.Chmod(dst, srcInfo.Mode())
}

// CopyDir 递归复制目录，处理符号链接
func CopyDir(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	}

	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := CopyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, dstPath); err != nil {
				if !errors.Is(err, os.ErrExist) {
					return err
				}
				if err := os.Remove(dstPath); err != nil {
					return err
				}
				if err := os.Symlink(link, dstPath); err != nil {
					return err
				}
			}
		} else {
			if err := CopyFile(dstPath, srcPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// Copy 智能复制（文件或目录），处理符号链接
//
// 参数顺序是 src 在前、dst 在后（与 CopyDir 一致，与 CopyFile 相反，后者是历史遗留的不一致）
//
// 入口守卫（本次新增）：命中「源与目标相同」或「目标位于源内部」时直接返回
// ErrCopyPathOverlap，不做任何文件系统变更。守卫放在本函数的第一行，先于下面的 os.Lstat，
// 保证「拒绝」这件事完全不依赖文件系统状态（例如源不存在时也不会先报 Stat 错误而掩盖了
// 真正的路径关系问题），也与 cmd 层调用 CheckCopyPaths 的提前判断共用同一份口径与文案
//
// 潜在影响点：守卫只拦「目标在源里面」这一个方向，反向（目标在源外面、或目标恰为源的父目录）
// 一律放行。备份口、unlink、fix 里都有合法的反向复制，绝不能顺手扩大拦截范围
func Copy(src, dst string) error {
	if err := CheckCopyPaths(src, dst); err != nil {
		return err
	}

	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	}

	if info.IsDir() {
		return CopyDir(src, dst)
	}
	return CopyFile(dst, src)
}
