package fsfs

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
)

type Layout string

const (
	LayoutLinear  Layout = "linear"
	LayoutSharded Layout = "sharded"
)

type Addressing string

const (
	AddressingPhysical Addressing = "physical"
	AddressingLogical  Addressing = "logical"
)

type Format struct {
	Number     int
	Layout     Layout
	ShardSize  int64
	Addressing Addressing
}

func parseFormat(data []byte) (Format, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	if !scanner.Scan() {
		return Format{}, formatError("missing FSFS format number")
	}
	number, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || number < 1 || number > 8 {
		return Format{}, formatError("unsupported FSFS format %q", strings.TrimSpace(scanner.Text()))
	}
	format := Format{Number: number, Layout: LayoutLinear, Addressing: AddressingPhysical}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "layout":
			if len(fields) == 2 && fields[1] == string(LayoutLinear) {
				format.Layout = LayoutLinear
				format.ShardSize = 0
			} else if len(fields) == 3 && fields[1] == string(LayoutSharded) {
				shardSize, parseErr := strconv.ParseInt(fields[2], 10, 64)
				if parseErr != nil || shardSize <= 0 {
					return Format{}, formatError("invalid sharded layout %q", scanner.Text())
				}
				format.Layout, format.ShardSize = LayoutSharded, shardSize
			} else {
				return Format{}, formatError("invalid layout %q", scanner.Text())
			}
		case "addressing":
			if len(fields) != 2 || fields[1] != string(AddressingPhysical) && fields[1] != string(AddressingLogical) {
				return Format{}, formatError("invalid addressing %q", scanner.Text())
			}
			format.Addressing = Addressing(fields[1])
		default:
			return Format{}, formatError("unknown FSFS format option %q", fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return Format{}, err
	}
	if format.Number < 7 && format.Addressing != AddressingPhysical || format.Number >= 7 && format.Addressing != AddressingLogical {
		return Format{}, formatError("FSFS format %d does not support %s addressing", format.Number, format.Addressing)
	}
	return format, nil
}

func formatError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", svn.ErrFSUnsupportedFormat, fmt.Sprintf(format, args...))
}
