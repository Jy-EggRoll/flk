package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件覆盖 HTML 内嵌翻译表的提取，重点在三件事：
//  1. 表里的 key 能被提取成消息（含与 Go 消息合并后的去重）
//  2. 表外的一切都不算消息——正文、属性值、CSS、注释、普通 JS 字面量都不能误报
//  3. 表外含非 ASCII 字母的文案要作为「未迁移」报出来，且注释与 CSS 不掺进来

// writeHTML 在临时仓库里写一个 HTML 文件，并返回配置好的扫描配置。
func writeHTML(t *testing.T, root, rel, content string) Config {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入 %s 失败: %v", rel, err)
	}
	return Config{Root: root, ImportPath: testPkg, SrcDirs: []string{"cmd"}, HTMLSrc: []string{"ui"}}
}

// scanHTML 扫描只有 HTML 来源的临时仓库。
func scanHTML(t *testing.T, cfg Config) *Result {
	t.Helper()
	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	return res
}

// messageTexts 取消息 id 列表，便于断言。
func messageTexts(res *Result) []string {
	out := make([]string, 0, len(res.Messages))
	for _, m := range res.Messages {
		out = append(out, m.Text)
	}
	return out
}

// unwrappedTexts 取待迁移文案列表，便于断言。
func unwrappedTexts(res *Result) []string {
	out := make([]string, 0, len(res.Unwrapped))
	for _, u := range res.Unwrapped {
		out = append(out, u.Text)
	}
	return out
}

// TestScanHTMLExtractsTableKeys 断言内嵌翻译表的 key 被提取成消息，
// 且每条消息记录了它出现在哪些语言段里（缺段即该语言会回退英文）。
func TestScanHTMLExtractsTableKeys(t *testing.T) {
	root := t.TempDir()
	cfg := writeHTML(t, root, "ui/page.html", `<html><body>
<h1>flk config</h1>
<script>
var LANG = (window.__FLK_LANG__ || 'en');
var MSG = {
  'zh-CN': {
    'flk config': 'flk 配置',
    'Save': '保存',
    ' | Size: {size}': ' | 大小: {size}'
  }
};
</script>
</body></html>
`)

	res := scanHTML(t, cfg)
	want := []string{" | Size: {size}", "Save", "flk config"}
	if got := messageTexts(res); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("消息集合不符\n实得: %q\n期望: %q", got, want)
	}
	if len(res.HTMLFiles) != 1 || res.HTMLFiles[0] != "ui/page.html" {
		t.Errorf("HTML 文件列表不符: %q", res.HTMLFiles)
	}
	if len(res.HTMLTables) != 1 {
		t.Fatalf("应提取到 1 张翻译表，实得 %d", len(res.HTMLTables))
	}
	tbl := res.HTMLTables[0]
	if strings.Join(tbl.Langs, ",") != "zh-CN" {
		t.Errorf("语言段不符: %q", tbl.Langs)
	}
	// 带前导空格的拼接片段是合法 id：HTML 的 key 是引号内字面量，不存在源码格式导致的漂移
	// （Go 侧的 validateMessage 会拒绝它，这里刻意放宽，见 validateHTMLMessageID）
	for _, m := range tbl.Msgs {
		if m.Text == " | Size: {size}" && strings.Join(m.Langs, ",") != "zh-CN" {
			t.Errorf("消息 %q 的语言段不符: %q", m.Text, m.Langs)
		}
	}
}

// TestScanHTMLIgnoresEverythingOutsideTable 断言表外的一切都不算消息。
//
// 这是"抗误报"的核心用例：正文英文、属性值、CSS、注释、普通 JS 字面量、
// 以及同名但非翻译表的对象（如 MSG2）都不该被收成消息。
func TestScanHTMLIgnoresEverythingOutsideTable(t *testing.T) {
	root := t.TempDir()
	cfg := writeHTML(t, root, "ui/page.html", `<html>
<head>
<style>
/* 样式注释里的中文与文案无关 */
.box::after { content: "占位"; }
</style>
</head>
<body>
<p>Plain English paragraph</p>
<span title="Some English title">Text</span>
<script>
var saveLabel = 'Save';
var MSG2 = { 'zh-CN': { 'Not a table': 'not a table' } };
var MSG = {
  'zh-CN': {
    'Save': '保存'
  }
};
/* 这里是中文注释，不是文案 */
// 另一行中文注释
function pick() { return MSG['zh-CN']; }
</script>
</body>
</html>
`)

	res := scanHTML(t, cfg)
	if got := messageTexts(res); len(got) != 1 || got[0] != "Save" {
		t.Errorf("只应提取到翻译表里的 Save，实得 %q", got)
	}
	if len(res.Unwrapped) != 0 {
		t.Errorf("表外没有中文文案，待迁移应为空，实得 %q", unwrappedTexts(res))
	}
}

