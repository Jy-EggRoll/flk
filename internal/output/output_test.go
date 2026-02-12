package output

import "testing"

func TestOutputFormat(t *testing.T) {
	if OutputFormat("json") != JSON {
		t.Errorf("Expected JSON, got %v", OutputFormat("json"))
	}
	if OutputFormat("table") != Table {
		t.Errorf("Expected Table, got %v", OutputFormat("table"))
	}
	if OutputFormat("plain") != Plain {
		t.Errorf("Expected Plain, got %v", OutputFormat("plain"))
	}
}
