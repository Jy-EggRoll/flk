package l10n

import (
	"io/fs"
	"sync"
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
//
// "未初始化"的判定标准随状态结构一起变了：从"localizer 为 nil"变成"包级快照为 nil"
// （见 l10n.go 的 state）。显式置 nil 覆盖的正是"首次 Init 就失败"这一真实场景，
// 顺带锁住 Current/Supported/SetLanguage 在无状态时的行为——它们都不允许 panic，
// 也不允许假装成功
func TestTranslateBeforeInit(t *testing.T) {
	saved := state.Load()
	state.Store(nil)
	defer func() { state.Store(saved) }()

	const id = "Hello"
	if got := T(id, nil); got != id {
		t.Errorf("未初始化时应回退为源串，实得 %q", got)
	}
	if got := Current(); got != "" {
		t.Errorf("未初始化时 Current() 应为空串，实得 %q", got)
	}
	if got := Supported(); got != nil {
		t.Errorf("未初始化时 Supported() 应为 nil，实得 %v", got)
	}
	if err := SetLanguage(testOther); err == nil {
		t.Error("未初始化时 SetLanguage 必须报错，而不是假装切换成功")
	}
}

// countingFS 包装一个文件系统并统计读取次数，用于证明「语言文件只在 Init 时解析一次」。
//
// 为什么需要把读取次数变成可断言的量：SetLanguage 若偷偷重新 ParseMessageFileBytes，
// 所有功能断言照样全绿——只有并发下的数据竞争才暴露（loadLocaleFile 会写 bundle 的内部
// 状态），而那种失败既难稳定复现，报错位置也离根因很远。把前提变成门禁拦得住的事实，
// 才不至于"注释里写着不许，代码里没人管"
type countingFS struct {
	fs    fs.FS
	reads int
}

func (c *countingFS) Open(name string) (fs.File, error) {
	c.reads++
	return c.fs.Open(name)
}

// ReadFile 让 fs.ReadFile 走这条直通路径（它优先使用 ReadFileFS），
// 否则计数会漏掉真实的读取调用
func (c *countingFS) ReadFile(name string) ([]byte, error) {
	c.reads++
	return fs.ReadFile(c.fs, name)
}

// TestSetLanguageSwitchesWithoutReloading 覆盖运行期切换语言的全部口径。
//
// 三类断言缺一不可：
//   - 切换生效且可逆：de → fr 往复，译文与 Current 必须同时跟上
//   - 拒绝而不是改写：不受支持的语言必须返回 error，且**状态保持不变**——
//     静默回退默认语言是本包明确要避免的行为（见 IsSupported 的注释）
//   - 不重新解析文件：切换前后文件读取次数必须相同，这是 SetLanguage 并发安全的前提
func TestSetLanguageSwitchesWithoutReloading(t *testing.T) {
	counting := &countingFS{fs: testFS(t)}
	opts := testOptions(t)
	opts.FS = counting
	if err := Init(testDefault, opts); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	readsAfterInit := counting.reads
	if readsAfterInit == 0 {
		t.Fatal("Init 必须读取语言文件（否则本用例的计数断言是空真）")
	}

	cases := []struct {
		name        string
		lang        string
		wantCurrent string
		wantHello   string
		wantErr     bool
	}{
		{name: "切到另一种语言", lang: testOther, wantCurrent: "de", wantHello: "Hallo"},
		{name: "大小写不敏感", lang: "DE", wantCurrent: "de", wantHello: "Hallo"},
		{name: "地区变体收敛到已发布项", lang: "de-AT", wantCurrent: "de", wantHello: "Hallo"},
		{name: "切回默认语言", lang: "fr-CA", wantCurrent: "fr", wantHello: "Bonjour"},
		{name: "不受支持的语言必须被拒绝", lang: "zz", wantErr: true},
		{name: "空串必须被拒绝", lang: "  ", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := Current()
			err := SetLanguage(c.lang)
			if c.wantErr {
				if err == nil {
					t.Fatalf("SetLanguage(%q) 应返回 error", c.lang)
				}
				if got := Current(); got != before {
					t.Fatalf("被拒绝的切换不得改动状态：Current() 从 %q 变成 %q", before, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetLanguage(%q) 失败: %v", c.lang, err)
			}
			if got := Current(); got != c.wantCurrent {
				t.Fatalf("Current() = %q，期望 %q", got, c.wantCurrent)
			}
			if got := T("Hello", nil); got != c.wantHello {
				t.Fatalf("切换后 T(Hello) = %q，期望 %q", got, c.wantHello)
			}
		})
	}

	if counting.reads != readsAfterInit {
		t.Fatalf("SetLanguage 不得重新解析语言文件：Init 期间读取 %d 次，多次切换后变成 %d 次",
			readsAfterInit, counting.reads)
	}
}

// TestSetLanguageConcurrentWithTranslators 是本次改造的核心门禁，必须配合 -race 运行。
//
// 回归背景：切换语言曾经只能靠重启进程，包级 localizer/current 被当作"Init 之后只读"，
// 于是运行期切换必然会与并发中的 T() 竞争这两个裸指针（实测报 Write at l10n.go:70
// vs Read at l10n.go:83）。改成 atomic 快照后，本用例用多读者 + 多写者的方式持续施压：
//   - 8 个读者各自反复调用 T / Current / Supported（模拟 worker 并发翻译）
//   - 2 个写者在两种语言之间反复切换
//
// 光靠 -race 只能证明"没有数据竞争"，因此收尾还补一条功能断言：切换停下来之后，
// Current 与译文必须一致地落在最后一次切换的语言上（防"状态发布了一半"）
func TestSetLanguageConcurrentWithTranslators(t *testing.T) {
	if err := Init(testDefault, testOptions(t)); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	// 本包其它用例依赖进程级语言状态，无论成败都还原成默认语言
	t.Cleanup(func() {
		if err := Init(testDefault, testOptions(t)); err != nil {
			t.Errorf("还原 l10n 状态失败: %v", err)
		}
	})

	const readers = 8
	const switches = 2000
	stop := make(chan struct{})
	// 读者与写者各用一个 WaitGroup：读者的退出条件依赖写者跑完（见下方 close(stop)），
	// 两者混在一起会形成"写者等读者、读者等写者"的死锁
	var readersWG, writersWG sync.WaitGroup

	for i := 0; i < readers; i++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = T("Hello", nil)
				_ = T("Total: {{.N}}", map[string]any{"N": 1})
				_ = Current()
				_ = Supported()
			}
		}()
	}

	for i := 0; i < 2; i++ {
		writersWG.Add(1)
		go func(offset int) {
			defer writersWG.Done()
			for n := 0; n < switches; n++ {
				lang := testDefault
				if (n+offset)%2 == 1 {
					lang = testOther
				}
				if err := SetLanguage(lang); err != nil {
					t.Errorf("并发切换 %q 失败: %v", lang, err)
					return
				}
			}
		}(i)
	}

	// 写者跑固定次数（用例时长可控），跑完才放读者退出
	writersWG.Wait()
	close(stop)
	readersWG.Wait()

	if err := SetLanguage(testOther); err != nil {
		t.Fatalf("收尾切换失败: %v", err)
	}
	if got := Current(); got != "de" {
		t.Fatalf("收尾后 Current() = %q，期望 de", got)
	}
	if got := T("Hello", nil); got != "Hallo" {
		t.Fatalf("收尾后 T(Hello) = %q，期望 Hallo", got)
	}
	if got := T("Bye", nil); got != "Au revoir" {
		t.Fatalf("收尾后 T(Bye) = %q，期望 Au revoir（德语无此译文，应回退默认语言 fr）", got)
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
