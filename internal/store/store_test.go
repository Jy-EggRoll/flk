package store

import (
	"runtime"
	"testing"

	"github.com/jy-eggroll/flk/internal/logger"
)

func TestAddRecord(t *testing.T) {
	logger.Init(nil)
	platform := runtime.GOOS
	mgr := &Manager{
		Data: make(RootConfig),
	}
	fields := map[string]string{"prim": "a", "seco": "b"}
	mgr.AddRecord("test", "hardlink", "/tmp", fields)
	if len(mgr.Data[platform]["test"]["hardlink"]["/tmp"]) != 1 {
		t.Error("Record not added")
	}
}
