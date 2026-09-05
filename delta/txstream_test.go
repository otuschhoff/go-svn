package delta

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestTxDeltaStreamRoundTrip(t *testing.T) {
	source := bytes.Repeat([]byte("0123456789abcdef"), 20000)
	target := append([]byte(nil), source...)
	copy(target[70000:], bytes.Repeat([]byte("changed!"), 1000))
	target = append(target, []byte("tail")...)
	stream := NewTxDeltaStream(bytes.NewReader(source), bytes.NewReader(target))
	var windows []Window
	for {
		window, err := stream.NextWindow()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if window.TargetLength > MaxWindowSize {
			t.Fatalf("target window size = %d", window.TargetLength)
		}
		windows = append(windows, *window)
	}
	var reconstructed bytes.Buffer
	checksum, err := Apply(bytes.NewReader(source), &reconstructed, Windows(windows...), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reconstructed.Bytes(), target) {
		t.Fatal("reconstructed target differs")
	}
	if got := stream.(*TxDeltaStream).MD5(); got == nil || !got.Equal(checksum) {
		t.Fatalf("stream checksum = %v, apply checksum = %v", got, checksum)
	}
}

func TestTxDeltaRandomProperty(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	for iteration := 0; iteration < 200; iteration++ {
		source := make([]byte, random.Intn(8192))
		target := make([]byte, random.Intn(8192))
		_, _ = random.Read(source)
		_, _ = random.Read(target)
		if iteration%2 == 0 && len(source) > 128 && len(target) > 128 {
			copy(target[32:], source[64:])
		}
		stream := NewTxDeltaStream(bytes.NewReader(source), bytes.NewReader(target))
		var reconstructed bytes.Buffer
		if _, err := Apply(bytes.NewReader(source), &reconstructed, stream, nil); err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		if !bytes.Equal(reconstructed.Bytes(), target) {
			t.Fatalf("iteration %d differs", iteration)
		}
	}
}

func TestRollingHashUpdateMatchesRecomputation(t *testing.T) {
	data := make([]byte, xdeltaBlockSize*4)
	random := rand.New(rand.NewSource(2))
	_, _ = random.Read(data)
	hash := rollingHash(data[:xdeltaBlockSize])
	for position := 1; position+xdeltaBlockSize <= len(data); position++ {
		hash = rollHash(hash, data[position-1], data[position+xdeltaBlockSize-1])
		if want := rollingHash(data[position : position+xdeltaBlockSize]); hash != want {
			t.Fatalf("position %d: rolling hash %08x, recomputed %08x", position, hash, want)
		}
	}
}

func TestTxDeltaCompressionRatio(t *testing.T) {
	baselines, err := os.Open("../testdata/delta/sizes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer baselines.Close()
	scanner := bufio.NewScanner(baselines)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if len(fields) != 2 {
			t.Fatalf("invalid sizes line %q", scanner.Text())
		}
		referenceSize, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		t.Run(fields[0], func(t *testing.T) {
			source, err := os.ReadFile("../testdata/delta/" + fields[0] + ".a")
			if err != nil {
				t.Fatal(err)
			}
			target, err := os.ReadFile("../testdata/delta/" + fields[0] + ".b")
			if err != nil {
				t.Fatal(err)
			}
			var encoded bytes.Buffer
			encoder, err := NewEncoder(&encoded, 0)
			if err != nil {
				t.Fatal(err)
			}
			stream := NewTxDeltaStream(bytes.NewReader(source), bytes.NewReader(target))
			for {
				window, streamErr := stream.NextWindow()
				if streamErr == io.EOF {
					break
				}
				if streamErr != nil {
					t.Fatal(streamErr)
				}
				if err := encoder.WriteWindow(*window); err != nil {
					t.Fatal(err)
				}
			}
			if err := encoder.Close(); err != nil {
				t.Fatal(err)
			}
			if encoded.Len() > referenceSize*120/100 {
				t.Fatalf("encoded size %d exceeds 120%% of reference size %d", encoded.Len(), referenceSize)
			}
			decoder, err := NewDecoder(bytes.NewReader(encoded.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			var reconstructed bytes.Buffer
			if _, err := Apply(bytes.NewReader(source), &reconstructed, decoder, nil); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(reconstructed.Bytes(), target) {
				t.Fatal("reconstructed fixture differs")
			}
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestSendContents(t *testing.T) {
	var windows []Window
	closed := false
	handler := &recordingWindowHandler{windows: &windows, closed: &closed}
	checksum, err := SendContents([]byte("contents"), handler)
	if err != nil || !closed || !checksum.Equal(svn.Sum(svn.ChecksumMD5, []byte("contents"))) {
		t.Fatalf("SendContents = %s, closed %v, error %v", checksum.Hex(), closed, err)
	}
}

func FuzzTxDeltaRoundTrip(f *testing.F) {
	f.Add([]byte("source contents"), []byte("source changed contents"))
	f.Add([]byte(nil), []byte("new file"))
	f.Fuzz(func(t *testing.T, source, target []byte) {
		if len(source) > MaxWindowSize*2 || len(target) > MaxWindowSize*2 {
			t.Skip()
		}
		stream := NewTxDeltaStream(bytes.NewReader(source), bytes.NewReader(target))
		var reconstructed bytes.Buffer
		if _, err := Apply(bytes.NewReader(source), &reconstructed, stream, nil); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(reconstructed.Bytes(), target) {
			t.Fatal("reconstructed target differs")
		}
	})
}

type recordingWindowHandler struct {
	windows *[]Window
	closed  *bool
}

func (handler *recordingWindowHandler) Window(window *Window) error {
	*handler.windows = append(*handler.windows, *window)
	return nil
}

func (handler *recordingWindowHandler) Close() error {
	*handler.closed = true
	return nil
}
