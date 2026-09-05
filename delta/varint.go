package delta

import (
	"errors"
	"io"
	"math"
)

var errVarintOverflow = errors.New("svndiff integer overflow")

func appendVarint(destination []byte, value uint64) []byte {
	var encoded [10]byte
	position := len(encoded) - 1
	encoded[position] = byte(value & 0x7f)
	for value >>= 7; value != 0; value >>= 7 {
		position--
		encoded[position] = byte(value&0x7f) | 0x80
	}
	return append(destination, encoded[position:]...)
}

func readVarint(reader io.ByteReader) (uint64, error) {
	var value uint64
	for index := 0; index < 10; index++ {
		current, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		if value > math.MaxUint64>>7 {
			return 0, errVarintOverflow
		}
		value = value<<7 | uint64(current&0x7f)
		if current&0x80 == 0 {
			return value, nil
		}
	}
	return 0, errVarintOverflow
}

func consumeVarint(data []byte, position *int) (uint64, error) {
	var value uint64
	for count := 0; count < 10; count++ {
		if *position >= len(data) {
			return 0, io.ErrUnexpectedEOF
		}
		current := data[*position]
		*position++
		if value > math.MaxUint64>>7 {
			return 0, errVarintOverflow
		}
		value = value<<7 | uint64(current&0x7f)
		if current&0x80 == 0 {
			return value, nil
		}
	}
	return 0, errVarintOverflow
}
