package hardlink

import (
	"os"
	"testing"
)

func TestCreate(t *testing.T) {
	// Create temp file
	prim := "temp.txt"
	err := os.WriteFile(prim, []byte("test"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(prim)

	seco := "link.txt"
	defer os.Remove(seco)

	err = Create(prim, seco, false)
	if err != nil {
		t.Error(err)
	}

	if _, err := os.Stat(seco); os.IsNotExist(err) {
		t.Error("Link not created")
	}
}
