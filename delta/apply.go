package delta

import (
	"crypto/md5"
	"errors"
	"fmt"
	"io"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

// WindowReader streams windows. It returns io.EOF after the final window.
type WindowReader interface {
	NextWindow() (*Window, error)
}

// WindowHandler consumes a window. A nil window marks the end of a stream.
type WindowHandler func(*Window) error

// Apply streams reconstructed target data to target and returns its MD5.
func Apply(source io.ReadSeeker, target io.Writer, windows WindowReader, expectedMD5 *svn.Checksum) (svn.Checksum, error) {
	digest := md5.New()
	output := io.MultiWriter(target, digest)
	var sourceView []byte

	for {
		window, err := windows.NextWindow()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return svn.Checksum{}, err
		}
		if window == nil {
			return svn.Checksum{}, fmt.Errorf("%w: nil window before end of stream", svn.ErrSvndiffCorruptWindow)
		}
		if cap(sourceView) < window.SourceLength {
			sourceView = make([]byte, window.SourceLength)
		} else {
			sourceView = sourceView[:window.SourceLength]
		}
		if window.SourceLength > 0 {
			if _, err := source.Seek(window.SourceOffset, io.SeekStart); err != nil {
				return svn.Checksum{}, fmt.Errorf("seek source view: %w", err)
			}
			if _, err := io.ReadFull(source, sourceView); err != nil {
				return svn.Checksum{}, fmt.Errorf("read source view: %w", err)
			}
		}
		contents, err := ApplyWindow(sourceView, *window)
		if err != nil {
			return svn.Checksum{}, err
		}
		if _, err := output.Write(contents); err != nil {
			return svn.Checksum{}, fmt.Errorf("write target view: %w", err)
		}
	}

	actual := svn.Checksum{Kind: svn.ChecksumMD5, Digest: digest.Sum(nil)}
	if expectedMD5 != nil && !actual.Equal(*expectedMD5) {
		return actual, fmt.Errorf("%w: expected %s, got %s", svn.ErrChecksumMismatch, expectedMD5.Hex(), actual.Hex())
	}
	return actual, nil
}

type sliceWindowReader struct {
	windows []Window
	index   int
}

// Windows returns a WindowReader over a slice.
func Windows(windows ...Window) WindowReader {
	return &sliceWindowReader{windows: windows}
}

func (reader *sliceWindowReader) NextWindow() (*Window, error) {
	if reader.index == len(reader.windows) {
		return nil, io.EOF
	}
	window := &reader.windows[reader.index]
	reader.index++
	return window, nil
}
