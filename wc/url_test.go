package wc

import (
	"testing"

	svnpath "github.com/otuschhoff/go-svn/svn/path"
)

func fileURL(t testing.TB, name string) string {
	t.Helper()
	value, err := svnpath.DirentToFileURL(name)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
