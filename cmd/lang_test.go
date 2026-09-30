package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jy-eggroll/flk/internal/locales"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// 本文件覆盖 cmd/lang.go 的两条链路，各自对应一次真实缺陷：
//   - 语言取值：scanLangFlag / chooseLanguage（「--help 出现在 --lang 之前导致语言失效」的回归防线）
//   - 文案翻译：localizeTree（「持久化 flag 的说明始终不翻译」的回归防线）
//
// 为什么用同包单测而不只靠 main_test.go 的子进程用例：
//   - 根因位于 scanLangFlag 的原始参数预扫描，直接对函数做表驱动断言，能精确指出是哪一段参数被丢弃
//   - 取值优先级（--lang/-l > FLK_LANG > 设置文件 > 默认）由 chooseLanguage 一处决定，
//     同包可直接调用并逐级断言，不必为每一级都起一个子进程
//   - localizeTree 之外还有 DefValue / annotation 这类「翻译不该碰」的字段，只有同包才方便直接断言
//
// 契约（与 cmd/record_test.go、cmd/selection_test.go 同一套）：
//   - 用例只读环境变量与临时目录，不落盘到真实用户目录
//   - 被改动的进程级状态（os.Args、FLK_LANG、HOME 以及 l10n 的 localizer）
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

// TestChooseLanguagePrecedence 锁定取值优先级：--lang/-l > 环境变量 FLK_LANG > 设置文件 language 字段 > 空串（交由 l10n 兜底）
//
// 每个子用例都必须同时隔离 os.Args 与 HOME：
//   - chooseLanguage 直接读 os.Args[1:]，签名不接受注入参数（改签名需要连带修改 cmd/root.go 的调用点，超出本次范围）
//   - 未设 FLK_LANG 且命令行无 --lang 时会去读 ~/.config/flk/flk-config.json，
//     不隔离 HOME 就会读到开发者本机的真实设置，用例结果将不可复现
//
// 断言口径是「上一级存在时取上一级、缺失时才降级」，避免只覆盖到其中一条分支
func TestChooseLanguagePrecedence(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		env         string
		writeConfig bool
		want        string
	}{
		{
			name:        "命令行长写在帮助之前并覆盖环境变量与设置文件",
			args:        []string{"flk", "serve", "--help", "--lang", "zh-CN"},
			env:         "en",
			writeConfig: true,
			want:        "zh-CN",
		},
		{
			name:        "命令行短写在帮助之后同样覆盖环境变量",
			args:        []string{"flk", "serve", "-h", "-l", "zh-CN"},
			env:         "en",
			writeConfig: true,
			want:        "zh-CN",
		},
		{
			name:        "无命令行语言时取环境变量 FLK_LANG",
			args:        []string{"flk", "serve", "--help"},
			env:         "zh-CN",
			writeConfig: true,
			want:        "zh-CN",
		},
		{
			name:        "无命令行语言且无环境变量时取设置文件",
			args:        []string{"flk", "--help"},
			env:         "",
			writeConfig: true,
			want:        "zh-CN",
		},
		{
			name:        "全部缺失时返回空串交由 l10n 使用默认语言",
			args:        []string{"flk", "--help"},
			env:         "",
			writeConfig: false,
			want:        "",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			// HOME 每个子用例独立，设置文件与真实用户目录彻底隔离
			home := t.TempDir()
			t.Setenv("HOME", home)
			// 显式写入（即使是空串）可屏蔽调用者 shell 里残留的 FLK_LANG，保证用例可复现
			t.Setenv(langEnv, testCase.env)

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
					got, testCase.want, testCase.args, testCase.env, testCase.writeConfig)
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
	// 父命令的持久化 flag（对应 flk 的全局 flag）、子命令自己的持久化 flag（对应 serve 的 --host/-p）、
	// 子命令的普通 flag（修复前就可用，作为「没被改坏」的对照）
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

	localizeTree(parent)

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
	// 这条断言正面回答「同一 flag 会不会被重复翻译」：译文再查一次必然无命中（源串即 key，
	// 译文不是 key），因此重复遍历必须是无副作用的；若将来有人在遍历里塞进非幂等操作，这里会立刻变红
	before := make(map[string]string)
	for _, item := range translated {
		before[item.name] = item.flag.Usage
	}
	localizeTree(parent)
	for _, item := range translated {
		if item.flag.Usage != before[item.name] {
			t.Fatalf("%s 在第二次遍历时被改动: %q -> %q", item.name, before[item.name], item.flag.Usage)
		}
	}
}
