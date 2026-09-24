// Command l10n 是 pkg/l10n 配套的文案提取工具，对应 VSCode 的 @vscode/l10n-dev。
//
// 用法：
//
//	go run ./pkg/l10n/cmd/l10n export --pkg <import path> --src <dirs> --dir <locales dir>
//	go run ./pkg/l10n/cmd/l10n check  --pkg <import path> --src <dirs> --dir <locales dir>
//
// 模型见 pkg/l10n 的包注释：**英文源串本身就是消息 id**。因此默认语言的 JSON 是生成物，
// 每次 export 整体覆盖；其余语言的译文文件由人工维护，本工具只做排序与校验，不增删内容。
//
// 三个必填参数刻意不给默认值：它们描述的是"在哪个仓库、扫哪些目录、消息函数在哪"，
// 猜错了的后果是「扫描 0 个文件却通过校验」——最坏的门禁失效形态。宁可让调用方显式写清。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jy-eggroll/flk/pkg/jsonfile"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/jy-eggroll/flk/pkg/l10n/scan"
)

// repoRootMarker 用于从当前目录向上定位仓库根。
// 依赖 cwd 而不向上找根的话，一旦从子目录运行就会扫不到任何文件而静默通过。
const repoRootMarker = "go.mod"

// options 是本工具的运行参数。
type options struct {
	root    string   // 仓库根
	pkg     string   // 消息函数所在包的导入路径
	srcDirs []string // 参与扫描的目录（相对 root）
	dir     string   // 语言文件目录的绝对路径
	def     string   // 默认语言标签
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		usage()
		return
	}
	if cmd != "export" && cmd != "check" {
		usage()
	}

	fs := flag.NewFlagSet("l10n "+cmd, flag.ExitOnError)
	pkg := fs.String("pkg", "", "消息函数所在包的导入路径（必填），如 github.com/jy-eggroll/flk/pkg/l10n")
	src := fs.String("src", "", "参与扫描的目录，逗号分隔（必填），如 cmd,internal")
	dir := fs.String("dir", "", "语言文件所在目录，相对仓库根（必填），如 internal/locales")
	def := fs.String("default", "en", "默认语言标签，其 JSON 是生成物")
	_ = fs.Parse(os.Args[2:])

	if *pkg == "" || *src == "" || *dir == "" {
		usage()
	}

	root, err := findRepoRoot()
	if err != nil {
		fatal(err)
	}
	o := options{
		root:    root,
		pkg:     *pkg,
		srcDirs: splitDirs(*src),
		dir:     filepath.Join(root, *dir),
		def:     *def,
	}

	switch cmd {
	case "export":
		err = cmdExport(o)
	case "check":
		err = cmdCheck(o)
	}
	if err != nil {
		fatal(err)
	}
}

func usage() {
	name := "l10n"
	if len(os.Args) > 0 {
		name = filepath.Base(os.Args[0])
	}
	fmt.Fprintf(os.Stderr, `用法: %s <export|check> --pkg <import path> --src <dirs> --dir <locales dir> [--default <tag>]

命令:
  export   扫描源码提取消息，生成并覆盖默认语言文件，重排译文文件，打印待翻译/待迁移清单
  check    只读校验（语言文件与源码一致、已按字典序排列、可被 go-i18n 正常解析）

参数:
  --pkg      消息函数所在包的导入路径，如 github.com/jy-eggroll/flk/pkg/l10n
  --src      参与扫描的目录，逗号分隔，如 cmd,internal
  --dir      语言文件所在目录，相对仓库根，如 internal/locales
  --default  默认语言标签（默认 en）；其 JSON 是生成物

消息 id 即英文原文字符串，详见 pkg/l10n 的包注释。
`, name)
	os.Exit(2)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误: "+err.Error())
	os.Exit(1)
}

