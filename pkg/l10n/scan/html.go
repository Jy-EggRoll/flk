package scan

import (
	"fmt"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jy-eggroll/flk/pkg/l10n"
)

// 本文件实现 HTML 页面内嵌翻译表的提取，是 scan 包在 Go 源码之外的第二个消息来源。
//
// 前因：WebUI 页面是无构建步骤的单文件，为了让页面文案也能随语言切换，它自带一份
// 内嵌翻译表，形如
//
//	var MSG = { 'zh-CN': { 'Save': '保存' } };
//
// 由后端注入的 window.__FLK_LANG__ 选语言，查不到译文就回退英文源串。这份表在
// .go 文件里看不见，而早先的提取器只扫 .go，于是页面文案漏译、译文随 key 改动而失效
// 都不会被门禁拦住，只会静默回退英文——本文件的全部工作就是把这份表纳入提取与校验。
//
// 提取规则刻意专一，宁可漏报也不误报：
//
//   - 消息只有一个来源：**MSG 翻译表里带引号的 key**。它天然满足"英文源串即消息 id"
//     的模型，和 Go 侧的 l10n.T("...") 地位完全相同，因此可以与 Go 消息直接合并
//   - 表外的一切都不算文案：正文文本、标签属性值、CSS 全是界面骨架或实现细节，
//     既没有稳定的 id 语义，也不该翻译
//   - 表的形态必须是两层（语言 -> 源串 -> 译文）。形态不符直接报错而不是靠猜，
//     否则"猜错了却静默通过"就是最坏的门禁失效
//
// 未包裹文案（迁移进度指标，对应 Go 侧的 Unwrapped）另行统计：正文文本节点、标签
// 属性值与 <script> 里的字符串字面量中含非 ASCII 字母者。注释与 CSS 不算——它们是
// 开发说明和排版，把中文注释当成待翻译文案会直接污染这个指标。

// HTMLMessage 是 HTML 内嵌翻译表里的一条消息。
type HTMLMessage struct {
	// Text 是消息 id，即翻译表的 key（英文源串）
	Text string
	// Pos 是该 key 在 HTML 文件中的位置，便于在报告里直接点击跳转
	Pos token.Position
	// Langs 是这条消息出现在哪些语言段里；缺某语言即该语言下这条文案会回退英文
	Langs []string
}

// HTMLTable 是一个 HTML 文件里内嵌的翻译表。
type HTMLTable struct {
	// Pos 是 MSG 表在文件中的起始位置（相对仓库根）
	Pos token.Position
	// Langs 是表中出现的语言标签，按出现顺序排列
	Langs []string
	// Msgs 是表中提取到的消息，按 id 去重并排序
	Msgs []HTMLMessage
}

// msgTableRe 匹配内嵌翻译表的声明。
//
// 刻意要求 var/let/const 的声明形式：只有"声明一个新表"才是表的定义点，
// 把 MSG 当普通变量引用的地方（如赋值、传参）不该被当成表的定义。
// 一旦页面改了声明写法，这里会一条都匹配不到，而"扫不到表"会被当成错误报出来，
// 不会静默放过——这正是需要显式声明形式的前提。
var msgTableRe = regexp.MustCompile(`\b(?:var|let|const)\s+MSG\s*=`)

