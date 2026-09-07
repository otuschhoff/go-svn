package delta

import (
	"bytes"
	"io"
	"testing"
)

func BenchmarkApplyWindow(b *testing.B) {
	source := bytes.Repeat([]byte("0123456789abcdef"), MaxWindowSize/16)
	window := Window{SourceLength: len(source), TargetLength: len(source), Ops: []Op{{Kind: OpSource, Length: len(source) / 2}, {Kind: OpTarget, Offset: 0, Length: len(source) / 2}}}
	b.SetBytes(int64(window.TargetLength))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := ApplyWindow(source, window); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGenerateWindows(b *testing.B) {
	source := bytes.Repeat([]byte("0123456789abcdef"), MaxWindowSize/16)
	target := append([]byte(nil), source...)
	copy(target[len(target)/2:], bytes.Repeat([]byte("changed-content!"), len(target)/32))
	b.SetBytes(int64(len(target)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		stream := NewTxDeltaStream(bytes.NewReader(source), bytes.NewReader(target))
		for {
			_, err := stream.NextWindow()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}
