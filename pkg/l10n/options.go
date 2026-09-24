package l10n

import (
	"errors"
	"io/fs"
)

// Options 描述一个项目的多语言配置。除 FS 外都应有确定的值，
// 全部缺失会让 Init 直接报错——静默用一个空配置跑起来只会让问题推迟到运行期。
type Options struct {
	// Default 是默认语言标签，同时也是缺失译文的最终回退层。
	// 语言文件 <Default>.json 是生成物（由 pkg/l10n/cmd/l10n export 写入的自映射）。
	Default string

	// Supported 是随二进制发布的语言列表。
	//
	// **顺序是语义的一部分**：go-i18n 的 language.Matcher 按语言标签的注册顺序
	// 挑选最匹配项，依赖随机顺序会让"请求 zh 时落到哪个标签"变得不确定。
	// 通常把 Default 放在首位。
	Supported []string

	// FS 是语言文件所在的文件系统，通常由调用方 //go:embed 提供。
	FS fs.FS

	// Dir 是语言文件在 FS 内的子目录，空表示直接位于 FS 根。
	//
	// 文件路径必须是 "<Dir>/<tag>.json" 的形态：go-i18n 是从**文件路径**反推语言
	// 标签的（见 go-i18n 的 parsePath），改了形态会让标签解析失败。
	Dir string
}

// validate 检查 Options 是否可用。
func (o Options) validate() error {
	if o.Default == "" {
		return errors.New("l10n: Options.Default must not be empty")
	}
	if len(o.Supported) == 0 {
		return errors.New("l10n: Options.Supported must not be empty")
	}
	if o.FS == nil {
		return errors.New("l10n: Options.FS must not be nil (usually a //go:embed filesystem)")
	}

	seen := make(map[string]bool, len(o.Supported))
	foundDefault := false
	for _, tag := range o.Supported {
		if tag == "" {
			return errors.New("l10n: Options.Supported must not contain an empty tag")
		}
		if seen[tag] {
			return errors.New("l10n: Options.Supported contains duplicate tag " + tag)
		}
		seen[tag] = true
		if tag == o.Default {
			foundDefault = true
		}
	}
	if !foundDefault {
		// Default 不在 Supported 里的话，回退链的最后一环就没有对应文件，
		// 任何漏翻都会变成"显示源串"——看起来能跑，实则默认语言形同虚设
		return errors.New("l10n: Options.Default (" + o.Default + ") must be one of Options.Supported")
	}
	return nil
}
