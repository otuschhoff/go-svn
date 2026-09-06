package fsfs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/svn"
)

const maxRepresentationDepth = 1024

type representationHeader struct {
	plain  bool
	base   *Representation
	data   io.Reader
	close  func() error
	finish func() error
}

func (filesystem *FS) writeRepresentation(ctx context.Context, representation *Representation, writer io.Writer) error {
	return filesystem.writeRepresentationDepth(ctx, representation, writer, 0)
}

func (filesystem *FS) writeRepresentationDepth(ctx context.Context, representation *Representation, writer io.Writer, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if representation == nil {
		return nil
	}
	if depth > maxRepresentationDepth {
		return fmt.Errorf("%w: representation delta chain is too deep", svn.ErrFSCorrupt)
	}
	header, err := filesystem.readRepresentation(representation)
	if err != nil {
		return err
	}
	defer header.close()
	md5Digest := md5.New()
	sha1Digest := sha1.New()
	counted := &countingWriter{writer: io.MultiWriter(writer, md5Digest, sha1Digest)}
	contextual := contextWriter{ctx: ctx, writer: counted}
	if header.plain {
		if _, err := io.Copy(&contextual, header.data); err != nil {
			return err
		}
	} else {
		base, err := os.CreateTemp("", "go-svn-fsfs-base-*")
		if err != nil {
			return err
		}
		baseName := base.Name()
		defer os.Remove(baseName)
		defer base.Close()
		if header.base != nil {
			if err := filesystem.writeRepresentationDepth(ctx, header.base, base, depth+1); err != nil {
				return err
			}
		}
		if _, err := base.Seek(0, io.SeekStart); err != nil {
			return err
		}
		decoder, err := delta.NewDecoder(header.data)
		if err != nil {
			return err
		}
		if _, err := delta.Apply(base, &contextual, decoder, nil); err != nil {
			return err
		}
	}
	if err := header.finish(); err != nil {
		return err
	}
	if representation.ExpandedSize >= 0 && counted.count != representation.ExpandedSize {
		return fmt.Errorf("%w: representation expanded to %d bytes, want %d", svn.ErrFSCorrupt, counted.count, representation.ExpandedSize)
	}
	if representation.MD5 != "" && !strings.EqualFold(hex.EncodeToString(md5Digest.Sum(nil)), representation.MD5) {
		return fmt.Errorf("%w: representation MD5 mismatch", svn.ErrChecksumMismatch)
	}
	if representation.SHA1 != "" && !strings.EqualFold(hex.EncodeToString(sha1Digest.Sum(nil)), representation.SHA1) {
		return fmt.Errorf("%w: representation SHA1 mismatch", svn.ErrChecksumMismatch)
	}
	return nil
}

func (filesystem *FS) readRepresentation(representation *Representation) (representationHeader, error) {
	file, err := filesystem.openRevision(representation.Revision)
	if err != nil {
		return representationHeader{}, err
	}
	offset, err := file.itemOffset(representation.Revision, representation.Item)
	if err != nil {
		return representationHeader{}, err
	}
	if offset < 0 || offset >= file.size {
		return representationHeader{}, fmt.Errorf("%w: representation offset is outside revision file", svn.ErrFSCorrupt)
	}
	reader, err := os.Open(file.path)
	if err != nil {
		return representationHeader{}, err
	}
	if _, err := reader.Seek(offset, io.SeekStart); err != nil {
		reader.Close()
		return representationHeader{}, err
	}
	buffered := bufio.NewReaderSize(reader, 64*1024)
	line, err := buffered.ReadSlice('\n')
	if err != nil {
		reader.Close()
		return representationHeader{}, fmt.Errorf("%w: representation header is incomplete", svn.ErrFSCorrupt)
	}
	if representation.Length < 0 || offset+int64(len(line)) > file.size || representation.Length > file.size-offset-int64(len(line)) {
		reader.Close()
		return representationHeader{}, fmt.Errorf("%w: representation data is truncated", svn.ErrFSCorrupt)
	}
	parsed, err := parseRepresentationHeader(bytes.TrimSuffix(line, []byte("\n")))
	if err != nil {
		reader.Close()
		return representationHeader{}, err
	}
	payload := &io.LimitedReader{R: buffered, N: representation.Length}
	parsed.data = payload
	parsed.close = reader.Close
	parsed.finish = func() error {
		if payload.N != 0 {
			return fmt.Errorf("%w: representation payload was not fully consumed", svn.ErrFSCorrupt)
		}
		trailer := make([]byte, len("ENDREP\n"))
		if _, err := io.ReadFull(buffered, trailer); err != nil || !bytes.Equal(trailer, []byte("ENDREP\n")) {
			return fmt.Errorf("%w: representation trailer is missing", svn.ErrFSCorrupt)
		}
		return nil
	}
	return parsed, nil
}

func parseRepresentationHeader(data []byte) (representationHeader, error) {
	header := representationHeader{}
	fields := strings.Fields(string(data))
	if len(fields) == 1 && fields[0] == "PLAIN" {
		header.plain = true
		return header, nil
	}
	if len(fields) == 1 && fields[0] == "DELTA" {
		return header, nil
	}
	if len(fields) != 4 || fields[0] != "DELTA" {
		return representationHeader{}, fmt.Errorf("%w: invalid representation header %q", svn.ErrFSCorrupt, data)
	}
	revision, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || revision < 0 {
		return representationHeader{}, fmt.Errorf("%w: invalid delta base revision", svn.ErrFSCorrupt)
	}
	item, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || item < 0 {
		return representationHeader{}, fmt.Errorf("%w: invalid delta base item", svn.ErrFSCorrupt)
	}
	length, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || length < 0 {
		return representationHeader{}, fmt.Errorf("%w: invalid delta base length", svn.ErrFSCorrupt)
	}
	header.base = &Representation{Revision: svn.Revnum(revision), Item: item, Length: length, ExpandedSize: -1}
	return header, nil
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer *contextWriter) Write(data []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	return writer.writer.Write(data)
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(data []byte) (int, error) {
	written, err := writer.writer.Write(data)
	writer.count += int64(written)
	return written, err
}
