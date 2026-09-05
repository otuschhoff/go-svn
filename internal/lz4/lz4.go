package lz4

import (
	"encoding/binary"
	"fmt"
)

const maxOffset = 65535

const (
	lastLiterals   = 5
	matchFindLimit = 12
)

// DecodeBlock decodes one raw LZ4 block and requires exactly size output bytes.
func DecodeBlock(source []byte, size int) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("negative output size")
	}
	output := make([]byte, 0, size)
	position := 0
	for position < len(source) {
		token := source[position]
		position++
		literalLength, err := readLength(source, &position, int(token>>4))
		if err != nil {
			return nil, err
		}
		if literalLength > size-len(output) || literalLength > len(source)-position {
			return nil, fmt.Errorf("literal exceeds block bounds")
		}
		output = append(output, source[position:position+literalLength]...)
		position += literalLength
		if position == len(source) {
			break
		}
		if position+2 > len(source) {
			return nil, fmt.Errorf("truncated match offset")
		}
		offset := int(binary.LittleEndian.Uint16(source[position:]))
		position += 2
		if offset == 0 || offset > len(output) {
			return nil, fmt.Errorf("invalid match offset %d", offset)
		}
		matchLength, err := readLength(source, &position, int(token&0x0f))
		if err != nil {
			return nil, err
		}
		matchLength += 4
		if matchLength > size-len(output) {
			return nil, fmt.Errorf("match exceeds output bounds")
		}
		start := len(output) - offset
		for index := 0; index < matchLength; index++ {
			output = append(output, output[start+index])
		}
	}
	if position != len(source) || len(output) != size {
		return nil, fmt.Errorf("decoded %d bytes, want %d", len(output), size)
	}
	return output, nil
}

// EncodeBlock encodes one independent raw LZ4 block using greedy matches.
func EncodeBlock(source []byte) []byte {
	if len(source) == 0 {
		return []byte{0}
	}
	const hashBits = 16
	table := make([]int, 1<<hashBits)
	for index := range table {
		table[index] = -1
	}
	output := make([]byte, 0, len(source))
	anchor := 0
	position := 0
	for position+matchFindLimit <= len(source) {
		sequence := binary.LittleEndian.Uint32(source[position:])
		hash := int((sequence * 2654435761) >> (32 - hashBits))
		candidate := table[hash]
		table[hash] = position
		if candidate < 0 || position-candidate > maxOffset || !equal4(source[candidate:], source[position:]) {
			position++
			continue
		}
		matchLength := 4
		for position+matchLength < len(source)-lastLiterals && source[candidate+matchLength] == source[position+matchLength] {
			matchLength++
		}
		output = appendSequence(output, source[anchor:position], position-candidate, matchLength)
		position += matchLength
		anchor = position
		if position+matchFindLimit <= len(source) {
			previous := position - 2
			sequence = binary.LittleEndian.Uint32(source[previous:])
			table[int((sequence*2654435761)>>(32-hashBits))] = previous
		}
	}
	return appendLastLiterals(output, source[anchor:])
}

func readLength(source []byte, position *int, length int) (int, error) {
	if length != 15 {
		return length, nil
	}
	for {
		if *position >= len(source) {
			return 0, fmt.Errorf("truncated extended length")
		}
		value := int(source[*position])
		*position++
		if length > int(^uint(0)>>1)-value {
			return 0, fmt.Errorf("length overflow")
		}
		length += value
		if value != 255 {
			return length, nil
		}
	}
}

func appendSequence(output, literals []byte, offset, matchLength int) []byte {
	tokenPosition := len(output)
	output = append(output, 0)
	literalNibble := len(literals)
	if literalNibble > 15 {
		literalNibble = 15
	}
	matchNibble := matchLength - 4
	if matchNibble > 15 {
		matchNibble = 15
	}
	output[tokenPosition] = byte(literalNibble<<4 | matchNibble)
	output = appendExtendedLength(output, len(literals)-15)
	output = append(output, literals...)
	output = append(output, byte(offset), byte(offset>>8))
	return appendExtendedLength(output, matchLength-4-15)
}

func appendLastLiterals(output, literals []byte) []byte {
	token := len(literals)
	if token > 15 {
		token = 15
	}
	output = append(output, byte(token<<4))
	output = appendExtendedLength(output, len(literals)-15)
	return append(output, literals...)
}

func appendExtendedLength(output []byte, extra int) []byte {
	if extra < 0 {
		return output
	}
	for extra >= 255 {
		output = append(output, 255)
		extra -= 255
	}
	return append(output, byte(extra))
}

func equal4(first, second []byte) bool {
	return binary.LittleEndian.Uint32(first) == binary.LittleEndian.Uint32(second)
}
