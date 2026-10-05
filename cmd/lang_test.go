package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/flk/internal/locales"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// 本文件覆盖 cmd/lang.go 的两条链路，各自对应一次真实缺陷：
//   - 语言取值：scanLangFlag / chooseLanguage（「--help 出现在 --lang 之前导致语言失效」的回归防线）
//   - 文案翻译：localizeTree（「持久化 flag 的说明始终不翻译」的回归防线）
//
// 为什么用同包单测而不只靠 main_test.go 的子进程用例：
//   - 根因位于 scanLangFlag 的原始参数预扫描，直接对函数做表驱动断言，能精确指出是哪一段参数被丢弃
//   - 取值优先级（--lang/-l > 设置文件 > 默认）由 chooseLanguage 一处决定，
//     同包可直接调用并逐级断言，不必为每一级都起一个子进程
//   - localizeTree 之外还有 DefValue / annotation 这类「翻译不该碰」的字段，只有同包才方便直接断言
//
// 契约（与 cmd/record_test.go、cmd/selection_test.go 同一套）：
//   - 用例只读环境变量与临时目录，不落盘到真实用户目录
//   - 被改动的进程级状态（os.Args、HOME 以及 l10n 的 localizer）
//     一律用 t.Setenv / t.Cleanup 还原，子用例串行执行，不开启 t.Parallel

// TestScanLangFlagIsOrderIndependent 锁定「--help/-h 无论出现在 --lang/-l 之前还是之后，语言取值都必须被扫到」
//
// 回归背景（本用例要守住的核心缺陷）：pflag v1.0.10 对未定义的 --help/-h 有专门分支，
// 会立即 usage() 并返回 ErrHelp 中断解析，而该分支排在 ParseErrorsWhitelist.UnknownFlags 白名单判断之前，
// 于是 --help 之后出现的 --lang/-l 全部丢失，表现为 `flk serve --help --lang zh-CN` 输出英文
// （只有把 --lang 写在 --help 之前才生效）。scanLangFlag 中为帮助参数注册占位 flag 即为修此问题，
// 因此下面同时保留「修复前的可用顺序」与「修复前失效的顺序」两组用例，防止后续重构再次引入顺序依赖
func TestScanLangFlagIsOrderIndependent(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		// 缺陷复现顺序：帮助参数在前，语言参数在后
		{name: "长帮助在语言之前", args: []string{"serve", "--help", "--lang", "zh-CN"}, want: "zh-CN"},
		{name: "短帮助在语言之前", args: []string{"serve", "-h", "-l", "zh-CN"}, want: "zh-CN"},
		{name: "帮助在等号写法之前", args: []string{"--help", "--lang=zh-CN"}, want: "zh-CN"},
		{name: "帮助在贴身写法之前", args: []string{"-h", "-lzh-CN"}, want: "zh-CN"},
		{name: "帮助在子命令之后与语言之前", args: []string{"create", "symlink", "--help", "-l", "zh-CN"}, want: "zh-CN"},

		// 修复前就可用、修复后必须继续有效的顺序
		{name: "语言在长帮助之前", args: []string{"serve", "--lang", "zh-CN", "--help"}, want: "zh-CN"},
		{name: "语言在短帮助之前", args: []string{"serve", "-l", "zh-CN", "-h"}, want: "zh-CN"},

		// 仅帮助时不应凭空产生语言，否则会把 l10n 的默认语言逻辑掩盖掉
		{name: "只有长帮助", args: []string{"serve", "--help"}, want: ""},
		{name: "只有短帮助", args: []string{"-h"}, want: ""},

		// 语言缺值：pflag 报错后停止解析，此时不得把后续内容误当成语言
		{name: "长语言在末尾缺值", args: []string{"serve", "--help", "--lang"}, want: ""},
		{name: "短语言在末尾缺值", args: []string{"serve", "-h", "-l"}, want: ""},

		// -- 之后属于位置参数而非 flag，这是 scanLangFlag 依赖 pflag 的既有语义，不能被本次修改动摇
		{name: "分隔符之后不算 flag", args: []string{"serve", "--", "--help", "--lang", "zh-CN"}, want: ""},
		{name: "分隔符之前仍生效", args: []string{"serve", "--lang", "zh-CN", "--", "--help"}, want: "zh-CN"},

		// 未知 flag 的「剥离取值」行为必须保持：子命令的 flag 取值不能被误读成语言
		{name: "跳过未知 flag 的取值", args: []string{"create", "copy", "--src", "zh-CN", "--help", "--lang", "en"}, want: "en"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := scanLangFlag(testCase.args); got != testCase.want {
				t.Fatalf("scanLangFlag(%q) = %q，期望 %q", testCase.args, got, testCase.want)
			}
		})
	}
}

