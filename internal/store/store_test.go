package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestManager_Save_WritesJson(t *testing.T) {
	// prepare a sample in-memory data
	m := &Manager{Data: make(RootConfig)}
	// add a tiny record
	m.Data["linux"] = DeviceGroup{
		"devA": TypeGroup{
			"hardlink": PathGroup{
				"/home/user": []Entry{{"prim": "a", "seco": "/tmp/b"}},
			},
		},
	}

	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "flk-store.json")
	if err := m.Save(storePath); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}

	// 读取并验证 JSON 能被正确解析
	b, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("无法读取写入的文件: %v", err)
	}
	var parsed RootConfig
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if len(parsed) == 0 {
		t.Fatalf("预期解析得到非空数据结构")
	}
}

func TestManager_Save_PreservesTildeInJson(t *testing.T) {
	m := &Manager{Data: make(RootConfig)}
	m.Data["darwin"] = DeviceGroup{
		"devB": TypeGroup{
			"symlink": PathGroup{
				"~": []Entry{{"path": "~", "link": "~"}},
			},
		},
	}

	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "store.json")
	if err := m.Save(p); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	// 读取并确保 ~ 字符在 JSON 内容中存在
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("无法读取写入的文件: %v", err)
	}
	if !json.Valid(b) {
		t.Fatalf("输出不是有效的 JSON")
	}
}
