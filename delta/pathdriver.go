package delta

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
	svnpath "github.com/otuschhoff/go-svn/svn/path"
)

type PathDriverFunc func(context.Context, DirEditor, string) (DirEditor, error)

func PathDriver(ctx context.Context, root DirEditor, paths []string, baseRevision svn.Revnum, callback PathDriverFunc) error {
	canonical := make([]string, len(paths))
	for index, path := range paths {
		canonical[index] = svnpath.RelpathCanonicalize(path)
		if canonical[index] == "" {
			return fmt.Errorf("path driver target is empty")
		}
	}
	sort.Strings(canonical)
	type frame struct {
		path   string
		editor DirEditor
	}
	stack := []frame{{editor: root}}
	previous := ""
	for _, path := range canonical {
		if path == previous {
			return fmt.Errorf("duplicate path %q", path)
		}
		previous = path
		parent := svnpath.RelpathDirname(path)
		for len(stack) > 1 {
			if _, ok := svnpath.RelpathSkipAncestor(stack[len(stack)-1].path, parent); ok {
				break
			}
			if err := stack[len(stack)-1].editor.Close(ctx); err != nil {
				return err
			}
			stack = stack[:len(stack)-1]
		}
		current := stack[len(stack)-1].path
		remaining, ok := svnpath.RelpathSkipAncestor(current, parent)
		if !ok {
			return fmt.Errorf("cannot drive from %q to %q", current, parent)
		}
		for _, component := range strings.Split(remaining, "/") {
			if component == "" {
				continue
			}
			current = svnpath.RelpathJoin(current, component)
			opened, err := stack[len(stack)-1].editor.OpenDirectory(ctx, current, baseRevision)
			if err != nil {
				return err
			}
			stack = append(stack, frame{path: current, editor: opened})
		}
		opened, err := callback(ctx, stack[len(stack)-1].editor, path)
		if err != nil {
			return err
		}
		if opened != nil {
			stack = append(stack, frame{path: path, editor: opened})
		}
	}
	for len(stack) > 1 {
		if err := stack[len(stack)-1].editor.Close(ctx); err != nil {
			return err
		}
		stack = stack[:len(stack)-1]
	}
	return nil
}
