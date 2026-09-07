package fsfs

import (
	"context"
	"testing"
)

func BenchmarkRevisionRoot(b *testing.B) {
	filesystem, err := Create(context.Background(), b.TempDir()+"/repository", CreateOptions{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := filesystem.RevisionRoot(context.Background(), 0); err != nil {
			b.Fatal(err)
		}
	}
}
