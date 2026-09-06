package diff

import (
	"reflect"
	"testing"
)

func TestUnifiedContextAndRanges(t *testing.T) {
	oldData := []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n")
	newData := []byte("one\ntwo changed\nthree\nfour\nfive\nsix\nseven\neight changed\nnine\n")
	want := "--- old\n+++ new\n@@ -1,9 +1,9 @@\n one\n-two\n+two changed\n three\n four\n five\n six\n seven\n-eight\n+eight changed\n nine\n"
	if got := string(Unified(oldData, newData, "old", "new", Options{})); got != want {
		t.Fatalf("unified diff:\n%s\nwant:\n%s", got, want)
	}
}

func TestOperationsHonorWhitespaceAndEOL(t *testing.T) {
	if got := Unified([]byte("a  b\r\n"), []byte("a b\n"), "old", "new", Options{}); len(got) == 0 {
		t.Fatal("exact diff ignored whitespace and EOL changes")
	}
	if got := Unified([]byte("a  b\r\n"), []byte("a b\n"), "old", "new", Options{IgnoreSpaceChange: true, IgnoreEOL: true}); len(got) != 0 {
		t.Fatalf("ignored diff=%q", got)
	}
}

func TestCarryOrigins(t *testing.T) {
	origins := CarryOrigins([]byte("one\ntwo\n"), []byte("one\nchanged\nthree\n"), []int{1, 1}, 2, Options{})
	if !reflect.DeepEqual(origins, []int{1, 2, 2}) {
		t.Fatalf("origins=%v", origins)
	}
}

func TestUnifiedPropertyFormat(t *testing.T) {
	got := string(UnifiedProperty(nil, []byte("alpha\nbeta")))
	want := "## -0,0 +1,2 ##\n+alpha\n+beta\n\\ No newline at end of property\n"
	if got != want {
		t.Fatalf("property diff:\n%s\nwant:\n%s", got, want)
	}
}