// splitDirs 把逗号分隔的目录参数切成切片，忽略空项与首尾空白。
func splitDirs(s string) []string {
	var out []string
	for _, d := range strings.Split(s, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// scanSource 按参数扫描源码。
func scanSource(o options) (*scan.Result, error) {
	return scan.Scan(scan.Config{Root: o.root, ImportPath: o.pkg, SrcDirs: o.srcDirs})
}

// discoverLanguages 从语言文件目录推导已发布的语言列表，默认语言排首位。
//
// 从目录推导而不是加一个 --langs 参数：语言的真相源就是"目录里有哪些文件"，
// 多一个需要手工同步的参数只会带来漂移。
func discoverLanguages(o options) ([]string, error) {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		if os.IsNotExist(err) {
			// 目录还不存在：首次 export 会创建它
			return []string{o.def}, nil
		}
		return nil, err
	}

	seen := map[string]bool{o.def: true}
	var others []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		tag := strings.TrimSuffix(e.Name(), ".json")
		if seen[tag] {
			continue
		}
		seen[tag] = true
		others = append(others, tag)
	}
	sort.Strings(others)
	return append([]string{o.def}, others...), nil
}

// loadOptions 构造运行期加载语言文件所需的 Options。
// 刻意用 os.DirFS 读工作区文件而不是 embed：工具要校验的正是"磁盘上这份文件能不能加载"。
func loadOptions(o options, langs []string) l10n.Options {
	return l10n.Options{
		Default:   o.def,
		Supported: langs,
		FS:        os.DirFS(o.dir),
	}
}

// cmdExport 重新生成语言文件并打印报告。
func cmdExport(o options) error {
	res, err := scanSource(o)
	if err != nil {
		return err
	}
	if len(res.Messages) == 0 {
		return fmt.Errorf("未扫描到任何消息（已解析 %d 个文件）：--pkg/--src 或识别规则可能已失效", res.Files)
	}

	langs, err := discoverLanguages(o)
	if err != nil {
		return err
	}

	// 默认语言的文件是生成物：内容为 { 源串: 源串 } 的自映射，整体覆盖
	en := make(map[string]string, len(res.Messages))
	for _, m := range res.Messages {
		en[m.Text] = m.Text
	}
	if err := writeFlat(localePath(o, o.def), en); err != nil {
		return err
	}

	// 其余语言是人工维护的译文：只重新排序，不增删任何条目。
	// 先解析成功才写回，解析失败就退出——否则会把译者手改的文件覆盖成损坏内容
	for _, lang := range langs {
		if lang == o.def {
			continue
		}
		p := localePath(o, lang)
		entries, err := readFlat(p)
		if err != nil {
			return err
		}
		if err := writeFlat(p, entries); err != nil {
			return err
		}
		reportTranslations(lang, entries, en)
	}

	reportUnwrapped(res)
	return nil
}

