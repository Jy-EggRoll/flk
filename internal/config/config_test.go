package config

import (
	"os"
	"path/filepath"
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

// ---------- 写入能力（网页端切换语言需要） ----------

// readFile 读取文件内容，供写入用例逐字节比对
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(data)
}

// TestSetLanguageAtCreatesFile 验证文件不存在时按需新建，且内容可被读回
//
// 覆盖的是"首次运行就打开 WebUI 切语言"这条最容易被漏掉的路径：
// ~/.config/flk/ 可能压根不存在，写入必须自己创建目录而不是报错
func TestSetLanguageAtCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "flk-config.json")
	if err := SetLanguageAt(path, "zh-CN"); err != nil {
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

// TestSetLanguageAtPreservesOtherKeysAndOrder 验证写回时保留其它字段、键序与键的原始大小写
//
// 这是本次写入能力的核心约束：设置文件由用户手工维护，程序只应改动 language 一个键。
// 断言直接比对**整份文件内容**而不是只查关键字段——只有逐字节比对才能同时锁住
// 「未知键没丢」「键序没变」「allowHosts 的大小写没被改写成小写」这三件事
//
// 期望值里数组被展开成多行是**刻意锁定**的行为（json.Indent 的规范化）：
// 同一份设置必须永远序列化成同一串字节，否则每次写回都会产生与本次修改无关的排版 diff
func TestSetLanguageAtPreservesOtherKeysAndOrder(t *testing.T) {
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
	if err := SetLanguageAt(path, "zh-CN"); err != nil {
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

// TestSetLanguageAtAppendsMissingKey 验证 language 缺失时追加到末尾，而不是覆盖整份文件
func TestSetLanguageAtAppendsMissingKey(t *testing.T) {
	path := writeTemp(t, `{"allowHosts": ["a.local"]}`)
	if err := SetLanguageAt(path, "zh-CN"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	const want = "{\n  \"allowHosts\": [\n    \"a.local\"\n  ],\n  \"language\": \"zh-CN\"\n}\n"
	if got := readFile(t, path); got != want {
		t.Errorf("文件内容 = %q，期望 %q（新键追加在末尾）", got, want)
	}
}

// TestSetLanguageAtMatchesKeyIgnoringCase 验证键的大小写不影响定位，且原有写法被保留
//
// 与读路径 normalizeKeys 的口径一致。若不大小写不敏感地匹配，用户写成 "Language" 时
// 会出现"旧键还在、又追加一个新键"的重复条目；若顺手把键改写成小写，则属于
// 程序擅自改动了用户手写的键名，diff 里会莫名多出一处改动
func TestSetLanguageAtMatchesKeyIgnoringCase(t *testing.T) {
	path := writeTemp(t, `{"Language": "en"}`)
	if err := SetLanguageAt(path, "zh-CN"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	const want = "{\n  \"Language\": \"zh-CN\"\n}\n"
	if got := readFile(t, path); got != want {
		t.Errorf("文件内容 = %q，期望 %q", got, want)
	}
}

// TestSetLanguageAtRejectsEmptyLanguage 验证空语言被拒绝且不改动文件
func TestSetLanguageAtRejectsEmptyLanguage(t *testing.T) {
	original := "{\n  \"language\": \"en\"\n}\n"
	path := writeTemp(t, original)
	for _, lang := range []string{"", "   "} {
		if err := SetLanguageAt(path, lang); err == nil {
			t.Errorf("SetLanguageAt(%q) 应当报错", lang)
		}
	}
	if got := readFile(t, path); got != original {
		t.Errorf("被拒绝的写入不得改动文件: %q", got)
	}
}

// TestSetLanguageAtRejectsMalformedFile 验证非法 JSON 被如实报错且原文件原样保留
//
// 取向是"看不懂就不动"：覆盖式重写会把用户手写的内容整体丢弃，
// 而这份文件恰恰是用户唯一的手工配置入口
func TestSetLanguageAtRejectsMalformedFile(t *testing.T) {
	original := "{not json"
	path := writeTemp(t, original)
	if err := SetLanguageAt(path, "zh-CN"); err == nil {
		t.Error("非法 JSON 应当报错")
	}
	if got := readFile(t, path); got != original {
		t.Errorf("非法文件不得被改写: %q", got)
	}

	// 顶层是数组同样拒绝：本包的读取路径要求顶层为对象，写入也不该把它改成对象
	arrayPath := writeTemp(t, `["a"]`)
	if err := SetLanguageAt(arrayPath, "zh-CN"); err == nil {
		t.Error("顶层不是对象时应当报错")
	}
}

// TestSetLanguageAtPreservesFileMode 验证写回沿用目标文件原有的权限位
func TestSetLanguageAtPreservesFileMode(t *testing.T) {
	path := writeTemp(t, `{"language": "en"}`)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("调整权限失败: %v", err)
	}
	if err := SetLanguageAt(path, "zh-CN"); err != nil {
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

// TestSetLanguageAtLeavesNoTempFile 验证成功写入后不留临时文件
//
// 原子写会先建同目录临时文件，用例同时锁住"临时文件已随 rename 消失"这一可见后果：
// 若 rename 没发生（例如误写成复制），配置目录里就会攒下一堆 .tmp
func TestSetLanguageAtLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flk-config.json")
	if err := SetLanguageAt(path, "zh-CN"); err != nil {
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

// TestWriteFileAtomicCleansTempOnFailure 验证落位失败时会清掉临时文件
//
// 手段是把目标路径做成一个非空目录：rename 到目录上必然失败，从而走到清理分支。
// 这一条必须单独守——失败路径上的临时文件不会被任何成功用例发现，
// 而它恰恰是用户最容易在"磁盘满了/权限不对"之后撞见的东西
func TestWriteFileAtomicCleansTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "flk-config.json")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatalf("准备非空目录失败: %v", err)
	}

	if err := writeFileAtomic(target, []byte("{}\n")); err == nil {
		t.Fatal("目标是非空目录时写入应当失败")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "flk-config.json" {
			t.Fatalf("失败后残留了临时文件 %q", e.Name())
		}
	}
}

// TestSetLanguageWritesDefaultPath 验证默认路径入口真的落在 ~/.config/flk/flk-config.json
//
// 前因：WebUI 切换语言调用的正是 SetLanguage（无路径版本），它的路径拼接一旦与读取侧
// 不一致（例如少一个 flk 子目录），表现就是"切换成功但重启后又变回英文"，
// 而这种缺陷在只测 SetLanguageAt 的用例里完全看不见。因此这里用隔离的 HOME
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

// TestSetLanguageAtFollowsSymlink 守住「设置文件是符号链接时，写入必须落在链接指向的真实文件上」
//
// 回归背景：写入走「同目录临时文件 + rename」实现原子落盘，而 rename 替换的是**路径上的那个名字**。
// 用户若把设置文件链进自己的配置仓库（与 flk-store.json 同一种用法），直接按链接路径写入
// 会把链接本身换成普通文件，仓库侧与 ~/.config 下的入口从此脱钩——这个破坏是静默的，
// 直到下次同步才会发现两边各写各的，因此用测试把契约钉死
func TestSetLanguageAtFollowsSymlink(t *testing.T) {
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

	if err := SetLanguageAt(linkPath, "zh-CN"); err != nil {
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
