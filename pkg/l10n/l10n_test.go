package l10n

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

// 本文件的测试刻意不引用任何真实项目的语言文件，而是用 fstest.MapFS 造一套
// 「法语 / 德语」的数据。这既是测试，也是**库可复用性的自证**：若本包残留了
// 任何项目专属假设（硬编码的语言列表、写死的文件路径、默认英文等），下面会失败。
const (
	testDefault = "fr"
	testOther   = "de"
)

// testLangs 是测试用的语言列表，顺序即匹配优先级
var testLangs = []string{testDefault, testOther}

func testFS(t *testing.T) fs.FS {
	t.Helper()
	return fstest.MapFS{
		"fr.json": &fstest.MapFile{Data: []byte(`{"Hello": "Bonjour", "Bye": "Au revoir"}`)},
		"de.json": &fstest.MapFile{Data: []byte(`{"Hello": "Hallo"}`)},
	}
}

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{Default: testDefault, Supported: testLangs, FS: testFS(t)}
}

// TestInitAndTranslate 验证按 Options 指定的语言列表加载并翻译。
func TestInitAndTranslate(t *testing.T) {
	if err := Init(testDefault, testOptions(t)); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	if got := T("Hello", nil); got != "Bonjour" {
		t.Errorf("默认语言下 T(Hello) = %q, want Bonjour", got)
	}

	if err := Init(testOther, testOptions(t)); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	if got := T("Hello", nil); got != "Hallo" {
		t.Errorf("德语下 T(Hello) = %q, want Hallo", got)
	}
}

// TestInitAcceptsSloppyLanguage 验证 Init 自己会归一化语言串，
// 调用方不必在 Init 之前持有语言白名单。
func TestInitAcceptsSloppyLanguage(t *testing.T) {
	cases := map[string]string{
		"未指定":    "",
		"大小写不一":  "FR",
		"带地区子标签": "fr-CA",
		"完全不认识":  "zz",
	}
	for name, lang := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Init(lang, testOptions(t)); err != nil {
				t.Fatalf("Init(%q) 失败: %v", lang, err)
			}
			if got := Current(); got != testDefault {
				t.Errorf("Init(%q) 后 Current() = %q, want %q", lang, got, testDefault)
			}
		})
	}
}

// TestTranslateInterpolatesTemplateData 验证模板变量插值。
func TestTranslateInterpolatesTemplateData(t *testing.T) {
	opts := testOptions(t)
	opts.Supported = []string{testDefault}
	opts.FS = fstest.MapFS{
		"fr.json": &fstest.MapFile{Data: []byte(`{"Total: {{.N}}": "TOTAL: {{.N}}"}`)},
	}
	if err := Init(testDefault, opts); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	if got := T("Total: {{.N}}", map[string]any{"N": 3}); got != "TOTAL: 3" {
		t.Errorf("插值结果 = %q", got)
	}
}

// TestTranslateFallsBackToSourceText 验证两类降级都回落到源串。
//
// 这是"源串即 id"模型的关键性质：**任何漏翻都不会产生空串或裸 key**，
// 最差情况只是显示源串。因此这里断言的正是"等于源串"而不是"报错"。
func TestTranslateFallsBackToSourceText(t *testing.T) {
	if err := Init(testDefault, testOptions(t)); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}

	// 语言文件里不存在的消息
	const unknown = "This message does not exist in any locale file"
	if got := T(unknown, nil); got != unknown {
		t.Errorf("未知消息应回退为源串，实得 %q", got)
	}

	// 模板变量漏传：missingkey=error 会让渲染失败，同样回退为源串
	opts := testOptions(t)
	opts.Supported = []string{testDefault}
	opts.FS = fstest.MapFS{
		"fr.json": &fstest.MapFile{Data: []byte(`{"V: {{.N}}": "V: {{.N}}"}`)},
	}
	if err := Init(testDefault, opts); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	const needData = "V: {{.N}}"
	if got := T(needData, nil); got != needData {
		t.Errorf("漏传模板变量应回退为源串，实得 %q", got)
	}
}

// TestTranslateBeforeInit 断言未初始化时 T 安全返回源串而不是 panic。
func TestTranslateBeforeInit(t *testing.T) {
	saved := localizer
	localizer = nil
	defer func() { localizer = saved }()

	const id = "Hello"
	if got := T(id, nil); got != id {
		t.Errorf("未初始化时应回退为源串，实得 %q", got)
	}
}