// cmdCheck 只读校验，任一问题都返回错误（退出码 1）。
func cmdCheck(o options) error {
	var problems []string

	res, err := scanSource(o)
	if err != nil {
		// 首参不是字面量之类的结构性问题，本身就是必须修的错误
		return err
	}
	if len(res.Messages) == 0 {
		problems = append(problems, "未扫描到任何消息：--pkg/--src 或识别规则可能已失效")
	}

	langs, err := discoverLanguages(o)
	if err != nil {
		return err
	}

	// 复用运行期加载校验：平铺结构、保留字、消息数一致性都在 l10n.Init 里，
	// 不在这里重复实现一套，避免两处规则漂移
	if err := l10n.Init(o.def, loadOptions(o, langs)); err != nil {
		problems = append(problems, "语言文件无法被 go-i18n 加载："+err.Error())
	}

	enPath := localePath(o, o.def)
	en, err := readFlat(enPath)
	if err != nil {
		return err
	}

	expected := make(map[string]string, len(res.Messages))
	for _, m := range res.Messages {
		expected[m.Text] = m.Text
	}
	for _, m := range res.Messages {
		if _, ok := en[m.Text]; !ok {
			problems = append(problems, fmt.Sprintf("%s.json 缺少消息 %q（%s）", o.def, m.Text, m.Pos))
		}
	}
	for k := range en {
		if _, ok := expected[k]; !ok {
			problems = append(problems, fmt.Sprintf("%s.json 存在源码中已不存在的消息 %q，请运行 export 子命令", o.def, k))
		}
	}

	// 规范形态校验：重新序列化后应与磁盘内容完全一致，否则说明未排序或格式不规范
	if err := checkCanonical(enPath); err != nil {
		problems = append(problems, err.Error())
	}

	for _, lang := range langs {
		if lang == o.def {
			continue
		}
		p := localePath(o, lang)
		entries, err := readFlat(p)
		if err != nil {
			return err
		}
		if err := checkCanonical(p); err != nil {
			problems = append(problems, err.Error())
		}
		for k := range entries {
			if _, ok := en[k]; !ok {
				problems = append(problems, fmt.Sprintf("%s.json 存在失效译文 %q——英文原文已改动或该消息已删除，请同步删除该条目",
					lang, k))
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New(strings.Join(problems, "\n       "))
	}
	// 成功时也输出一行：静默退出容易被误认为"门禁没跑"
	fmt.Printf("✓ 语言文件校验通过：%d 条消息，扫描 %d 个文件\n", len(res.Messages), res.Files)
	return nil
}

// reportTranslations 打印某个语言相对默认语言的翻译缺口。
// 缺失只作警告不阻断：翻译允许滞后于源码。
func reportTranslations(lang string, entries, en map[string]string) {
	var missing []string
	for k := range en {
		if _, ok := entries[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)

	if len(missing) == 0 {
		fmt.Printf("✓ %s：%d 条译文，无缺口\n", lang, len(entries))
		return
	}
	fmt.Printf("! %s：%d 条译文，尚有 %d 条未翻译\n", lang, len(entries), len(missing))
	for _, k := range missing {
		fmt.Printf("    未翻译: %s\n", oneLine(k))
	}
}

// reportUnwrapped 打印尚未迁移的存量文案。
// 这不是错误而是迁移进度指标：迁移完成后应当归零。
func reportUnwrapped(res *scan.Result) {
	if len(res.Unwrapped) == 0 {
		fmt.Printf("✓ 源码中已无非 ASCII 字面量，迁移完成\n")
		return
	}
	byFile := map[string]int{}
	for _, u := range res.Unwrapped {
		byFile[u.Pos.Filename]++
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	fmt.Printf("! 尚有 %d 处未被消息函数包裹的中文文案，分布在 %d 个文件（迁移完成后应归零）:\n",
		len(res.Unwrapped), len(files))
	for _, f := range files {
		fmt.Printf("    %s: %d 处\n", f, byFile[f])
	}
}

// localePath 返回某语言文件的绝对路径。
func localePath(o options, lang string) string {
	return filepath.Join(o.dir, lang+".json")
}

// readFlat 读取一份语言文件为扁平映射。
// 文件不存在时返回空映射（首次 export 会创建它）。
func readFlat(path string) (map[string]string, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s 的 %q 取值不是字符串：语言文件必须是平铺的「源串 -> 译文」对象", path, k)
		}
		out[k] = s
	}
	return out, nil
}

// writeFlat 以规范形态写入语言文件：字典序、2 空格缩进、不做 HTML 转义。
// 规范形态由 pkg/jsonfile 提供，与配置文件写入共用同一份策略。
func writeFlat(path string, entries map[string]string) error {
	buf, err := jsonfile.Marshal(entries)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, buf, 0644)
}

// checkCanonical 校验文件已是规范形态（排序正确、缩进统一）。
func checkCanonical(path string) error {
	onDisk, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	entries, err := readFlat(path)
	if err != nil {
		return err
	}
	want, err := jsonfile.Marshal(entries)
	if err != nil {
		return err
	}
	if !bytes.Equal(onDisk, want) {
		return fmt.Errorf("%s 未按字典序排列或格式不规范，请运行 export 子命令", path)
	}
	return nil
}

// findRepoRoot 从当前目录向上查找含 go.mod 的目录。
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, repoRootMarker)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("未找到 %s，请在本仓库内运行", repoRootMarker)
		}
		dir = parent
	}
}

// oneLine 把多行消息压成单行，便于在报告里显示。
func oneLine(s string) string {
	return strings.ReplaceAll(s, "\n", " ⏎ ")
}
