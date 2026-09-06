package delta

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"

	"github.com/otuschhoff/go-svn/internal/lz4"
)

type Encoder struct {
	writer      io.Writer
	version     byte
	compression int
	closed      bool
}

func NewEncoder(output io.Writer, version int) (*Encoder, error) {
	if version < 0 || version > 2 {
		return nil, fmt.Errorf("unsupported svndiff version %d", version)
	}
	if _, err := output.Write([]byte{'S', 'V', 'N', byte(version)}); err != nil {
		return nil, fmt.Errorf("write svndiff header: %w", err)
	}
	return &Encoder{writer: output, version: byte(version), compression: zlib.DefaultCompression}, nil
}

func NewSvndiffWriter(output io.Writer, version int, compression int) WindowHandler {
	encoder, err := newEncoder(output, version, compression)
	if err != nil {
		return &errorWindowHandler{err: err}
	}
	return encoder
}

func newEncoder(output io.Writer, version, compression int) (*Encoder, error) {
	if compression < zlib.HuffmanOnly || compression > zlib.BestCompression {
		return nil, fmt.Errorf("invalid compression level %d", compression)
	}
	encoder, err := NewEncoder(output, version)
	if err != nil {
		return nil, err
	}
	encoder.compression = compression
	return encoder, nil
}

type errorWindowHandler struct{ err error }

func (handler *errorWindowHandler) Window(*Window) error { return handler.err }
func (handler *errorWindowHandler) Close() error         { return handler.err }

func (encoder *Encoder) WriteWindow(window Window) error {
	if encoder.closed {
		return fmt.Errorf("svndiff encoder is closed")
	}
	if err := window.Validate(); err != nil {
		return err
	}
	instructions := encodeInstructions(window.Ops)
	newData := append([]byte(nil), window.NewData...)
	if encoder.version == 1 {
		var err error
		instructions, err = encodeZlibSection(instructions, encoder.compression)
		if err != nil {
			return err
		}
		newData, err = encodeZlibSection(newData, encoder.compression)
		if err != nil {
			return err
		}
	} else if encoder.version == 2 {
		instructions = encodeLZ4Section(instructions)
		newData = encodeLZ4Section(newData)
	}
	header := make([]byte, 0, 50)
	header = appendVarint(header, uint64(window.SourceOffset))
	header = appendVarint(header, uint64(window.SourceLength))
	header = appendVarint(header, uint64(window.TargetLength))
	header = appendVarint(header, uint64(len(instructions)))
	header = appendVarint(header, uint64(len(newData)))
	for _, part := range [][]byte{header, instructions, newData} {
		if _, err := encoder.writer.Write(part); err != nil {
			return fmt.Errorf("write svndiff window: %w", err)
		}
	}
	return nil
}

func (encoder *Encoder) Window(window *Window) error {
	if window == nil {
		return encoder.Close()
	}
	return encoder.WriteWindow(*window)
}

func encodeLZ4Section(data []byte) []byte {
	result := appendVarint(nil, uint64(len(data)))
	compressed := lz4.EncodeBlock(data)
	if len(compressed) >= len(data) {
		return append(result, data...)
	}
	return append(result, compressed...)
}

func (encoder *Encoder) Close() error {
	encoder.closed = true
	if closer, ok := encoder.writer.(interface{ Flush() error }); ok {
		return closer.Flush()
	}
	return nil
}

func encodeInstructions(operations []Op) []byte {
	encoded := make([]byte, 0, len(operations)*2)
	for _, operation := range operations {
		first := byte(operation.Kind) << 6
		if operation.Length < 64 {
			encoded = append(encoded, first|byte(operation.Length))
		} else {
			encoded = append(encoded, first)
			encoded = appendVarint(encoded, uint64(operation.Length))
		}
		if operation.Kind != OpNew {
			encoded = appendVarint(encoded, uint64(operation.Offset))
		}
	}
	return encoded
}

func encodeZlibSection(data []byte, compression int) ([]byte, error) {
	result := appendVarint(nil, uint64(len(data)))
	if len(data) < 512 {
		return append(result, data...), nil
	}
	var compressed bytes.Buffer
	writer, err := zlib.NewWriterLevel(&compressed, compression)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() >= len(data) {
		return append(result, data...), nil
	}
	return append(result, compressed.Bytes()...), nil
}