// TestOptionsValidation 验证非法 Options 会被 Init 拒绝而不是静默降级。
func TestOptionsValidation(t *testing.T) {
	ok := testOptions(t)

	cases := map[string]struct {
		mutate func(*Options)
		why    string
	}{
		"缺 Default":            {func(o *Options) { o.Default = "" }, "默认语言为空会让回退链断掉"},
		"缺 Supported":          {func(o *Options) { o.Supported = nil }, "没有语言就无从加载"},
		"缺 FS":                 {func(o *Options) { o.FS = nil }, "没有文件系统就无从加载"},
		"Default 不在 Supported": {func(o *Options) { o.Default = "en" }, "默认语言必须有自己的语言文件"},
		"Supported 有重复":        {func(o *Options) { o.Supported = []string{"fr", "fr"} }, "重复项说明调用方搞错了"},
		"Supported 有空项":        {func(o *Options) { o.Supported = []string{"fr", ""} }, "空标签会让文件路径怪怪的"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			o := ok
			c.mutate(&o)
			if err := Init(o.Default, o); err == nil {
				t.Errorf("应被拒绝（%s）", c.why)
			}
		})
	}
}

// TestLoadRejectsBadLocaleFiles 验证三类会让译文静默失效的文件问题会被拦下。
func TestLoadRejectsBadLocaleFiles(t *testing.T) {
	cases := map[string]string{
		"非法 JSON": `{not json`,
		"嵌套结构":    `{"a": {"b": "c"}}`,
		"取值不是字符串": `{"a": 1}`,
		"键命中保留字":  `{"Other": "x"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			opts := testOptions(t)
			opts.Supported = []string{testDefault}
			opts.FS = fstest.MapFS{"fr.json": &fstest.MapFile{Data: []byte(content)}}
			if err := Init(testDefault, opts); err == nil {
				t.Errorf("%s 应被拒绝", name)
			}
		})
	}
}

// TestValidateMessageIDRejectsReservedWords 验证保留字拦截。
//
// go-i18n 会把 other/one/hash 等词当作消息字段名（忽略大小写），若某条消息 id 恰好
// 等于这些词，整份语言文件会解析失败或条目被丢弃——两种后果都是译文静默全失。
func TestValidateMessageIDRejectsReservedWords(t *testing.T) {
	for _, id := range []string{"other", "Other", "ONE", "hash", "id", "description", "translation", "zero", "few"} {
		if err := ValidateMessageID(id); err == nil {
			t.Errorf("保留字 %q 应被拒绝", id)
		}
	}
	for _, id := range []string{"Others", "description of the repo", "Total size: {{.Size}}", "Disk usage"} {
		if err := ValidateMessageID(id); err != nil {
			t.Errorf("正常消息 %q 不应被拒绝: %v", id, err)
		}
	}
}

// TestNormalize 验证宽松归一化：受支持的语言必须能被各种写法命中，其余回退默认。
func TestNormalize(t *testing.T) {
	if err := Init(testDefault, testOptions(t)); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	cases := map[string]string{
		"": testDefault, "  ": testDefault, "fr": "fr", "FR": "fr",
		"fr-CA": "fr", "de": "de", "DE-AT": "de",
		"zh-CN": testDefault, "200": testDefault,
	}
	for in, want := range cases {
		if got := Normalize(in, testLangs, testDefault); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsSupported 验证严格校验与宽松归一化的分工。
//
// 两者的差别是本包对外契约的一部分：Normalize 把"不认识"当成"回退默认语言"，
// 而需要"拒绝而不是改写"的场合（如设置语言）必须用 IsSupported。
func TestIsSupported(t *testing.T) {
	if err := Init(testDefault, testOptions(t)); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	for _, s := range []string{"fr", "FR", "fr-CA", "de", "de-AT"} {
		if !IsSupported(s, testLangs) {
			t.Errorf("IsSupported(%q) 应为 true", s)
		}
	}
	for _, s := range []string{"", "  ", "en", "zh-CN", "200", "-l"} {
		if IsSupported(s, testLangs) {
			t.Errorf("IsSupported(%q) 应为 false", s)
		}
	}
}

// TestSupportedReturnsCopy 断言 Supported 返回副本，调用方改动不会污染包内状态。
func TestSupportedReturnsCopy(t *testing.T) {
	if err := Init(testDefault, testOptions(t)); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	got := Supported()
	if len(got) != 2 {
		t.Fatalf("Supported() = %v", got)
	}
	got[0] = "mutated"
	if Supported()[0] == "mutated" {
		t.Error("Supported 必须返回副本，否则调用方可以污染内部语言列表")
	}
}
