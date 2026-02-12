package logger

import "testing"

func TestInit(t *testing.T) {
	Init(nil)
	// No panic expected
}
