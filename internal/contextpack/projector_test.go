package contextpack

import (
	"strings"
	"testing"
)

func TestMiddleOut(t *testing.T) {
	input := strings.Repeat("a", 1000)
	got := middleOut(input, 100)
	if len(got) > 100 {
		t.Fatalf("len=%d", len(got))
	}
	if !strings.Contains(got, "HarnessMesh middle truncation") {
		t.Fatalf("missing marker")
	}
}