// TestScanHTMLDetectsUnwrappedChinese 断言表外含非 ASCII 字母的文案被报为待迁移，
// 且注释与 CSS 里的中文不掺进来（它们被当成开发说明与排版，不是待翻译文案）。
func TestScanHTMLDetectsUnwrappedChinese(t *testing.T) {
	root := t.TempDir()
	cfg := writeHTML(t, root, "ui/page.html", `<html>
<head>
<style>
/* 样式里的中文不该被算作文案 */
.box { color: red; }
</style>
</head>
<body>
<!-- 注释里的中文也不该被算作文案 -->
<div title="硬编码的标题">尚未迁移的正文</div>
<script>
var MSG = { 'zh-CN': { 'Save': '保存' } };
/* 中文注释同样不算 */
var legacy = '尚未迁移的 JS 文案';
</script>
</body>
</html>
`)

	res := scanHTML(t, cfg)
	got := unwrappedTexts(res)
	want := map[string]bool{"硬编码的标题": true, "尚未迁移的正文": true, "尚未迁移的 JS 文案": true}
	if len(got) != len(want) {
		t.Fatalf("待迁移集合不符\n实得: %q\n期望: %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("多报了不该算作文案的内容: %q", g)
		}
	}
}

// TestScanHTMLRejectsBrokenTable 断言翻译表形态不符时直接报错，而不是猜一个结果。
// 猜错的后果是"页面文案没被提取，门禁却通过"，属于最坏的失效形态。
func TestScanHTMLRejectsBrokenTable(t *testing.T) {
	cases := map[string]string{
		"语言段的取值不是对象": `<script>var MSG = { 'zh-CN': 'whole thing' };</script>`,
		"译文不是字符串":    `<script>var MSG = { 'zh-CN': { 'Save': { one: '保存' } } };</script>`,
		"key 未加引号":   `<script>var MSG = { 'zh-CN': { Save: '保存' } };</script>`,
		"key 是中文源串":  `<script>var MSG = { 'zh-CN': { '保存': '保存' } };</script>`,
		"完全没有翻译表":    `<html><body><p>no table here</p></body></html>`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			cfg := writeHTML(t, root, "ui/page.html", "<html><body>"+body+"</body></html>\n")
			if _, err := Scan(cfg); err == nil {
				t.Errorf("%s：应当报错", name)
			}
		})
	}
}

// TestScanMergesGoAndHTMLMessages 断言两个来源的消息合并成一份语料并去重。
//
// 消息 id 就是英文源串，"Save" 无论在 Go 代码还是页面里都是同一条消息，
// 因此默认语言文件只需要一份——这正是把页面纳入同一套校验的前提。
func TestScanMergesGoAndHTMLMessages(t *testing.T) {
	root := t.TempDir()
	cfg := writeHTML(t, root, "ui/page.html", `<script>
var MSG = { 'zh-CN': { 'Save': '保存', 'Page only': '仅页面' } };
</script>
`)
	writeGo(t, root, "cmd/x.go", msgFile(`var a = l10n.T("Save", nil)
var b = l10n.T("Go only", nil)`))

	res := scanHTML(t, cfg)
	want := []string{"Go only", "Page only", "Save"}
	if got := messageTexts(res); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("合并后的消息集合不符\n实得: %q\n期望: %q", got, want)
	}
	if res.Files != 2 {
		t.Errorf("解析文件数应为 Go 与 HTML 合计 2，实得 %d", res.Files)
	}
}

// TestScanHTMLListsFilesUnderDirectory 断言 --html 给目录时会递归收集 .html，
// 且同一文件被重复点名（文件 + 所在目录）不会重复扫描。
func TestScanHTMLListsFilesUnderDirectory(t *testing.T) {
	root := t.TempDir()
	writeHTML(t, root, "ui/a.html", `<script>var MSG = { 'zh-CN': { 'A': '甲' } };</script>`)
	writeHTML(t, root, "ui/nested/b.html", `<script>var MSG = { 'zh-CN': { 'B': '乙' } };</script>`)
	// 非 .html 文件不该被目录递归收进来
	writeHTML(t, root, "ui/notes.txt", "应当被跳过")

	cfg := Config{
		Root: root, ImportPath: testPkg, SrcDirs: []string{"cmd"},
		HTMLSrc: []string{"ui", "ui/a.html"}, // 目录 + 其中的文件
	}
	res := scanHTML(t, cfg)
	if strings.Join(res.HTMLFiles, ",") != "ui/a.html,ui/nested/b.html" {
		t.Errorf("HTML 文件列表不符: %q", res.HTMLFiles)
	}
	if got := messageTexts(res); strings.Join(got, "|") != "A|B" {
		t.Errorf("消息集合不符: %q", got)
	}
}
