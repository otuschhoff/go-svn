package delta

import (
	"bytes"
	"testing"
)

func TestBuildWindowWithRepeatedSource(t *testing.T) {
	source := bytes.Repeat([]byte("a"), MaxWindowSize)
	target := append(bytes.Repeat([]byte("a"), MaxWindowSize/2), bytes.Repeat([]byte("b"), MaxWindowSize/2)...)
	window := buildWindow(source, target, 0)
	actual, err := ApplyWindow(source, window)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, target) {
		t.Fatal("generated window did not reproduce repeated target")
	}
	if len(window.Ops) > 3 {
		t.Fatalf("generated %d operations for repeated input", len(window.Ops))
	}
}
