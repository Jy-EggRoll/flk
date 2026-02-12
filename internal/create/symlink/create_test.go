package symlink

import (
	"os"
	"runtime"
	"testing"
)

func TestCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skip symlink test on Windows")
	}

	// Create temp file
	real := "temp.txt"
	err := os.WriteFile(real, []byte("test"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(real)

	fake := "sym.txt"
	defer os.Remove(fake)

	err = Create(real, fake, false)
	if err != nil {
		t.Error(err)
	}

	if _, err := os.Lstat(fake); os.IsNotExist(err) {
		t.Error("Symlink not created")
	}
}
