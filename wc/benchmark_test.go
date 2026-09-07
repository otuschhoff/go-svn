package wc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/ra/inmem"
	"github.com/otuschhoff/go-svn/svn"
)

func BenchmarkCheckout10kFiles(b *testing.B) {
	children := make(map[string]*inmem.Node, 10_000)
	for index := range 10_000 {
		children[fmt.Sprintf("file-%05d", index)] = inmem.File([]byte("benchmark content\n"))
	}
	repository := inmem.NewRepository("memory://benchmark", "benchmark-uuid")
	repository.AddRevision(inmem.Revision{Root: inmem.Directory(map[string]*inmem.Node{"trunk": inmem.Directory(children)})})
	session, err := repository.Open("memory://benchmark/trunk")
	if err != nil {
		b.Fatal(err)
	}
	defer session.Close()
	root := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		destination := filepath.Join(root, fmt.Sprintf("checkout-%d", index))
		database, err := Checkout(context.Background(), session, destination, 1, UpdateOptions{Depth: svn.DepthInfinity})
		if err != nil {
			b.Fatal(err)
		}
		if err := database.Close(); err != nil {
			b.Fatal(err)
		}
		if err := os.RemoveAll(destination); err != nil {
			b.Fatal(err)
		}
	}
}