// TestChooseLanguagePrecedence 锁定取值优先级：--lang/-l > 设置文件 language 字段 > 空串（交由 l10n 回退处理）
//
// 环境变量 FLK_LANG 已**彻底移除**，下面刻意保留两条"设了环境变量也必须无效"的用例：
// 少了它们，日后有人图省事再往 chooseLanguage 里加回一次 os.Getenv，
// 用例会照常全绿，而这条"配置类环境变量一律不要"的取向就被悄悄破坏了
//
// 每个子用例都必须同时隔离 os.Args 与 HOME：
//   - chooseLanguage 直接读 os.Args[1:]，签名不接受注入参数（改签名需要连带修改 cmd/root.go 的调用点，超出本次范围）
//   - 未传 --lang 时会去读 ~/.config/flk/flk-config.json，
//     不隔离 HOME 就会读到开发者本机的真实设置，用例结果将不可复现
//
// 断言口径是「上一级存在时取上一级、缺失时才降级」，避免只覆盖到其中一条分支
func TestChooseLanguagePrecedence(t *testing.T) {
	tests := []struct {
		name string
		args []string
		// envLang 是刻意设置的 FLK_LANG 取值，用来证明该环境变量已失效（空串表示不设）
		envLang     string
		writeConfig bool
		want        string
	}{
		{
			name:        "命令行长写在帮助之前并覆盖设置文件",
			args:        []string{"flk", "serve", "--help", "--lang", "zh-CN"},
			writeConfig: true,
			want:        "zh-CN",
		},
		{
			name:        "命令行短写在帮助之后同样覆盖设置文件",
			args:        []string{"flk", "serve", "-h", "-l", "zh-CN"},
			writeConfig: true,
			want:        "zh-CN",
		},
		{
			name:        "无命令行语言时取设置文件",
			args:        []string{"flk", "serve", "--help"},
			writeConfig: true,
			want:        "zh-CN",
		},
		{
			name:        "全部缺失时返回空串交由 l10n 使用默认语言",
			args:        []string{"flk", "--help"},
			writeConfig: false,
			want:        "",
		},
		{
			name:        "环境变量 FLK_LANG 不再有任何效果",
			args:        []string{"flk", "--help"},
			envLang:     "zh-CN",
			writeConfig: false,
			want:        "",
		},
		{
			name:        "设置文件优先于环境变量",
			args:        []string{"flk", "--help"},
			envLang:     "en",
			writeConfig: true,
			want:        "zh-CN",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			// HOME 每个子用例独立，设置文件与真实用户目录彻底隔离
			home := t.TempDir()
			t.Setenv("HOME", home)
			// 显式写入（即使是空串）可屏蔽调用者 shell 里残留的取值，保证用例可复现。
			// 这里刻意用字面量而不是常量：常量已随环境变量层一起删除，写死才能守住"它不该再被读"
			t.Setenv("FLK_LANG", testCase.envLang)

			if testCase.writeConfig {
				configDir := filepath.Join(home, ".config", "flk")
				if err := os.MkdirAll(configDir, 0o755); err != nil {
					t.Fatalf("创建临时设置目录失败: %v", err)
				}
				configPath := filepath.Join(configDir, "flk-config.json")
				if err := os.WriteFile(configPath, []byte(`{"language":"zh-CN"}`), 0o600); err != nil {
					t.Fatalf("写入临时设置文件失败: %v", err)
				}
			}

			previousArgs := os.Args
			os.Args = testCase.args
			t.Cleanup(func() { os.Args = previousArgs })

			if got := chooseLanguage(); got != testCase.want {
				t.Fatalf("chooseLanguage() = %q，期望 %q（args=%q env=%q writeConfig=%v）",
					got, testCase.want, testCase.args, testCase.envLang, testCase.writeConfig)
			}
		})
	}
}