// htmlFiles 列出参与扫描的 HTML 文件。
//
// 与 goFiles 的差别：Go 侧只按目录收集，HTML 侧允许直接点名文件——WebUI 常常只有
// 一两个页面，逐页点名比让整个目录都被扫更明确。目录则递归收集 .html。
// 路径不存在时跳过（仓库未必同时拥有全部目录，缺失不是错误），
// "一个文件都没扫到"由调用方拦下，避免门禁静默失效。
func htmlFiles(root string, srcs []string) ([]string, error) {
	var out []string
	for _, src := range srcs {
		base := filepath.Join(root, src)
		info, err := os.Stat(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			// 显式点名的文件不看后缀：调用方已经指名道姓，跳过反而让人困惑；
			// 但非 HTML 文件里不会有 MSG 表，会在提取阶段报出来
			out = append(out, base)
			continue
		}
		err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".html") {
				return nil
			}
			out = append(out, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return dedupeSorted(out), nil
}

// dedupeSorted 去掉已排序切片里的重复项。
// 调用方可能同时点名了文件与它所在的目录，重复扫描会让消息条数翻倍。
func dedupeSorted(in []string) []string {
	out := in[:0]
	for i, p := range in {
		if i > 0 && p == in[i-1] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// htmlSource 是一份 HTML 的内容与行索引，用于把字节偏移换算成行列位置。
type htmlSource struct {
	name  string
	text  string
	lines []int // 每行起始字节偏移
}

// newHTMLSource 建立行索引。只算一次，后续每个偏移都是二分查找。
func newHTMLSource(name, text string) *htmlSource {
	lines := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lines = append(lines, i+1)
		}
	}
	return &htmlSource{name: name, text: text, lines: lines}
}

// position 把字节偏移换算成位置。列号不填：报告里只需要"点得过去"，行号足够。
func (s *htmlSource) position(off int) token.Position {
	lo, hi := 0, len(s.lines)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if s.lines[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return token.Position{Filename: s.name, Line: lo + 1, Offset: off}
}

// scanHTMLFile 读取并解析一个 HTML 文件，返回它的内嵌翻译表与未包裹文案。
func scanHTMLFile(root, path string) (*HTMLTable, []Literal, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读取 %s 失败: %w", rel(root, path), err)
	}
	src := newHTMLSource(rel(root, path), string(buf))

	table, ranges, err := collectHTMLTable(src)
	if err != nil {
		return nil, nil, err
	}
	return table, collectHTMLUnwrapped(src, ranges), nil
}

// collectHTMLTable 从 HTML 里提取内嵌翻译表。
//
// 返回的 ranges 是翻译表占用的字节区间：未包裹文案的扫描要跳过它，
// 否则表里的中文译文会被当成"尚未迁移的中文文案"重复计数。
func collectHTMLTable(src *htmlSource) (*HTMLTable, [][2]int, error) {
	matches := msgTableRe.FindAllStringIndex(src.text, -1)
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf(`%s: 未找到内嵌翻译表（期望形如 var MSG = { '<语言>': { '<英文源串>': '<译文>' } }）——`+
			`页面若改用别的方式承载译文，请同步调整提取规则，不要让门禁悄悄失去覆盖`, src.name)
	}

	table := &HTMLTable{Pos: src.position(matches[0][0])}
	langIndex := map[string]int{}
	msgIndex := map[string]int{}
	var ranges [][2]int

	for _, m := range matches {
		sc := &jsScanner{src: src, i: m[1]}
		sc.skipTrivia()
		obj, err := sc.parseObject()
		if err != nil {
			return nil, nil, err
		}
		ranges = append(ranges, [2]int{obj.off, obj.end})

		for _, section := range obj.members {
			if section.val == nil {
				return nil, nil, fmt.Errorf("%s: 翻译表里的语言 %q 取值必须是对象字面量（该表是「语言 -> 英文源串 -> 译文」两层结构）",
					src.position(section.keyOff), section.key)
			}
			lang := section.key
			if _, ok := langIndex[lang]; !ok {
				langIndex[lang] = len(table.Langs)
				table.Langs = append(table.Langs, lang)
			}
			for _, msg := range section.val.members {
				if msg.val != nil {
					return nil, nil, fmt.Errorf("%s: %q 的译文必须是字符串字面量，实得对象字面量",
						src.position(msg.keyOff), msg.key)
				}
				if err := validateHTMLMessageID(msg.key, src.position(msg.keyOff).String()); err != nil {
					return nil, nil, err
				}
				idx, ok := msgIndex[msg.key]
				if !ok {
					idx = len(table.Msgs)
					msgIndex[msg.key] = idx
					table.Msgs = append(table.Msgs, HTMLMessage{Text: msg.key, Pos: src.position(msg.keyOff)})
				}
				table.Msgs[idx].Langs = append(table.Msgs[idx].Langs, lang)
			}
		}
	}

	// 排序让报告与测试的断言不受页面里条目顺序的影响
	sort.Slice(table.Msgs, func(i, j int) bool { return table.Msgs[i].Text < table.Msgs[j].Text })
	for i := range table.Msgs {
		sort.Strings(table.Msgs[i].Langs)
	}
	sort.Slice(ranges, func(a, b int) bool { return ranges[a][0] < ranges[b][0] })
	return table, ranges, nil
}

