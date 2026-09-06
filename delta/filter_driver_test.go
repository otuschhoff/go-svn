package delta

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestDepthFilterMatrix(t *testing.T) {
	tests := []struct {
		depth svn.Depth
		want  []string
	}{
		{svn.DepthEmpty, nil},
		{svn.DepthFiles, []string{"file"}},
		{svn.DepthImmediates, []string{"dir", "file"}},
		{svn.DepthInfinity, []string{"dir", "dir/nested", "file"}},
	}
	for _, test := range tests {
		t.Run(test.depth.String(), func(t *testing.T) {
			builder := NewTreeBuilder()
			editor := DepthFilter(builder, test.depth, "")
			ctx := context.Background()
			root, _ := editor.OpenRoot(ctx, 0)
			directory, err := root.AddDirectory(ctx, "dir", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = directory.AddFile(ctx, "dir/nested", nil)
			_, _ = root.AddFile(ctx, "file", nil)
			var got []string
			if node := builder.Root().Children["dir"]; node != nil {
				got = append(got, "dir")
				if node.Children["nested"] != nil {
					got = append(got, "dir/nested")
				}
			}
			if builder.Root().Children["file"] != nil {
				got = append(got, "file")
			}
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestDepthFilterReachesNestedTarget(t *testing.T) {
	builder := NewTreeBuilder()
	editor := DepthFilter(builder, svn.DepthEmpty, "parent/target")
	ctx := context.Background()
	root, _ := editor.OpenRoot(ctx, 0)
	parent, err := root.AddDirectory(ctx, "parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := parent.AddDirectory(ctx, "parent/target", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ChangeProp(ctx, "included", []byte("yes")); err != nil {
		t.Fatal(err)
	}
	_, _ = target.AddFile(ctx, "parent/target/excluded", nil)
	node := builder.Root().Children["parent"].Children["target"]
	if node == nil || string(node.Props["included"]) != "yes" || node.Children["excluded"] != nil {
		t.Fatalf("filtered tree = %#v", builder.Root())
	}
}

func TestDepthFilterReportsAbsentChildrenOfIncludedParent(t *testing.T) {
	builder := NewTreeBuilder()
	editor := DepthFilter(builder, svn.DepthEmpty, "")
	ctx := context.Background()
	root, _ := editor.OpenRoot(ctx, 0)
	if err := root.AbsentDirectory(ctx, "missing-dir"); err != nil {
		t.Fatal(err)
	}
	if err := root.AbsentFile(ctx, "missing-file"); err != nil {
		t.Fatal(err)
	}
	if !builder.Root().Children["missing-dir"].Absent || !builder.Root().Children["missing-file"].Absent {
		t.Fatalf("absent children = %#v", builder.Root().Children)
	}
}

func TestPathDriverOrdering(t *testing.T) {
	var trace bytes.Buffer
	editor := Trace(&trace, Noop())
	root, _ := editor.OpenRoot(context.Background(), 1)
	err := PathDriver(context.Background(), root, []string{"z/file", "a/b/two", "a/one"}, 1, func(ctx context.Context, parent DirEditor, path string) (DirEditor, error) {
		_, err := parent.OpenFile(ctx, path, 1)
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"open-root 1", "open-directory a 1", "open-directory a/b 1", "open-file a/b/two 1",
		"close-directory", "open-file a/one 1", "close-directory", "open-directory z 1",
		"open-file z/file 1", "close-directory", "",
	}, "\n")
	if trace.String() != want {
		t.Fatalf("trace:\n%s\nwant:\n%s", trace.String(), want)
	}
}

func TestPathDriverKeepsReturnedDirectoryOpen(t *testing.T) {
	var trace bytes.Buffer
	editor := Trace(&trace, Noop())
	root, _ := editor.OpenRoot(context.Background(), 1)
	err := PathDriver(context.Background(), root, []string{"a", "a/file"}, 1, func(ctx context.Context, parent DirEditor, path string) (DirEditor, error) {
		if path == "a" {
			return parent.OpenDirectory(ctx, path, 1)
		}
		_, err := parent.OpenFile(ctx, path, 1)
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"open-root 1", "open-directory a 1", "open-file a/file 1", "close-directory", ""}, "\n")
	if trace.String() != want {
		t.Fatalf("trace:\n%s\nwant:\n%s", trace.String(), want)
	}
}

func TestPathDriverUsesDirectoryRevisions(t *testing.T) {
	var trace bytes.Buffer
	editor := Trace(&trace, Noop())
	root, _ := editor.OpenRoot(context.Background(), 1)
	revisions := map[string]svn.Revnum{"a": 3, "a/b": 7}
	err := PathDriverRevisions(context.Background(), root, []string{"a/b/file"}, func(path string) svn.Revnum {
		return revisions[path]
	}, func(ctx context.Context, parent DirEditor, path string) (DirEditor, error) {
		_, err := parent.OpenFile(ctx, path, 9)
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"open-root 1", "open-directory a 3", "open-directory a/b 7", "open-file a/b/file 9",
		"close-directory", "close-directory", "",
	}, "\n")
	if trace.String() != want {
		t.Fatalf("trace:\n%s\nwant:\n%s", trace.String(), want)
	}
}

func TestPathDriverRejectsInvalidTargets(t *testing.T) {
	root, _ := Noop().OpenRoot(context.Background(), 1)
	callback := func(context.Context, DirEditor, string) (DirEditor, error) { return nil, nil }
	for _, paths := range [][]string{{""}, {"a", "a"}} {
		if err := PathDriver(context.Background(), root, paths, 1, callback); err == nil {
			t.Fatalf("accepted paths %q", paths)
		}
	}
}
