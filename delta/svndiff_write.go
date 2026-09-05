package delta

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

type Encoder struct {
	writer  io.Writer
	version byte
	closed  bool
}

func NewEncoder(output io.Writer, version int) (*Encoder, error) {
	if version < 0 || version > 1 {
		return nil, fmt.Errorf("unsupported svndiff version %d", version)
	}
	if _, err := output.Write([]byte{'S', 'V', 'N', byte(version)}); err != nil {
		return nil, fmt.Errorf("write svndiff header: %w", err)
	}
	return &Encoder{writer: output, version: byte(version)}, nil
}

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
		instructions, err = encodeZlibSection(instructions)
		if err != nil {
			return err
		}
		newData, err = encodeZlibSection(newData)
		if err != nil {
			return err
		}
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

func encodeZlibSection(data []byte) ([]byte, error) {
	result := appendVarint(nil, uint64(len(data)))
	if len(data) < 512 {
		return append(result, data...), nil
	}
	var compressed bytes.Buffer
	writer, err := zlib.NewWriterLevel(&compressed, zlib.DefaultCompression)
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
