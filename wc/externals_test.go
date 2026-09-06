package wc

import (
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestParseExternals(t *testing.T) {
	definitions, err := ParseExternals(`# comment
-r 7 ^/libs/core@9 "vendor/core lib"
legacy -r8 ../legacy@10
https://example.test/repository/tool@HEAD tool
^/dated@{2024-01-01} dated
^/escaped tool\ path
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 5 {
		t.Fatalf("definitions = %#v", definitions)
	}
	checks := []struct {
		local, remote string
		operative     svn.Revision
		peg           svn.Revision
	}{
		{"vendor/core lib", "^/libs/core", svn.Revision{Kind: svn.RevisionNumber, Number: 7}, svn.Revision{Kind: svn.RevisionNumber, Number: 9}},
		{"legacy", "../legacy", svn.Revision{Kind: svn.RevisionNumber, Number: 8}, svn.Revision{Kind: svn.RevisionNumber, Number: 10}},
		{"tool", "https://example.test/repository/tool", svn.Revision{}, svn.Revision{Kind: svn.RevisionHead}},
		{"dated", "^/dated", svn.Revision{}, mustRevision(t, "{2024-01-01}")},
		{"tool path", "^/escaped", svn.Revision{}, svn.Revision{}},
	}
	for index, want := range checks {
		got := definitions[index]
		if got.LocalPath != want.local || got.URL != want.remote || got.OperativeRevision != want.operative || got.PegRevision != want.peg {
			t.Errorf("definition %d = %#v, want %#v", index, got, want)
		}
	}
}

func mustRevision(t *testing.T, value string) svn.Revision {
	t.Helper()
	revision, err := svn.ParseRevision(value)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestParseExternalsRejectsInvalidDefinitions(t *testing.T) {
	for _, value := range []string{"^/remote", "^/remote ../escape", `^/remote "unterminated`, "-r nope ^/remote local"} {
		if _, err := ParseExternals(value); err == nil {
			t.Errorf("ParseExternals(%q) succeeded", value)
		}
	}
}
