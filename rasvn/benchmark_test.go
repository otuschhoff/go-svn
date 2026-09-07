package rasvn

import (
	"bytes"
	"testing"
)

func BenchmarkMarshalTuple(b *testing.B) {
	item := List(Word("success"), List(Number(123456), String(bytes.Repeat([]byte("payload"), 128)), Word("done")))
	b.ReportAllocs()
	for range b.N {
		var output bytes.Buffer
		encoder := NewEncoder(&output)
		if err := encoder.Encode(item); err != nil {
			b.Fatal(err)
		}
		if err := encoder.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshalTuple(b *testing.B) {
	var encoded bytes.Buffer
	encoder := NewEncoder(&encoded)
	if err := encoder.Encode(List(Word("success"), List(Number(123456), String(bytes.Repeat([]byte("payload"), 128)), Word("done")))); err != nil {
		b.Fatal(err)
	}
	if err := encoder.Flush(); err != nil {
		b.Fatal(err)
	}
	data := encoded.Bytes()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := NewDecoder(bytes.NewReader(data)).Decode(); err != nil {
			b.Fatal(err)
		}
	}
}