// validateHTMLMessageID 校验一条 HTML 消息能否安全地用作 id。
//
// 与 Go 侧的 validateMessage 共用"非空 + 不撞 go-i18n 保留字"两条判断，但**刻意不查**
// 制表符与首尾空白：那两条在 Go 侧存在的原因是"id 就是源码里的字面量，源码缩进变化会让 id 漂移"；
// HTML 的 key 是引号内的字符串，不存在格式漂移，而且页面里确实有 ' | Size: {size}' 这种
// 以空格开头的拼接片段（见 config.html 的 MSG 表），照搬会误报。
//
// 额外加一条 Go 侧没有的约束：id 不得含非 ASCII 字母。原因见下。
func validateHTMLMessageID(text, pos string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s: 内嵌翻译表里出现了空 key", pos)
	}
	if err := l10n.ValidateMessageID(text); err != nil {
		return fmt.Errorf("%s: %w", pos, err)
	}
	if hasNonASCII(text) {
		// "英文源串即消息 id"模型下，中文 key 意味着这条消息再也不可能与 Go 侧或其它语言对齐：
		// 它既不是英文原文，也无法作为默认语言的文案。属于模型被破坏，必须当场拦下
		return fmt.Errorf("%s: 内嵌翻译表的 key 必须是英文源串，%q 含非 ASCII 字母", pos, text)
	}
	return nil
}

// jsScanner 是一个只服务于本文件的极简 JS 扫描器。
//
// 刻意不引入 JS 解析器依赖：本文件只关心三件事——跳过注释与空白、读字符串字面量、
// 按配对跳过一个非字符串的值，手写扫描足够稳，也不给仓库添依赖。
type jsScanner struct {
	src *htmlSource
	i   int
}

// skipTrivia 跳过空白与注释。
// 注释必须跳过：页面里的中文注释是开发说明，不是待翻译文案。
func (s *jsScanner) skipTrivia() {
	text := s.src.text
	for s.i < len(text) {
		switch {
		case text[s.i] == ' ' || text[s.i] == '\t' || text[s.i] == '\r' || text[s.i] == '\n':
			s.i++
		case strings.HasPrefix(text[s.i:], "//"):
			for s.i < len(text) && text[s.i] != '\n' {
				s.i++
			}
		case strings.HasPrefix(text[s.i:], "/*"):
			end := strings.Index(text[s.i+2:], "*/")
			if end < 0 {
				s.i = len(text)
				return
			}
			s.i += end + 4
		default:
			return
		}
	}
}

