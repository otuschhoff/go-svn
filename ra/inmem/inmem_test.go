package inmem_test

import (
	"testing"
	"time"

	"github.com/oliver-tuschhoff/go-svn/ra/conformance"
	"github.com/oliver-tuschhoff/go-svn/ra/inmem"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestConformance(t *testing.T) {
	const rootURL = "memory://fixture"
	const uuid = "9a160628-1f61-42df-a452-d2ecbd30690f"
	repository := inmem.NewRepository(rootURL, uuid)
	first := inmem.Directory(map[string]*inmem.Node{"trunk": withProps(inmem.Directory(map[string]*inmem.Node{
		"README.txt": withProps(inmem.File([]byte("first\n")), svn.Props{"svn:eol-style": []byte("LF")}),
		"old.txt":    inmem.File([]byte("old\n")),
	}), svn.Props{"svn:mergeinfo": []byte("/branches:1-2"), "custom:inherited": []byte("yes")})})
	repository.AddRevision(inmem.Revision{Root: first, Time: time.Unix(100, 0), Props: svn.Props{"svn:author": []byte("alice"), "svn:log": []byte("initial")}, Changes: []svn.ChangedPath{{Path: "/trunk", Action: svn.LogAdded, NodeKind: svn.NodeDir}}})
	second := inmem.Directory(map[string]*inmem.Node{"trunk": withProps(inmem.Directory(map[string]*inmem.Node{
		"README.txt": withProps(inmem.File([]byte("first\nsecond\n")), svn.Props{"svn:eol-style": []byte("LF")}),
	}), svn.Props{"svn:mergeinfo": []byte("/branches:1-2"), "custom:inherited": []byte("yes")})})
	repository.AddRevision(inmem.Revision{Root: second, Time: time.Unix(200, 0), Props: svn.Props{"svn:author": []byte("bob"), "svn:log": []byte("update")}, Changes: []svn.ChangedPath{{Path: "/trunk/README.txt", Action: svn.LogModified, NodeKind: svn.NodeFile}, {Path: "/trunk/old.txt", Action: svn.LogDeleted, NodeKind: svn.NodeFile}}})

	conformance.Run(t, conformance.Fixture{Open: func(t *testing.T) interfaceSession {
		session, err := repository.Open(rootURL)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}, RootURL: rootURL, UUID: uuid, Latest: 2, FilePath: "trunk/README.txt", OldContent: "first\n", Content: "first\nsecond\n", Deleted: "trunk/old.txt",
		LatestTime: time.Unix(200, 0), RevProp: "svn:author", RevValue: "bob",
		MergePath: "trunk", InheritKey: "custom:inherited", InheritVal: "yes"})
}

type interfaceSession = interface {
	URL() string
}

func withProps(node *inmem.Node, props svn.Props) *inmem.Node { node.Props = props; return node }
