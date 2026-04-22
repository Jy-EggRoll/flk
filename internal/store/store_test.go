package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jy-eggroll/flk/internal/logger"
)

func TestMain(m *testing.M) {
	logger.Init(nil)
	m.Run()
}

func TestAddRecord_Symlink(t *testing.T) {
	m := &Manager{Data: make(RootConfig)}
	m.AddRecord("device1", "symlink", "/tmp/test", map[string]string{
		"real": "/tmp/test/real",
		"fake": "/tmp/test/link",
	})

	platform := runtime.GOOS
	entries := m.Data[platform]["device1"]["symlink"]["/tmp/test"]
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0]["real"] == "" {
		t.Fatal("real field should not be empty")
	}
}

func TestAddRecord_Hardlink(t *testing.T) {
	m := &Manager{Data: make(RootConfig)}
	m.AddRecord("device1", "hardlink", "/tmp/test", map[string]string{
		"prim": "/tmp/test/prim",
		"seco": "/tmp/test/seco",
	})

	platform := runtime.GOOS
	entries := m.Data[platform]["device1"]["hardlink"]["/tmp/test"]
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0]["prim"] == "" {
		t.Fatal("prim field should not be empty")
	}
}

func TestAddRecord_DedupSymlink(t *testing.T) {
	m := &Manager{Data: make(RootConfig)}
	m.AddRecord("device1", "symlink", "/tmp/test", map[string]string{
		"real": "/tmp/test/real1",
		"fake": "/tmp/test/link",
	})
	m.AddRecord("device1", "symlink", "/tmp/test", map[string]string{
		"real": "/tmp/test/real2",
		"fake": "/tmp/test/link",
	})

	platform := runtime.GOOS
	entries := m.Data[platform]["device1"]["symlink"]["/tmp/test"]
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after dedup, got %d", len(entries))
	}
	if entries[0]["real"] != "real2" {
		t.Fatalf("expected real to be updated to real2, got %s", entries[0]["real"])
	}
}

func TestAddRecord_DifferentFake(t *testing.T) {
	m := &Manager{Data: make(RootConfig)}
	m.AddRecord("device1", "symlink", "/tmp/test", map[string]string{
		"real": "/tmp/test/real1",
		"fake": "/tmp/test/link1",
	})
	m.AddRecord("device1", "symlink", "/tmp/test", map[string]string{
		"real": "/tmp/test/real2",
		"fake": "/tmp/test/link2",
	})

	platform := runtime.GOOS
	entries := m.Data[platform]["device1"]["symlink"]["/tmp/test"]
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries with different fake, got %d", len(entries))
	}
}

func TestRemoveMatchingEntry(t *testing.T) {
	m := &Manager{Data: make(RootConfig)}
	m.AddRecord("device1", "symlink", "/tmp/test", map[string]string{
		"real": "/tmp/test/real1",
		"fake": "/tmp/test/link1",
	})
	m.AddRecord("device1", "symlink", "/tmp/test", map[string]string{
		"real": "/tmp/test/real2",
		"fake": "/tmp/test/link2",
	})

	platform := runtime.GOOS
	entries := m.Data[platform]["device1"]["symlink"]["/tmp/test"]
	if len(entries) != 2 {
		t.Fatalf("setup: expected 2 entries, got %d", len(entries))
	}

	m.RemoveMatchingEntry(platform, "device1", "symlink", "/tmp/test", Entry{"fake": "/tmp/test/link1"})

	entries = m.Data[platform]["device1"]["symlink"]["/tmp/test"]
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after remove, got %d", len(entries))
	}
	if entries[0]["fake"] != "/tmp/test/link2" {
		t.Fatalf("expected remaining entry to be link2, got %s", entries[0]["fake"])
	}
}

func TestToJSON(t *testing.T) {
	m := &Manager{Data: make(RootConfig)}
	m.AddRecord("device1", "symlink", "/tmp/test", map[string]string{
		"real": "/tmp/test/real",
		"fake": "/tmp/test/link",
	})

	jsonStr := m.ToJSON()
	if jsonStr == "" {
		t.Fatal("ToJSON returned empty string")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("ToJSON output is not valid JSON: %v", err)
	}
}

func TestSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store.json")

	m1 := &Manager{Data: make(RootConfig)}
	m1.AddRecord("device1", "symlink", "/tmp/test", map[string]string{
		"real": "/tmp/test/real",
		"fake": "/tmp/test/link",
	})

	if err := m1.Save(storePath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	m2, err := LoadFromFile(storePath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	platform := runtime.GOOS
	if len(m2.Data[platform]["device1"]["symlink"]["/tmp/test"]) != 1 {
		t.Fatal("data not preserved after save/load")
	}
}

func TestSaveLoad_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "empty.json")

	if err := os.WriteFile(storePath, []byte(""), 0644); err != nil {
		t.Fatalf("create empty file failed: %v", err)
	}

	m, err := LoadFromFile(storePath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if m == nil {
		t.Fatal("Manager should not be nil")
	}
}