// readString 读一个字符串字面量，返回其值与起始偏移。当前字符必须是引号。
// 反引号模板串也走这里：当作"到下一个反引号为止"，其中的 ${...} 不求值——
// 本文件只判断内容里有没有中文，不还原运行期取值。
func (s *jsScanner) readString() (string, int, error) {
	text := s.src.text
	quote := text[s.i]
	start := s.i
	s.i++

	var b strings.Builder
	for s.i < len(text) {
		c := text[s.i]
		switch {
		case c == quote:
			s.i++
			return b.String(), start, nil
		case c == '\\' && quote != '`':
			lit, n, err := readEscape(text[s.i:])
			if err != nil {
				return "", start, fmt.Errorf("%s: %w", s.src.position(s.i), err)
			}
			b.WriteString(lit)
			s.i += n
		case c == '\n':
			return "", start, fmt.Errorf("%s: 字符串字面量在行尾未闭合", s.src.position(start))
		default:
			b.WriteByte(c)
			s.i++
		}
	}
	return "", start, fmt.Errorf("%s: 字符串字面量未闭合", s.src.position(start))
}

// readEscape 解析一个 JS 反斜杠转义序列，返回其文本与消耗的字节数。
// 只覆盖实际会出现的几种；未知转义按 JS 语义原样返回被转义的字符。
func readEscape(s string) (string, int, error) {
	if len(s) < 2 {
		return "", 0, fmt.Errorf("反斜杠后缺少转义字符")
	}
	switch c := s[1]; c {
	case 'n':
		return "\n", 2, nil
	case 't':
		return "\t", 2, nil
	case 'r':
		return "\r", 2, nil
	case 'b':
		return "\b", 2, nil
	case 'f':
		return "\f", 2, nil
	case 'v':
		return "\v", 2, nil
	case 'x':
		if len(s) < 4 {
			return "", 0, fmt.Errorf("\\x 转义不完整")
		}
		v, err := strconv.ParseUint(s[2:4], 16, 32)
		if err != nil {
			return "", 0, fmt.Errorf("\\x 转义非法: %v", err)
		}
		return string(rune(v)), 4, nil
	case 'u':
		if len(s) > 2 && s[2] == '{' {
			end := strings.IndexByte(s[3:], '}')
			if end < 0 {
				return "", 0, fmt.Errorf("\\u{...} 转义不完整")
			}
			v, err := strconv.ParseUint(s[3:3+end], 16, 32)
			if err != nil {
				return "", 0, fmt.Errorf("\\u{...} 转义非法: %v", err)
			}
			return string(rune(v)), 3 + end + 1, nil
		}
		if len(s) < 6 {
			return "", 0, fmt.Errorf("\\u 转义不完整")
		}
		v, err := strconv.ParseUint(s[2:6], 16, 32)
		if err != nil {
			return "", 0, fmt.Errorf("\\u 转义非法: %v", err)
		}
		return string(rune(v)), 6, nil
	default:
		return string(c), 2, nil
	}
}

// jsMember 是对象字面量里的一个成员。
type jsMember struct {
	key    string // 带引号的 key
	keyOff int    // key 的起始偏移
	val    *jsObject
}

// jsObject 是一个对象字面量的解析结果。
type jsObject struct {
	off     int // '{' 的偏移
	end     int // '}' 之后的偏移
	members []jsMember
}

