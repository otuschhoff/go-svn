package delta

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"hash"
	"io"

	"github.com/otuschhoff/go-svn/svn"
)

type TxDeltaStream struct {
	source       io.Reader
	target       io.Reader
	digest       hash.Hash
	sourceOffset int64
	done         bool
	checksum     *svn.Checksum
}

func NewTxDeltaStream(source, target io.Reader) WindowReader {
	if source == nil {
		source = bytes.NewReader(nil)
	}
	if target == nil {
		target = bytes.NewReader(nil)
	}
	return &TxDeltaStream{source: source, target: target, digest: md5.New()}
}

func (stream *TxDeltaStream) NextWindow() (*Window, error) {
	if stream.done {
		return nil, io.EOF
	}
	target, targetErr := readWindowChunk(stream.target)
	if targetErr != nil && targetErr != io.EOF {
		return nil, fmt.Errorf("read delta target: %w", targetErr)
	}
	if len(target) == 0 {
		stream.done = true
		digest := append([]byte(nil), stream.digest.Sum(nil)...)
		stream.checksum = &svn.Checksum{Kind: svn.ChecksumMD5, Digest: digest}
		return nil, io.EOF
	}
	source, sourceErr := readWindowChunk(stream.source)
	if sourceErr != nil && sourceErr != io.EOF {
		return nil, fmt.Errorf("read delta source: %w", sourceErr)
	}
	_, _ = stream.digest.Write(target)
	window := buildWindow(source, target, stream.sourceOffset)
	stream.sourceOffset += int64(len(source))
	return &window, nil
}

func (stream *TxDeltaStream) MD5() *svn.Checksum {
	if stream.checksum == nil {
		return nil
	}
	copy := *stream.checksum
	copy.Digest = append([]byte(nil), copy.Digest...)
	return &copy
}

func SendStream(source, target io.Reader, handler WindowHandler) (svn.Checksum, error) {
	stream := NewTxDeltaStream(source, target).(*TxDeltaStream)
	for {
		window, err := stream.NextWindow()
		if err == io.EOF {
			break
		}
		if err != nil {
			return svn.Checksum{}, err
		}
		if err := handler.Window(window); err != nil {
			return svn.Checksum{}, err
		}
	}
	if err := handler.Close(); err != nil {
		return svn.Checksum{}, err
	}
	return *stream.MD5(), nil
}

func SendString(source, target string, handler WindowHandler) (svn.Checksum, error) {
	return SendStream(bytes.NewBufferString(source), bytes.NewBufferString(target), handler)
}

func SendContents(contents []byte, handler WindowHandler) (svn.Checksum, error) {
	return SendStream(nil, bytes.NewReader(contents), handler)
}

func readWindowChunk(reader io.Reader) ([]byte, error) {
	buffer := make([]byte, MaxWindowSize)
	count, err := io.ReadFull(reader, buffer)
	if err == io.ErrUnexpectedEOF {
		err = nil
	}
	return buffer[:count], err
}
