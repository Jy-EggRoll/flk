package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jy-eggroll/flk/internal/locales"
)

// writeTemp 在临时目录写入一份设置文件并返回其路径
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flk-config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入设置文件失败: %v", err)
	}
	return path
}

// TestLoadAtDefaults 验证文件缺失或 language 为空时回退默认语言
func TestLoadAtDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nonexistent.json")
	cfg, err := LoadAt(missing)
	if err != nil {
		t.Fatalf("文件缺失不应报错: %v", err)
	}
	if cfg.Language != locales.Default {
		t.Errorf("文件缺失时 language = %q，期望 %q", cfg.Language, locales.Default)
	}

	empty := writeTemp(t, `{"language": "   "}`)
	cfg, err = LoadAt(empty)
	if err != nil {
		t.Fatalf("空 language 不应报错: %v", err)
	}
	if cfg.Language != locales.Default {
		t.Errorf("空 language 时 = %q，期望 %q", cfg.Language, locales.Default)
	}
}

// TestLoadAtReadsLanguage 验证正常读取 language 字段
func TestLoadAtReadsLanguage(t *testing.T) {
	path := writeTemp(t, `{"language": "zh-CN"}`)
	cfg, err := LoadAt(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if cfg.Language != "zh-CN" {
		t.Errorf("language = %q，期望 zh-CN", cfg.Language)
	}
}

// TestLoadLanguageReadsOnlyLanguage 验证 LoadLanguage 只取 language，且大小写键都能命中
func TestLoadLanguageReadsOnlyLanguage(t *testing.T) {
	// 键名大小写不一致时由 normalizeKeys 统一小写，仍能读到
	path := writeTemp(t, `{"Language": "zh-CN", "other": 1}`)
	got, err := LoadLanguageAt(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got != "zh-CN" {
		t.Errorf("LoadLanguage = %q，期望 zh-CN", got)
	}
}

// TestLoadAtReadsAllowHosts 验证 allowHosts 数组被正常读出，并做元素级清洗
//
// 清洗规则：元素去首尾空白、丢弃空串与非字符串元素；不清洗的部分（端口、大小写）留给 guard 归一化
func TestLoadAtReadsAllowHosts(t *testing.T) {
	path := writeTemp(t, `{"allowHosts": ["192.168.1.5", "  my.dev.lan  ", "", "   ", 42, null]}`)
	cfg, err := LoadAt(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	want := []string{"192.168.1.5", "my.dev.lan"}
	if len(cfg.AllowHosts) != len(want) {
		t.Fatalf("allowHosts = %#v，期望 %#v", cfg.AllowHosts, want)
	}
	for i, host := range want {
		if cfg.AllowHosts[i] != host {
			t.Errorf("allowHosts[%d] = %q，期望 %q", i, cfg.AllowHosts[i], host)
		}
	}
}

// TestLoadAtIgnoresWrongTypedAllowHosts 验证 allowHosts 类型不对时被忽略而不是报错
//
// 取向与 language 一致：单个字段写错不应让整份设置文件作废，
// 否则 WebUI 会因一个手滑写成字符串的 allowHosts 而连带丢掉 language 等有效设置
func TestLoadAtIgnoresWrongTypedAllowHosts(t *testing.T) {
	for name, content := range map[string]string{
		"字符串而非数组": `{"language": "zh-CN", "allowHosts": "192.168.1.5"}`,
		"数字而非数组":  `{"language": "zh-CN", "allowHosts": 1}`,
		"对象而非数组":  `{"language": "zh-CN", "allowHosts": {"host": "a"}}`,
		"字段缺失":    `{"language": "zh-CN"}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadAt(writeTemp(t, content))
			if err != nil {
				t.Fatalf("类型不对不应报错: %v", err)
			}
			if len(cfg.AllowHosts) != 0 {
				t.Errorf("allowHosts = %#v，期望为空", cfg.AllowHosts)
			}
			// 同一次读取中的其它字段必须照常生效，证明容错没有把整份配置丢掉
			if cfg.Language != "zh-CN" {
				t.Errorf("language = %q，期望 zh-CN", cfg.Language)
			}
		})
	}
}

// TestReadRawAtRejectsMalformed 验证损坏文件被如实报错，而不是静默当作空配置
func TestReadRawAtRejectsMalformed(t *testing.T) {
	path := writeTemp(t, "{not json")
	if _, err := ReadRawAt(path); err == nil {
		t.Error("损坏的 JSON 应当报错")
	}
}

// ---------- 单键写入（WebUI 切语言与 flk config set 共用同一条路径） ----------

// readFile 读取文件内容，供写入用例逐字节比对
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(data)
}

// TestSetKeyAtCreatesFile 验证文件不存在时按需新建，且内容可被读回
//
// 覆盖的是"首次运行就打开 WebUI 切语言"这条最容易被漏掉的路径：
// ~/.config/flk/ 可能压根不存在，写入必须自己创建目录而不是报错
func TestSetKeyAtCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "flk-config.json")
	if err := SetKeyAt(path, "language", "zh-CN"); err != nil {
		t.Fatalf("新建写入失败: %v", err)
	}

	const want = "{\n  \"language\": \"zh-CN\"\n}\n"
	if got := readFile(t, path); got != want {
		t.Errorf("文件内容 = %q，期望 %q（2 空格缩进 + 末尾换行）", got, want)
	}

	// 读回验证：写入格式必须能被本包自己的读取路径消费，而不是"写进去但读不出来"
	got, err := LoadLanguageAt(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got != "zh-CN" {
		t.Errorf("LoadLanguageAt = %q，期望 zh-CN", got)
	}
}

// TestSetKeyAtPreservesOtherKeysAndOrder 验证写回时保留其它字段、键序与键的原始大小写
//
// 这是本次写入能力的核心约束：设置文件由用户手工维护，程序只应改动 language 一个键。
// 断言直接比对**整份文件内容**而不是只查关键字段——只有逐字节比对才能同时锁住
// 「未知键没丢」「键序没变」「allowHosts 的大小写没被改写成小写」这三件事
//
// 期望值里数组被展开成多行是**刻意锁定**的行为（json.Indent 的规范化）：
// 同一份设置必须永远序列化成同一串字节，否则每次写回都会产生与本次修改无关的排版 diff
func TestSetKeyAtPreservesOtherKeysAndOrder(t *testing.T) {
	original := `{
  "allowHosts": [
    "192.168.1.5"
  ],
  "language": "en",
  "myCustomField": {
    "keep": [1, 2, 3]
  },
  "flag": true
}
`
	path := writeTemp(t, original)
	if err := SetKeyAt(path, "language", "zh-CN"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	want := `{
  "allowHosts": [
    "192.168.1.5"
  ],
  "language": "zh-CN",
  "myCustomField": {
    "keep": [
      1,
      2,
      3
    ]
  },
  "flag": true
}
`
	got := readFile(t, path)
	if got != want {
		t.Fatalf("写回内容与期望不一致:\n--- 实得 ---\n%s\n--- 期望 ---\n%s", got, want)
	}

	// 读路径必须同样能看到更新后的语言与未被破坏的白名单
	cfg, err := LoadAt(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if cfg.Language != "zh-CN" {
		t.Errorf("language = %q，期望 zh-CN", cfg.Language)
	}
	if len(cfg.AllowHosts) != 1 || cfg.AllowHosts[0] != "192.168.1.5" {
		t.Errorf("allowHosts 被破坏: %#v", cfg.AllowHosts)
	}
}

// TestSetKeyAtAppendsMissingKey 验证 language 缺失时追加到末尾，而不是覆盖整份文件
func TestSetKeyAtAppendsMissingKey(t *testing.T) {
	path := writeTemp(t, `{"allowHosts": ["a.local"]}`)
	if err := SetKeyAt(path, "language", "zh-CN"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	const want = "{\n  \"allowHosts\": [\n    \"a.local\"\n  ],\n  \"language\": \"zh-CN\"\n}\n"
	if got := readFile(t, path); got != want {
		t.Errorf("文件内容 = %q，期望 %q（新键追加在末尾）", got, want)
	}
}

// TestSetKeyAtMatchesKeyIgnoringCase 验证键的大小写不影响定位，且原有写法被保留
//
// 与读路径 normalizeKeys 的口径一致。若不大小写不敏感地匹配，用户写成 "Language" 时
// 会出现"旧键还在、又追加一个新键"的重复条目；若顺手把键改写成小写，则属于
// 程序擅自改动了用户手写的键名，diff 里会莫名多出一处改动
func TestSetKeyAtMatchesKeyIgnoringCase(t *testing.T) {
	path := writeTemp(t, `{"Language": "en"}`)
	if err := SetKeyAt(path, "language", "zh-CN"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	const want = "{\n  \"Language\": \"zh-CN\"\n}\n"
	if got := readFile(t, path); got != want {
		t.Errorf("文件内容 = %q，期望 %q", got, want)
	}
}

// TestParseLanguageRejectsInvalid 验证语言解析器拒绝空值与不受支持的语言。
//
// 校验发生在注册表的解析器里，而不是 SetKeyAt 里：set 与 validate 共用同一个解析器
// （P1 的单一真相），因此这里直接对解析器断言就等价于守住两条入口。
// 必须用严格判定而不是 l10n.Normalize 的宽松回退——后者会把用户输入的 fr 静默改写成 en，
// 而"我要法语"和"我要英语"显然不是一回事
func TestParseLanguageRejectsInvalid(t *testing.T) {
	for _, lang := range []string{"", "   ", "fr", "de-DE", "english"} {
		if _, err := parseLanguage(lang); err == nil {
			t.Errorf("parseLanguage(%q) 应当报错", lang)
		}
	}
	// 受支持的语言要落到归一化形态：同语种的地区/字形变体收敛到已发布的那一项
	for input, want := range map[string]string{"zh-CN": "zh-CN", "zh": "zh-CN", "zh-Hans": "zh-CN", "EN": "en"} {
		got, err := parseLanguage(input)
		if err != nil {
			t.Errorf("parseLanguage(%q) 不应报错: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("parseLanguage(%q) = %v，期望 %q", input, got, want)
		}
	}
}

// TestSetKeyAtRejectsMalformedFile 验证非法 JSON 被如实报错且原文件原样保留
//
// 取向是"看不懂就不动"：覆盖式重写会把用户手写的内容整体丢弃，
// 而这份文件恰恰是用户唯一的手工配置入口
func TestSetKeyAtRejectsMalformedFile(t *testing.T) {
	original := "{not json"
	path := writeTemp(t, original)
	if err := SetKeyAt(path, "language", "zh-CN"); err == nil {
		t.Error("非法 JSON 应当报错")
	}
	if got := readFile(t, path); got != original {
		t.Errorf("非法文件不得被改写: %q", got)
	}

	// 顶层是数组同样拒绝：本包的读取路径要求顶层为对象，写入也不该把它改成对象
	arrayPath := writeTemp(t, `["a"]`)
	if err := SetKeyAt(arrayPath, "language", "zh-CN"); err == nil {
		t.Error("顶层不是对象时应当报错")
	}
}

// TestSetKeyAtPreservesFileMode 验证写回沿用目标文件原有的权限位
func TestSetKeyAtPreservesFileMode(t *testing.T) {
	path := writeTemp(t, `{"language": "en"}`)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("调整权限失败: %v", err)
	}
	if err := SetKeyAt(path, "language", "zh-CN"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("权限位 = %o，期望 600（临时文件默认 600，落位前必须对齐原文件）", got)
	}
}

// TestSetKeyAtLeavesNoTempFile 验证成功写入后不留临时文件
//
// 原子写会先建同目录临时文件，用例同时锁住"临时文件已随 rename 消失"这一可见后果：
// 若 rename 没发生（例如误写成复制），配置目录里就会攒下一堆 .tmp
func TestSetKeyAtLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flk-config.json")
	if err := SetKeyAt(path, "language", "zh-CN"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "flk-config.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("写入后目录内容 = %v，期望只有 flk-config.json", names)
	}
}

// TestSetLanguageWritesDefaultPath 验证默认路径入口真的落在 ~/.config/flk/flk-config.json
//
// 前因：WebUI 切换语言调用的正是 SetLanguage（无路径版本），它的路径拼接一旦与读取侧
// 不一致（例如少一个 flk 子目录），表现就是"切换成功但重启后又变回英文"，
// 而这种缺陷在只测 SetKeyAt 的用例里完全看不见。因此这里用隔离的 HOME
// 端到端走一遍：写 → 默认路径读回
func TestSetLanguageWritesDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SetLanguage("zh-CN"); err != nil {
		t.Fatalf("SetLanguage 失败: %v", err)
	}
	expected := filepath.Join(home, ".config", "flk", "flk-config.json")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("设置文件未落在默认路径 %s: %v", expected, err)
	}

	got, err := LoadLanguage()
	if err != nil {
		t.Fatalf("默认路径读回失败: %v", err)
	}
	if got != "zh-CN" {
		t.Errorf("LoadLanguage = %q，期望 zh-CN", got)
	}
}

// TestSetKeyAtFollowsSymlink 守住「设置文件是符号链接时，写入必须落在链接指向的真实文件上」
//
// 回归背景：写入走「同目录临时文件 + rename」实现原子落盘，而 rename 替换的是**路径上的那个名字**。
// 用户若把设置文件链进自己的配置仓库（与 flk-store.json 同一种用法），直接按链接路径写入
// 会把链接本身换成普通文件，仓库侧与 ~/.config 下的入口从此脱钩——这个破坏是静默的，
// 直到下次同步才会发现两边各写各的，因此用测试把契约固定
func TestSetKeyAtFollowsSymlink(t *testing.T) {
	repoDir := t.TempDir()
	realPath := filepath.Join(repoDir, "flk-config.json")
	if err := os.WriteFile(realPath, []byte(`{"language": "en"}`), 0o644); err != nil {
		t.Fatalf("写入仓库侧文件失败: %v", err)
	}

	// 链接与真实文件刻意放在不同目录：写入要落到真实文件所在目录，
	// 临时文件也必须建在那里（rename 只在同一文件系统内原子）
	linkPath := filepath.Join(t.TempDir(), "flk-config.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("创建符号链接失败: %v", err)
	}

	if err := SetKeyAt(linkPath, "language", "zh-CN"); err != nil {
		t.Fatalf("经符号链接写入失败: %v", err)
	}

	// 契约一：链接必须仍是链接
	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("Lstat 失败: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("写入后符号链接被替换成了普通文件：配置仓库与 ~/.config 的入口已脱钩")
	}

	// 契约二：内容落在真实文件上，且经链接能读回
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("读取真实文件失败: %v", err)
	}
	if !strings.Contains(string(data), "zh-CN") {
		t.Errorf("真实文件内容未更新: %s", data)
	}
	back, err := LoadLanguageAt(linkPath)
	if err != nil {
		t.Fatalf("经链接读回失败: %v", err)
	}
	if back != "zh-CN" {
		t.Errorf("经链接读回 = %q，期望 zh-CN", back)
	}

	// 契约三：临时文件不能落在链接所在目录（那里不该出现任何新文件）
	entries, err := os.ReadDir(filepath.Dir(linkPath))
	if err != nil {
		t.Fatalf("读取链接所在目录失败: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(linkPath) {
			t.Errorf("链接所在目录出现了额外文件: %s", e.Name())
		}
	}
}

// TestSetKeyAtFollowsDanglingSymlink 守住「链接的目标还不存在时，写入仍不得把链接变成普通文件」
//
// 与上一条用例的区别在链接的形态：那里目标已存在（EvalSymlinks 能解开），
// 这里是断链——最典型的场景是把设置文件刚链进配置仓库、仓库侧那份还没写出来。
// 此时若沿用链接自身的路径落位，链接会被 rename 换成普通文件，
// 用户"配置放在仓库里"的意图就被静默推翻了，因此首次写入必须建在链接的目标位置
func TestSetKeyAtFollowsDanglingSymlink(t *testing.T) {
	repoDir := t.TempDir()
	realPath := filepath.Join(repoDir, "nested", "flk-config.json")
	// 刻意不创建 realPath：连它的父目录都不存在，写入必须自己按需创建
	linkPath := filepath.Join(t.TempDir(), "flk-config.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("创建符号链接失败: %v", err)
	}

	if err := SetKeyAt(linkPath, "language", "zh-CN"); err != nil {
		t.Fatalf("经断链写入失败: %v", err)
	}

	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("Lstat 失败: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("断链在写入后被替换成了普通文件：用户把设置放进仓库的意图被推翻了")
	}
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("链接目标处应有新文件: %v", err)
	}
	if !strings.Contains(string(data), "zh-CN") {
		t.Errorf("链接目标文件内容未更新: %s", data)
	}
}

// ---------- 注册表 ----------

// TestSettingsRegistryMatchesConfigFields 用反射锁住「注册表的键」与「Config 的 json 字段」一一对应。
//
// 为什么值得单测：两处必须同源——注册表少了键则该项无法 set/get，
// Config 少了字段则 flk config show 不显示它（而用户会以为自己没设成功）。
// 这种偏差编译期完全看不出来，只能靠断言拦住
func TestSettingsRegistryMatchesConfigFields(t *testing.T) {
	// Config 侧：字段名 -> json tag
	typ := reflect.TypeOf(Config{})
	fields := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Errorf("Config.%s 缺少 json tag，flk config show 不会输出它", typ.Field(i).Name)
			continue
		}
		fields[strings.Split(tag, ",")[0]] = true
	}

	for _, s := range settings {
		if !fields[s.Key] {
			t.Errorf("注册表的键 %q 在 Config 结构体里没有对应的 json 字段", s.Key)
		}
		delete(fields, s.Key)
		// 默认值与解析器是 get/set/reset/validate 四条路径的共同依赖，缺一不可
		if s.Default == nil {
			t.Errorf("%q 缺少默认值", s.Key)
		}
		if s.Parse == nil {
			t.Errorf("%q 缺少解析器，set 与 validate 将无值可校验", s.Key)
		}
		if s.Expected == "" {
			t.Errorf("%q 缺少期望值描述，非法取值时报错信息会不完整", s.Key)
		}
	}
	for key := range fields {
		t.Errorf("Config 的字段 %q 不在注册表里，用户无法通过 flk config set 修改它", key)
	}
}

// TestLookupIgnoresCaseAndWhitespace 验证键名匹配对大小写与首尾空白不敏感
func TestLookupIgnoresCaseAndWhitespace(t *testing.T) {
	for _, key := range []string{"language", "Language", " LANGUAGE ", "allowHosts", "allowhosts", "loglevel"} {
		if _, ok := Lookup(key); !ok {
			t.Errorf("Lookup(%q) 应当命中注册表里的键", key)
		}
	}
	if _, ok := Lookup("nosuchkey"); ok {
		t.Error("未注册的键不应命中")
	}
}

// TestParseHostListAndOtherParsers 覆盖三个解析器的取值边界。
// 它们是 set 与 validate 共用的同一份判定，因此这里的边界就是两条入口的边界
func TestParseHostListAndOtherParsers(t *testing.T) {
	// 逗号分隔、去空白、丢空项
	got, err := parseHostList(" 192.168.1.5 , my.dev.lan ,, ")
	if err != nil {
		t.Fatalf("parseHostList 不应报错: %v", err)
	}
	hosts, ok := got.([]string)
	if !ok || len(hosts) != 2 || hosts[0] != "192.168.1.5" || hosts[1] != "my.dev.lan" {
		t.Fatalf("parseHostList = %#v，期望两个主机", got)
	}
	// 全空判为非法：清空列表的正确做法是 reset，而不是把空值当清空（一次手滑会静默清掉白名单）
	for _, value := range []string{"", "  ", ",,", " , "} {
		if _, err := parseHostList(value); err == nil {
			t.Errorf("parseHostList(%q) 应当报错", value)
		}
	}

	// 日志级别：合法值落小写规范形态，非法值拒绝
	if v, err := parseLogLevel("DEBUG"); err != nil || v != "debug" {
		t.Errorf("parseLogLevel(\"DEBUG\") = %v, %v，期望 debug, nil", v, err)
	}
	for _, value := range []string{"trace", "notice", ""} {
		if _, err := parseLogLevel(value); err == nil {
			t.Errorf("parseLogLevel(%q) 应当报错", value)
		}
	}
}

// TestGetAtFallsBackToDefaultAndRejectsUnknownKey 验证"生效值"的取值口径与未知键的报错
func TestGetAtFallsBackToDefaultAndRejectsUnknownKey(t *testing.T) {
	// 文件缺失：每个键都回落到默认值
	missing := filepath.Join(t.TempDir(), "nonexistent.json")
	for key, want := range map[string]any{
		"language":   locales.Default,
		"logLevel":   "warn",
		"ALLOWHOSTS": []string{},
	} {
		got, err := GetAt(missing, key)
		if err != nil {
			t.Fatalf("GetAt(%q) 报错: %v", key, err)
		}
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
			t.Errorf("GetAt(%q) = %#v，期望 %#v", key, got, want)
		}
	}

	if _, err := GetAt(missing, "nosuchkey"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("未知键应返回 ErrUnknownKey，实际 %v", err)
	}

	// 文件里的值优先于默认值；language 要归一化到运行期真正生效的形态
	path := writeTemp(t, `{"language": "zh", "logLevel": "ERROR", "allowHosts": ["a.local"]}`)
	for key, want := range map[string]string{"language": "zh-CN", "logLevel": "error"} {
		got, err := GetAt(path, key)
		if err != nil {
			t.Fatalf("GetAt(%q) 报错: %v", key, err)
		}
		if got != want {
			t.Errorf("GetAt(%q) = %v，期望 %q（必须与运行期生效值一致）", key, got, want)
		}
	}

	// 已知键但类型写错：回落到默认值而不是报错，与 LoadAt 的容错取向一致
	broken := writeTemp(t, `{"allowHosts": 1}`)
	got, err := GetAt(broken, "allowHosts")
	if err != nil {
		t.Fatalf("类型不对不应报错: %v", err)
	}
	if list, ok := got.([]string); !ok || len(list) != 0 {
		t.Errorf("allowHosts 类型不对时应回落为空列表，实际 %#v", got)
	}
}

// TestSetKeyAtRejectsUnknownKey 验证写入未知键被拒绝，而不是往文件里写一个永远读不到的键
func TestSetKeyAtRejectsUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flk-config.json")
	if err := SetKeyAt(path, "nosuchkey", "x"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("写入未知键应返回 ErrUnknownKey，实际 %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("被拒绝的写入不得创建文件")
	}
	if err := UnsetKeyAt(path, "nosuchkey"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("删除未知键应返回 ErrUnknownKey，实际 %v", err)
	}
}

// TestUnsetKeyAtRemovesOnlyTargetKey 验证删键只动目标键，其它键与键序原样保留
func TestUnsetKeyAtRemovesOnlyTargetKey(t *testing.T) {
	path := writeTemp(t, "{\n  \"Language\": \"zh-CN\",\n  \"logLevel\": \"debug\",\n  \"mine\": 1\n}\n")
	if err := UnsetKeyAt(path, "language"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	const want = "{\n  \"logLevel\": \"debug\",\n  \"mine\": 1\n}\n"
	if got := readFile(t, path); got != want {
		t.Errorf("文件内容 = %q，期望 %q", got, want)
	}

	// 幂等：键本来就不存在时是空操作，便于 reset 被重复执行
	if err := UnsetKeyAt(path, "language"); err != nil {
		t.Fatalf("重复删除不应报错: %v", err)
	}
	if got := readFile(t, path); got != want {
		t.Errorf("重复删除改动了文件: %q", got)
	}

	// 注册表里的键被删光后文件仍是合法 JSON 对象，读取侧不应报错。
	// 未知键（mine）不会被本函数碰——只有注册表里的键才有确定的"默认值"语义，
	// 删掉用户的其它字段属于擅自改动他的数据
	if err := UnsetKeyAt(path, "logLevel"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if got := readFile(t, path); got != "{\n  \"mine\": 1\n}\n" {
		t.Errorf("清空注册表键后的文件 = %q，期望只剩用户自己的键", got)
	}
	if _, err := LoadAt(path); err != nil {
		t.Errorf("清空后的文件必须仍能被读取: %v", err)
	}
}

// TestResetAllAtBothModes 验证两种重置模式：删文件，或写一份全默认值的文件
func TestResetAllAtBothModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flk-config.json")

	// 写默认值：文件不存在也能工作（不先删再写，避免"删成功、写失败"的窗口）
	if err := ResetAllAt(path, true); err != nil {
		t.Fatalf("写入默认值失败: %v", err)
	}
	cfg, err := LoadAt(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if cfg.Language != locales.Default || cfg.LogLevel != "warn" || len(cfg.AllowHosts) != 0 {
		t.Errorf("默认值文件读回 = %#v，期望全默认", cfg)
	}
	// 注册表的每一项都必须出现在文件里，否则 reset --defaults 会静默漏写某个键
	raw, err := ReadRawAt(path)
	if err != nil {
		t.Fatalf("读原始键值失败: %v", err)
	}
	if len(raw) != len(settings) {
		t.Errorf("默认值文件有 %d 个键，注册表有 %d 项", len(raw), len(settings))
	}

	// 删除文件：回到"从未配置过"的状态
	if err := ResetAllAt(path, false); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("删除模式下文件应当不存在")
	}
	// 重复删除是幂等的
	if err := ResetAllAt(path, false); err != nil {
		t.Errorf("重复删除不应报错: %v", err)
	}
}

// ---------- 体检 ----------

// countIssues 统计体检结果里各严重级别的条数
func countIssues(issues []Issue) (errors, warnings int) {
	for _, issue := range issues {
		if issue.Level == LevelError {
			errors++
			continue
		}
		warnings++
	}
	return errors, warnings
}

// TestValidateAtReportsProblems 验证体检能把"运行期会被静默忽略的东西"逐条说出来。
//
// 这是体检存在的全部意义：运行期为了"永远能跑"会静默回退默认值，
// 若体检也不说，用户就只能面对"我明明设了却不生效"却毫无线索
func TestValidateAtReportsProblems(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantErrors   int
		wantWarnings int
	}{
		{name: "全部合法", content: `{"language":"zh-CN","logLevel":"debug","allowHosts":["a.local"]}`},
		{name: "语言不受支持", content: `{"language":"fr"}`, wantErrors: 1},
		{name: "日志级别非法", content: `{"logLevel":"notice"}`, wantErrors: 1},
		{name: "未知键", content: `{"nosuchkey":1}`, wantErrors: 1},
		{name: "标量类型不对", content: `{"language":1}`, wantErrors: 1},
		{name: "列表类型不对", content: `{"allowHosts":"a.local"}`, wantErrors: 1},
		{name: "空串等同未设置", content: `{"language":""}`, wantWarnings: 1},
		{name: "列表元素的三种问题", content: `{"allowHosts":["a.local",1,"","a.local"]}`, wantErrors: 1, wantWarnings: 2},
		{name: "仅大小写不同的重复键", content: `{"Language":"en","language":"en"}`, wantErrors: 1},
		{name: "非法 JSON", content: `{not json`, wantErrors: 1},
		{name: "顶层不是对象", content: `["a"]`, wantErrors: 1},
		{name: "BOM 开头的文件", content: "\ufeff" + `{"language":"zh-CN"}`, wantErrors: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issues, err := ValidateAt(writeTemp(t, test.content))
			if err != nil {
				t.Fatalf("体检本身不应报错: %v", err)
			}
			gotErrors, gotWarnings := countIssues(issues)
			if gotErrors != test.wantErrors || gotWarnings != test.wantWarnings {
				t.Fatalf("体检结果 = %d 错误 / %d 警告，期望 %d / %d：%#v",
					gotErrors, gotWarnings, test.wantErrors, test.wantWarnings, issues)
			}
		})
	}
}

// TestValidateAtMissingFileIsNotAProblem 验证文件不存在不算问题：设置文件本来就允许不存在
func TestValidateAtMissingFileIsNotAProblem(t *testing.T) {
	issues, err := ValidateAt(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatalf("文件缺失不应报错: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("文件缺失不应产生体检条目: %#v", issues)
	}
}