// parseObject 解析一个字面量对象，s.i 必须指向 '{'。
//
// key 一律要求带引号：带引号的 key 是"这里明确声明了一条消息"的唯一形态，
// 裸标识符 key 只可能出现在别的地方，认下来就会把借用的对象误当翻译表。
func (s *jsScanner) parseObject() (*jsObject, error) {
	text := s.src.text
	if s.i >= len(text) || text[s.i] != '{' {
		return nil, fmt.Errorf("%s: 内嵌翻译表必须是对象字面量", s.src.position(s.i))
	}
	obj := &jsObject{off: s.i}
	s.i++

	for {
		s.skipTrivia()
		if s.i >= len(text) {
			return nil, fmt.Errorf("%s: 对象字面量未闭合", s.src.position(obj.off))
		}
		if text[s.i] == '}' {
			s.i++
			obj.end = s.i
			return obj, nil
		}
		if text[s.i] != '\'' && text[s.i] != '"' {
			return nil, fmt.Errorf("%s: key 必须是带引号的字符串", s.src.position(s.i))
		}
		key, keyOff, err := s.readString()
		if err != nil {
			return nil, err
		}
		s.skipTrivia()
		if s.i >= len(text) || text[s.i] != ':' {
			return nil, fmt.Errorf("%s: %q 之后缺少冒号", s.src.position(s.i), key)
		}
		s.i++
		s.skipTrivia()

		member := jsMember{key: key, keyOff: keyOff}
		if s.i < len(text) && text[s.i] == '{' {
			child, err := s.parseObject()
			if err != nil {
				return nil, err
			}
			member.val = child
		} else if err := s.skipValue(); err != nil {
			return nil, err
		}
		obj.members = append(obj.members, member)

		s.skipTrivia()
		if s.i < len(text) && text[s.i] == ',' {
			s.i++
			continue
		}
		if s.i < len(text) && text[s.i] == '}' {
			continue // 交给循环开头统一收尾
		}
		if s.i >= len(text) {
			return nil, fmt.Errorf("%s: 对象字面量未闭合", s.src.position(obj.off))
		}
		return nil, fmt.Errorf("%s: 对象字面量里出现了预期之外的字符 %q", s.src.position(s.i), string(text[s.i]))
	}
}

// skipValue 跳过一个非对象字面量的值。
func (s *jsScanner) skipValue() error {
	text := s.src.text
	if s.i >= len(text) {
		return fmt.Errorf("%s: 缺少取值", s.src.position(s.i))
	}
	switch text[s.i] {
	case '\'', '"', '`':
		_, _, err := s.readString()
		return err
	case '[', '(':
		return s.skipBalanced()
	}
	for s.i < len(text) {
		switch text[s.i] {
		case ',', '}', ']', ';':
			return nil
		}
		s.i++
	}
	return nil
}

// skipBalanced 跳过一层由 [] 或 () 包起来的片段，尊重其中的字符串与注释。
func (s *jsScanner) skipBalanced() error {
	text := s.src.text
	open := text[s.i]
	closeCh := byte(']')
	if open == '(' {
		closeCh = ')'
	}
	start := s.i
	depth := 0
	for s.i < len(text) {
		switch text[s.i] {
		case open:
			depth++
			s.i++
		case closeCh:
			depth--
			s.i++
			if depth == 0 {
				return nil
			}
		case '\'', '"', '`':
			if _, _, err := s.readString(); err != nil {
				return err
			}
		case '/':
			before := s.i
			s.skipTrivia()
			if s.i == before {
				s.i++
			}
		default:
			s.i++
		}
	}
	return fmt.Errorf("%s: 括号未闭合", s.src.position(start))
}

// collectHTMLUnwrapped 收集 HTML 里未被内嵌翻译表覆盖的含非 ASCII 字母的文案。
//
// 与 Go 侧 collectUnwrapped 的语义一致：这不是错误，而是迁移进度指标，迁移完成后应为空。
// 口径：
//   - 正文文本节点与标签属性值算文案（它们在页面上看得见）
//   - <script> 里的字符串字面量算文案，但注释不算——中文注释是开发说明
//   - <style> 整段不算——CSS 是排版，里面的中文注释同样不是文案
//   - 内嵌翻译表自身不算（ranges 区间跳过），否则表里的中文译文会自我污染
func collectHTMLUnwrapped(src *htmlSource, ranges [][2]int) []Literal {
	text := src.text
	var out []Literal

	for i := 0; i < len(text); {
		if text[i] != '<' {
			j := strings.IndexByte(text[i:], '<')
			if j < 0 {
				j = len(text) - i
			}
			if node := strings.TrimSpace(text[i : i+j]); hasNonASCII(node) {
				out = append(out, Literal{Text: node, Pos: src.position(i)})
			}
			i += j
			continue
		}

		if strings.HasPrefix(text[i:], "<!--") {
			end := strings.Index(text[i+4:], "-->")
			if end < 0 {
				return out
			}
			i += 4 + end + 3
			continue
		}

		tagEnd := findTagEnd(text, i)
		switch lower := strings.ToLower(text[i:tagEnd]); {
		case strings.HasPrefix(lower, "<script"):
			bodyEnd, next := findClosingTag(text, tagEnd, "script")
			out = append(out, collectScriptUnwrapped(src, tagEnd, bodyEnd, ranges)...)
			i = next
		case strings.HasPrefix(lower, "<style"):
			_, next := findClosingTag(text, tagEnd, "style")
			i = next
		default:
			out = append(out, attrUnwrapped(src, i, tagEnd)...)
			i = tagEnd
		}
	}
	return out
}

