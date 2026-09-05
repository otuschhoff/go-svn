package hashfile

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

const maxRecordSize = 64 << 20

func Read(reader io.Reader) (svn.Props, error) {
	return read(reader, nil, false)
}

func ReadIncremental(reader io.Reader, base svn.Props) (svn.Props, error) {
	return read(reader, base, true)
}

func Write(writer io.Writer, properties svn.Props) error {
	buffered := bufio.NewWriter(writer)
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := properties[key]
		if len(key) > maxRecordSize || len(value) > maxRecordSize {
			return malformed("record exceeds %d bytes", maxRecordSize)
		}
		if err := writeRecord(buffered, 'K', []byte(key)); err != nil {
			return err
		}
		if err := writeRecord(buffered, 'V', value); err != nil {
			return err
		}
	}
	if _, err := buffered.WriteString("END\n"); err != nil {
		return err
	}
	return buffered.Flush()
}

func read(reader io.Reader, base svn.Props, incremental bool) (svn.Props, error) {
	properties := base.Clone()
	if properties == nil {
		properties = make(svn.Props)
	}
	buffered := bufio.NewReaderSize(reader, 128)
	for {
		kind, payload, err := readRecord(buffered)
		if err != nil {
			return nil, err
		}
		switch kind {
		case 'E':
			return properties, nil
		case 'D':
			if !incremental {
				return nil, malformed("delete record in non-incremental hash")
			}
			delete(properties, string(payload))
		case 'K':
			valueKind, value, err := readRecord(buffered)
			if err != nil {
				return nil, err
			}
			if valueKind != 'V' {
				return nil, malformed("key record is not followed by a value record")
			}
			properties[string(payload)] = value
		default:
			return nil, malformed("unexpected record type %q", kind)
		}
	}
}

func readRecord(reader *bufio.Reader) (byte, []byte, error) {
	header, err := reader.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return 0, nil, malformed("record header is too long")
		}
		if errors.Is(err, io.EOF) {
			return 0, nil, svn.NewError(svn.ErrIncompleteData, "hash file ended before END")
		}
		return 0, nil, svn.Wrap(svn.ErrMalformedFile, "read hash record header", err)
	}
	if string(header) == "END\n" {
		return 'E', nil, nil
	}
	if len(header) < 4 || header[1] != ' ' {
		return 0, nil, malformed("invalid record header %q", strings.TrimSuffix(string(header), "\n"))
	}
	kind := header[0]
	if kind != 'K' && kind != 'V' && kind != 'D' {
		return 0, nil, malformed("invalid record type %q", kind)
	}
	lengthText := string(header[2 : len(header)-1])
	if lengthText == "" || lengthText[0] == '+' || lengthText[0] == '-' {
		return 0, nil, malformed("invalid record length %q", lengthText)
	}
	length, err := strconv.ParseUint(lengthText, 10, 64)
	if err != nil || length > maxRecordSize {
		return 0, nil, malformed("invalid record length %q", lengthText)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, svn.Wrap(svn.ErrIncompleteData, "hash record data is truncated", err)
	}
	terminator, err := reader.ReadByte()
	if err != nil {
		return 0, nil, svn.Wrap(svn.ErrIncompleteData, "hash record terminator is missing", err)
	}
	if terminator != '\n' {
		return 0, nil, malformed("hash record is not newline terminated")
	}
	return kind, payload, nil
}

func writeRecord(writer *bufio.Writer, kind byte, value []byte) error {
	if _, err := fmt.Fprintf(writer, "%c %d\n", kind, len(value)); err != nil {
		return err
	}
	if _, err := writer.Write(value); err != nil {
		return err
	}
	return writer.WriteByte('\n')
}

func malformed(format string, arguments ...any) error {
	return svn.NewError(svn.ErrMalformedFile, fmt.Sprintf(format, arguments...))
}
