package client

import (
	"bytes"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestEmitDiffLabelsAbsentSidesAsNonexistent(t *testing.T) {
	left := map[string]diffNode{
		"deleted": {kind: svn.NodeFile, content: []byte("old\n")},
	}
	right := map[string]diffNode{
		"added": {kind: svn.NodeFile, content: []byte("new\n")},
	}
	var output bytes.Buffer
	if err := emitDiff(&output, left, right, 1, 2, DiffOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"--- added\t(nonexistent)\n+++ added\t(revision 2)\n",
		"--- deleted\t(revision 1)\n+++ deleted\t(nonexistent)\n",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("diff does not contain %q:\n%s", expected, output.String())
		}
	}
}

func TestEmitGitDiffAndPatchCompatible(t *testing.T) {
	right := map[string]diffNode{
		"script": {
			kind: svn.NodeFile, content: []byte("echo ok\n"), repositoryPath: "trunk/script",
			properties: svn.Props{"svn:executable": []byte("*")},
		},
	}
	var output bytes.Buffer
	if err := emitDiff(&output, nil, right, 1, svn.InvalidRevnum, DiffOptions{Git: true}, nil); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"diff --git a/trunk/script b/trunk/script\n",
		"new file mode 100755\n",
		"--- a/trunk/script\t(nonexistent)\n+++ b/trunk/script\t(working copy)\n",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("git diff does not contain %q:\n%s", expected, output.String())
		}
	}

	left := map[string]diffNode{".": {kind: svn.NodeDir}}
	right = map[string]diffNode{".": {kind: svn.NodeDir, properties: svn.Props{"custom:p": []byte("value")}}}
	output.Reset()
	if err := emitDiff(&output, left, right, 1, 2, DiffOptions{PatchCompatible: true}, nil); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("patch-compatible property output=%q", output.String())
	}
}