// collectScriptUnwrapped 收集一段 <script> 正文里含非 ASCII 字母的字符串字面量。
//
// 只认字符串字面量：注释与标识符里的中文都不是文案。翻译表区间整体跳过。
func collectScriptUnwrapped(src *htmlSource, start, end int, ranges [][2]int) []Literal {
	sc := &jsScanner{src: src, i: start}
	var out []Literal

	for sc.i < end {
		if skip, ok := coveringRange(ranges, sc.i); ok {
			sc.i = skip
			continue
		}
		switch c := src.text[sc.i]; c {
		case ' ', '\t', '\r', '\n':
			sc.i++
		case '/':
			before := sc.i
			sc.skipTrivia()
			if sc.i == before {
				sc.i++
			}
		case '\'', '"', '`':
			at := sc.i
			lit, _, err := sc.readString()
			if err != nil {
				// 正则字面量里的引号之类会让扫描错位，此时停下而不是把错位后的内容当成文案：
				// 这里统计的是进度指标，宁少不多
				return out
			}
			if hasNonASCII(lit) && sc.i <= end {
				out = append(out, Literal{Text: lit, Pos: src.position(at)})
			}
		default:
			sc.i++
		}
	}
	return out
}

// attrUnwrapped 收集一段标签里含非 ASCII 字母的属性值。
func attrUnwrapped(src *htmlSource, start, end int) []Literal {
	text := src.text
	var out []Literal
	for i := start; i < end; {
		if text[i] != '\'' && text[i] != '"' {
			i++
			continue
		}
		quote := text[i]
		j := i + 1
		for j < end && text[j] != quote {
			j++
		}
		if j >= end {
			break
		}
		if val := text[i+1 : j]; hasNonASCII(val) {
			out = append(out, Literal{Text: val, Pos: src.position(i + 1)})
		}
		i = j + 1
	}
	return out
}

// findTagEnd 返回 i 处标签的结束位置（'>' 之后），会跳过属性值里的 '>'。
func findTagEnd(text string, i int) int {
	for j := i + 1; j < len(text); {
		switch text[j] {
		case '\'', '"':
			quote := text[j]
			j++
			for j < len(text) && text[j] != quote {
				j++
			}
			j++
		case '>':
			return j + 1
		default:
			j++
		}
	}
	return len(text)
}

// findClosingTag 找 name 标签的闭合标签，返回其内容结束位置与闭合标签之后的位置。
// 找不到闭合标签时把剩下的内容整体当作标签体。
func findClosingTag(text string, from int, name string) (int, int) {
	rest := strings.ToLower(text[from:])
	idx := strings.Index(rest, "</"+name)
	if idx < 0 {
		return len(text), len(text)
	}
	bodyEnd := from + idx
	tagEnd := findTagEnd(text, bodyEnd)
	return bodyEnd, tagEnd
}

// coveringRange 判断 off 是否落在某个区间内，是则返回该区间的结束位置。
// 区间已按起点排序，逐个比较即可（翻译表通常只有一个）。
func coveringRange(ranges [][2]int, off int) (int, bool) {
	for _, r := range ranges {
		if off >= r[0] && off < r[1] {
			return r[1], true
		}
	}
	return 0, false
}