// containsCJK 判断字符串里是否含中日韩统一表意文字，用来确认说明确实变成了中文
//
// 用「是否含汉字」而不是比对具体译文：译文措辞会随翻译迭代改动，
// 把用例钉在某一句话上会让它跟着翻译一起变红，而这里真正要守住的是「这句话被翻译过了」
func containsCJK(text string) bool {
	for _, r := range text {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

// TestLocalizeTreeTranslatesPersistentFlags 守住「用 PersistentFlags 声明的 flag 说明也会被翻译」
//
// 回归背景：localizeTree 原先只遍历 cmd.Flags()，而语言在 cobra 解析之前就已确定，
// 那一刻持久化 flag 还没被合并进 Flags()（合并发生在 ParseFlags / InitDefaultHelpFlag 里），
// 于是 flk 的全部全局 flag（--lang/--output/--yes…）与 serve 的 --host/-p 在 zh-CN 下恒为英文
//
// 为什么用一次性的小命令树而不是直接对 rootCmd 调 localizeTree：
// localizeTree 是就地改写文本的，作用在包级命令树上会让本包其它用例看到被改过的帮助文案，
// 用例成败从此依赖执行顺序；造一棵小树既验证了递归与两类 flag 集，又不给全局状态留痕迹
//
// 为什么必须先 l10n.Init 再还原：localizer 是进程级全局，本包其它用例
// （selection_test.go 的警告按英文前缀计数）依赖当前语言，因此无论成败都要在 t.Cleanup 里
// 还原成进入用例前的语言；previous 为空串时会回落到默认英文，与「未 Init 时 T 返回源串」等价
func TestLocalizeTreeTranslatesPersistentFlags(t *testing.T) {
	previous := l10n.Current()
	t.Cleanup(func() {
		if err := l10n.Init(previous, locales.Options()); err != nil {
			t.Errorf("还原 localizer 失败: %v", err)
		}
	})
	if err := l10n.Init("zh-CN", locales.Options()); err != nil {
		t.Fatalf("初始化 zh-CN localizer 失败: %v", err)
	}

	parent := &cobra.Command{Use: "parent"}
	child := &cobra.Command{Use: "child"}
	parent.AddCommand(child)

	// 三种声明方式都要覆盖，缺任何一条都会漏掉一类真实的 flag：
	// 父命令的持久化 flag（对应 flk 的全局 flag）、子命令自己的持久化 flag（cobra 里共享 flag 的常见写法）、
	// 子命令的普通 flag（对应 serve 的 --port/--host，作为「修复前就可用」的对照）
	parentUsage := "Path to the flk-store.json file"
	parent.PersistentFlags().String("parent-global", "~/.config/flk/flk-store.json", parentUsage)
	childGlobalUsage := "Host to bind"
	child.PersistentFlags().String("child-global", "", childGlobalUsage)
	childLocalUsage := "Output format: json/table"
	child.Flags().String("child-local", "table", childLocalUsage)

	// 语言文件里不存在的说明，用于验证缺失译文时原样回落源串而不是变成空串
	missingUsage := "help for a command that is intentionally absent from the locale files"
	child.Flags().String("child-missing", "", missingUsage)

	// annotation 与 DefValue 属于解析语义与默认值展示，翻译一律不得触碰，
	// 先埋一个自定义 annotation，翻译之后必须仍然在
	if err := parent.PersistentFlags().SetAnnotation("parent-global", "flk/test-annotation", []string{"kept"}); err != nil {
		t.Fatalf("写入测试 annotation 失败: %v", err)
	}

	// 快照必须与本棵树绑定：传 newTreeTexts() 而不是复用包级 rootTexts，
	// 否则会把根命令树的英文原文错记到这条临时命令树上（键是指针，两棵树毫无关系）
	snapshot := newTreeTexts()
	localizeTree(parent, snapshot)

	translated := []struct {
		name     string
		flag     *pflag.Flag
		original string
	}{
		{name: "父命令持久化 flag", flag: parent.PersistentFlags().Lookup("parent-global"), original: parentUsage},
		{name: "子命令持久化 flag", flag: child.PersistentFlags().Lookup("child-global"), original: childGlobalUsage},
		{name: "子命令普通 flag", flag: child.Flags().Lookup("child-local"), original: childLocalUsage},
	}
	for _, item := range translated {
		if item.flag == nil {
			t.Fatalf("%s 没有注册成功，用例自身需要修正", item.name)
		}
		if item.flag.Usage == item.original {
			t.Fatalf("%s 的说明未被翻译，仍是英文源串 %q", item.name, item.original)
		}
		if !containsCJK(item.flag.Usage) {
			t.Fatalf("%s 的说明没有变成中文: %q", item.name, item.flag.Usage)
		}
	}

	if got := child.Flags().Lookup("child-missing").Usage; got != missingUsage {
		t.Fatalf("缺译文的说明应原样回落源串，实际为 %q", got)
	}
	if got := child.Flags().Lookup("child-local").DefValue; got != "table" {
		t.Fatalf("普通 flag 的 DefValue 被翻译改动: %q", got)
	}
	if got := parent.PersistentFlags().Lookup("parent-global").DefValue; got != "~/.config/flk/flk-store.json" {
		t.Fatalf("持久化 flag 的 DefValue 被翻译改动: %q", got)
	}
	if annotations := parent.PersistentFlags().Lookup("parent-global").Annotations; annotations["flk/test-annotation"] == nil {
		t.Fatalf("持久化 flag 的 annotation 在翻译后丢失: %#v", annotations)
	}

	// 再走一遍 localizeTree，所有说明必须逐字不变
	// 这条断言正面回答「同一 flag 会不会被重复翻译」：新版实现是"先按英文快照还原、再翻译"，
	// 而快照里存的是英文源串，因此第二次遍历的结果与第一次必然逐字相同；
	// 若将来有人在遍历里放进非幂等操作（例如把当前译文再当一次源串），这里会立刻变红
	before := make(map[string]string)
	for _, item := range translated {
		before[item.name] = item.flag.Usage
	}
	localizeTree(parent, snapshot)
	for _, item := range translated {
		if item.flag.Usage != before[item.name] {
			t.Fatalf("%s 在第二次遍历时被改动: %q -> %q", item.name, before[item.name], item.flag.Usage)
		}
	}
}

// TestRelocalizeCommandsOnRealTree 用**真实的包级命令树**验证 serve 切换语言时的那条路径。
//
// 为什么必须单独有这一条：上面两个用例都用临时命令树，证明的是 localizeTree 的算法正确；
// 而 WebUI 切换语言调用的入口是 relocalizeCommands()（见 cmd/serve_web.go 的 /api/language），
// 它绑定的是包级 rootCmd 与包级快照。两者之间还有几处只有真树才会暴露的细节：
//   - 真树里的文案必须能在语言文件里查到译文（用例里的英文源串是手抄的，抄错也不会报错）
//   - 真树上跑过多轮重译后，快照与树不能错位（错位只会表现为"某次切换后命令树没变"）
//   - 持久化 flag 由根命令声明，翻译它需要父命令的 PersistentFlags() 路径
//
// 收尾必须还原：本用例就地改写了包级命令树，不还原会让同包其它用例看到中文帮助文案，
// 用例成败从此依赖执行顺序。还原方式与真实流程一致（切回英文 + 重译），不使用特例代码
func TestRelocalizeCommandsOnRealTree(t *testing.T) {
	previous := l10n.Current()
	t.Cleanup(func() {
		if err := l10n.Init(previous, locales.Options()); err != nil {
			t.Errorf("还原 localizer 失败: %v", err)
			return
		}
		relocalizeCommands()
	})
	if err := l10n.Init("en", locales.Options()); err != nil {
		t.Fatalf("初始化 en localizer 失败: %v", err)
	}
	relocalizeCommands()

	const (
		serveShort    = "Open the WebUI management panel in a browser"
		storePathFlag = "Path to the flk-store.json file"
		portFlag      = "Port to listen on"
	)
	read := func() map[string]string {
		return map[string]string{
			"serve.Short":     serveCmd.Short,
			"全局 --store-path": rootCmd.PersistentFlags().Lookup("store-path").Usage,
			"serve --port":    serveCmd.Flags().Lookup("port").Usage,
		}
	}

	if got := read(); got["serve.Short"] != serveShort {
		t.Fatalf("英文初始化后 serve.Short = %q，期望 %q（命令树的英文源串与语言文件已不一致？）",
			got["serve.Short"], serveShort)
	}

	// 正序：en → zh-CN → en，逐轮断言。中文轮看重译是否真的发生，英文轮看是否逐字还原
	if err := l10n.SetLanguage("zh-CN"); err != nil {
		t.Fatalf("切换到 zh-CN 失败: %v", err)
	}
	relocalizeCommands()
	zh := read()
	for name, text := range zh {
		if !containsCJK(text) {
			t.Fatalf("切到 zh-CN 后 %s 未被翻译: %q", name, text)
		}
	}

	if err := l10n.SetLanguage("en"); err != nil {
		t.Fatalf("切回 en 失败: %v", err)
	}
	relocalizeCommands()
	en := read()
	want := map[string]string{
		"serve.Short":     serveShort,
		"全局 --store-path": storePathFlag,
		"serve --port":    portFlag,
	}
	for name, wantText := range want {
		if en[name] != wantText {
			t.Fatalf("切回 en 后 %s = %q，期望逐字还原为 %q", name, en[name], wantText)
		}
	}
}

// TestLocalizeTreeIsReversibleAcrossLanguages 守住「命令树可重复本地化」这条不变量。
//
// 回归背景（本次缺陷）：英文源串同时充当消息 id，而旧实现把译文就地写回
// cmd.Short/Long 与 flag.Usage，原串就此丢失；实测 en→zh→en 之后命令树与 flag 说明
// 仍是中文——因为回译时的入参已经是中文，查表必然落空。
//
// 修法是本地化前存一份英文快照，每次重译都"先还原快照、再翻译"。本用例用
// en→zh-CN→en→zh-CN 往复来守它，这是本次最容易出错的地方：
//   - 英文那一轮必须**逐字**等于最初的英文源串（丢一个字符都算不可逆）
//   - 中文那一轮必须真的含汉字（否则"还原成功"可能只是碰巧没翻译）
//
// 用 l10n.SetLanguage 切换而不是重新 Init：serve 的语言切换走的正是 SetLanguage
// （语言文件只在 Init 时解析一次，运行期不得重新解析），用例要对齐真实路径
func TestLocalizeTreeIsReversibleAcrossLanguages(t *testing.T) {
	previous := l10n.Current()
	t.Cleanup(func() {
		if err := l10n.Init(previous, locales.Options()); err != nil {
			t.Errorf("还原 localizer 失败: %v", err)
		}
	})
	if err := l10n.Init("en", locales.Options()); err != nil {
		t.Fatalf("初始化 en localizer 失败: %v", err)
	}

	// 用真实的英文源串（取自 internal/locales），确保"这里确实有译文"这一前提成立：
	// 自己编的串必然查不到译文，用例会退化成恒真
	const (
		cmdShort   = "Open the WebUI management panel in a browser"
		portUsage  = "Port to listen on"
		hostUsage  = "Host to bind"
		localUsage = "Output format: json/table"
	)
	english := map[string]string{
		"命令 Short":        cmdShort,
		"命令 flag port":    portUsage,
		"命令 flag host":    hostUsage,
		"根命令 flag output": localUsage,
	}

	root := &cobra.Command{Use: "root", Short: "flk is a cross-platform file link manager"}
	serve := &cobra.Command{Use: "serve", Short: cmdShort}
	serve.Flags().Int("port", 8999, portUsage)
	// 持久化 flag 也要覆盖：漏掉它正是「Global Flags 整段英文」那个历史缺陷的形态
	serve.PersistentFlags().String("host", "127.0.0.1", hostUsage)
	root.AddCommand(serve)
	root.PersistentFlags().String("output", "table", localUsage)

	snapshot := newTreeTexts()
	// 往复两轮：单次 en→zh→en 只能证明"回到英文"，
	// 连续两轮才能暴露"第二轮回中文时会不会因为快照被污染而失灵"
	rounds := []struct {
		lang    string
		chinese bool
	}{
		{lang: "zh-CN", chinese: true},
		{lang: "en"},
		{lang: "zh-CN", chinese: true},
		{lang: "en"},
	}

	for round, c := range rounds {
		if err := l10n.SetLanguage(c.lang); err != nil {
			t.Fatalf("第 %d 轮切换到 %s 失败: %v", round+1, c.lang, err)
		}
		localizeTree(root, snapshot)

		got := map[string]string{
			"命令 Short":        serve.Short,
			"命令 flag port":    serve.Flags().Lookup("port").Usage,
			"命令 flag host":    serve.PersistentFlags().Lookup("host").Usage,
			"根命令 flag output": root.PersistentFlags().Lookup("output").Usage,
		}
		for name, wantText := range english {
			if c.chinese {
				// 中文这一轮只断言"被翻译过"：把用例钉在具体译文的措辞上，
				// 翻译迭代时它会跟着变红，而这里真正要守的是"这句话会跟着语言变"
				if !containsCJK(got[name]) {
					t.Fatalf("第 %d 轮（%s）%s 未被翻译成中文: %q", round+1, c.lang, name, got[name])
				}
				continue
			}
			// 英文这一轮必须逐字还原，这是可逆性的核心断言
			if got[name] != wantText {
				t.Fatalf("第 %d 轮（%s）%s 未还原成英文源串:\n  实得 %q\n  期望 %q",
					round+1, c.lang, name, got[name], wantText)
			}
		}
	}
}
