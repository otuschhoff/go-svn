package delta

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

const (
	MaxWindowSize             = 100 * 1024
	maxInstructionSectionSize = MaxWindowSize * 21
)

type Decoder struct {
	reader           *bufio.Reader
	version          byte
	lastSourceOffset int64
	started          bool
}

func NewDecoder(input io.Reader) (*Decoder, error) {
	reader := bufio.NewReader(input)
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrSvndiffInvalidHeader, err)
	}
	if !bytes.Equal(header[:3], []byte("SVN")) || header[3] > 1 {
		return nil, fmt.Errorf("%w: %q", svn.ErrSvndiffInvalidHeader, header)
	}
	return &Decoder{reader: reader, version: header[3]}, nil
}

func (decoder *Decoder) Version() int { return int(decoder.version) }

func (decoder *Decoder) NextWindow() (*Window, error) {
	if _, err := decoder.reader.Peek(1); errors.Is(err, io.EOF) {
		return nil, io.EOF
	} else if err != nil {
		return nil, err
	}
	header := make([]uint64, 5)
	for index := range header {
		value, err := readVarint(decoder.reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("%w: window header: %v", svn.ErrSvndiffUnexpectedEnd, err)
		}
		header[index] = value
	}
	if header[0] > math.MaxInt64 || header[1] > MaxWindowSize || header[2] > MaxWindowSize || header[3] > maxInstructionSectionSize || header[4] > MaxWindowSize {
		return nil, fmt.Errorf("%w: window sizes exceed limits", svn.ErrSvndiffCorruptWindow)
	}
	sourceOffset := int64(header[0])
	if decoder.started && sourceOffset < decoder.lastSourceOffset {
		return nil, fmt.Errorf("%w: source view moved from %d to %d", svn.ErrSvndiffBackwardView, decoder.lastSourceOffset, sourceOffset)
	}
	decoder.started = true
	decoder.lastSourceOffset = sourceOffset

	instructions, err := readSection(decoder.reader, int(header[3]), decoder.version, maxInstructionSectionSize)
	if err != nil {
		return nil, err
	}
	newData, err := readSection(decoder.reader, int(header[4]), decoder.version, MaxWindowSize)
	if err != nil {
		return nil, err
	}
	operations, err := decodeInstructions(instructions, int(header[1]), int(header[2]), len(newData))
	if err != nil {
		return nil, err
	}
	window := &Window{
		SourceOffset: sourceOffset,
		SourceLength: int(header[1]),
		TargetLength: int(header[2]),
		Ops:          operations,
		NewData:      newData,
	}
	if err := window.Validate(); err != nil {
		return nil, err
	}
	return window, nil
}

func readSection(reader io.Reader, encodedLength int, version byte, limit int) ([]byte, error) {
	encoded := make([]byte, encodedLength)
	if _, err := io.ReadFull(reader, encoded); err != nil {
		return nil, fmt.Errorf("%w: section: %v", svn.ErrSvndiffUnexpectedEnd, err)
	}
	if version == 0 {
		return encoded, nil
	}
	position := 0
	originalLength, err := consumeVarint(encoded, &position)
	if err != nil || originalLength > uint64(limit) {
		return nil, fmt.Errorf("%w: invalid original section size", svn.ErrSvndiffInvalidCompressedData)
	}
	payload := encoded[position:]
	if uint64(len(payload)) == originalLength {
		return append([]byte(nil), payload...), nil
	}
	zlibReader, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrSvndiffInvalidCompressedData, err)
	}
	decoded, readErr := io.ReadAll(io.LimitReader(zlibReader, int64(originalLength)+1))
	closeErr := zlibReader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrSvndiffInvalidCompressedData, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrSvndiffInvalidCompressedData, closeErr)
	}
	if uint64(len(decoded)) != originalLength {
		return nil, fmt.Errorf("%w: decoded section has %d bytes, want %d", svn.ErrSvndiffInvalidCompressedData, len(decoded), originalLength)
	}
	return decoded, nil
}

func decodeInstructions(data []byte, sourceLength, targetLength, newLength int) ([]Op, error) {
	operations := make([]Op, 0)
	position := 0
	newPosition := 0
	for position < len(data) {
		instruction := data[position]
		position++
		kind := OpKind(instruction >> 6)
		if kind > OpNew {
			return nil, invalidOps("invalid instruction selector")
		}
		length := uint64(instruction & 0x3f)
		var err error
		if length == 0 {
			length, err = consumeVarint(data, &position)
			if err != nil {
				return nil, invalidOps("cannot decode instruction length")
			}
		}
		if length == 0 || length > math.MaxInt {
			return nil, invalidOps("invalid instruction length")
		}
		offset := uint64(newPosition)
		if kind != OpNew {
			offset, err = consumeVarint(data, &position)
			if err != nil || offset > math.MaxInt {
				return nil, invalidOps("cannot decode instruction offset")
			}
		} else {
			newPosition += int(length)
		}
		operations = append(operations, Op{Kind: kind, Offset: int(offset), Length: int(length)})
	}
	window := Window{SourceLength: sourceLength, TargetLength: targetLength, Ops: operations, NewData: make([]byte, newLength)}
	if err := window.Validate(); err != nil {
		return nil, err
	}
	if newPosition != newLength {
		return nil, invalidOps("instructions consume %d new bytes, section contains %d", newPosition, newLength)
	}
	return operations, nil
}
