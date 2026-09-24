package config

import (
	"os"
	"path/filepath"
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

// TestReadRawAtRejectsMalformed 验证损坏文件被如实报错，而不是静默当作空配置
func TestReadRawAtRejectsMalformed(t *testing.T) {
	path := writeTemp(t, "{not json")
	if _, err := ReadRawAt(path); err == nil {
		t.Error("损坏的 JSON 应当报错")
	}
}
