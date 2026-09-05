package props

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

type KeywordValues map[string]string

type KeywordContext struct {
	Author   string
	Basename string
	Date     time.Time
	Path     string
	Revision svn.Revnum
	RootURL  string
	URL      string
}

func ParseKeywords(spec string, context KeywordContext) KeywordValues {
	values := make(KeywordValues)
	standard := map[string]string{
		"Rev": fmt.Sprint(context.Revision), "Revision": fmt.Sprint(context.Revision), "LastChangedRevision": fmt.Sprint(context.Revision),
		"Date": formatKeywordDate(context.Date), "LastChangedDate": formatKeywordDate(context.Date),
		"Author": context.Author, "LastChangedBy": context.Author,
		"URL": context.URL, "HeadURL": context.URL,
		"Id":     strings.TrimSpace(fmt.Sprintf("%s %d %s %s", context.Basename, context.Revision, formatKeywordDate(context.Date), context.Author)),
		"Header": strings.TrimSpace(fmt.Sprintf("%s %d %s %s", context.URL, context.Revision, formatKeywordDate(context.Date), context.Author)),
	}
	for _, field := range strings.Fields(spec) {
		name, format, custom := strings.Cut(field, "=")
		if custom {
			values[name] = expandKeywordFormat(format, context)
		} else if value, ok := standard[name]; ok {
			values[name] = value
		}
	}
	return values
}

func Translate(reader io.Reader, writer io.Writer, eol string, keywords KeywordValues, expand, repair bool) error {
	targetEOL, err := canonicalEOL(eol)
	if err != nil {
		return err
	}
	buffered := bufio.NewReader(reader)
	state := &translationState{writer: writer, eol: targetEOL, keywords: keywords, expand: expand, repair: repair}
	for {
		value, readErr := buffered.ReadByte()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
		if err := state.consume(value); err != nil {
			return err
		}
	}
	return state.close()
}

func DetranslateFile(reader io.Reader, writer io.Writer, eol string, keywords KeywordValues, repair bool) error {
	return Translate(reader, writer, eol, keywords, false, repair)
}

func EncodeSpecial(target string) []byte { return []byte("link " + target) }

func DecodeSpecial(contents []byte) (string, error) {
	if !bytes.HasPrefix(contents, []byte("link ")) {
		return "", fmt.Errorf("%w: special file does not start with link marker", svn.ErrBadPropertyValue)
	}
	return string(contents[len("link "):]), nil
}

type translationState struct {
	writer    io.Writer
	eol       []byte
	keywords  KeywordValues
	expand    bool
	repair    bool
	keyword   []byte
	inKeyword bool
	pendingCR bool
	seenEOL   byte
}

func (state *translationState) consume(value byte) error {
	if state.pendingCR {
		state.pendingCR = false
		if value == '\n' {
			if err := state.emitEOL(2); err != nil {
				return err
			}
			return nil
		}
		if err := state.emitEOL(1); err != nil {
			return err
		}
	}
	if value == '\r' {
		state.pendingCR = true
		return nil
	}
	if value == '\n' {
		return state.emitEOL(3)
	}
	return state.emitTextByte(value)
}

func (state *translationState) emitEOL(style byte) error {
	if state.seenEOL != 0 && state.seenEOL != style && !state.repair {
		return fmt.Errorf("%w: mixed line endings", svn.ErrIOInconsistentEol)
	}
	state.seenEOL = style
	for _, value := range state.eol {
		if err := state.emitTextByte(value); err != nil {
			return err
		}
	}
	return nil
}

func (state *translationState) emitTextByte(value byte) error {
	if !state.inKeyword {
		if value != '$' {
			return writeAll(state.writer, []byte{value})
		}
		state.inKeyword = true
		state.keyword = append(state.keyword[:0], value)
		return nil
	}
	state.keyword = append(state.keyword, value)
	if value == '$' && len(state.keyword) > 1 {
		result := translateKeyword(state.keyword, state.keywords, state.expand)
		state.inKeyword = false
		state.keyword = state.keyword[:0]
		return writeAll(state.writer, result)
	}
	if len(state.keyword) > 255 || value == '\n' || value == '\r' {
		state.inKeyword = false
		data := append([]byte(nil), state.keyword...)
		state.keyword = state.keyword[:0]
		return writeAll(state.writer, data)
	}
	return nil
}

func (state *translationState) close() error {
	if state.pendingCR {
		if err := state.emitEOL(1); err != nil {
			return err
		}
	}
	if state.inKeyword {
		return writeAll(state.writer, state.keyword)
	}
	return nil
}

func translateKeyword(candidate []byte, values KeywordValues, expand bool) []byte {
	inside := string(candidate[1 : len(candidate)-1])
	name := inside
	if index := strings.IndexByte(name, ':'); index >= 0 {
		name = name[:index]
	}
	name = strings.TrimSpace(name)
	value, enabled := values[name]
	if !enabled {
		return candidate
	}
	if !expand {
		return []byte("$" + name + "$")
	}
	if marker := strings.Index(inside, "::"); marker >= 0 {
		width := len(candidate)
		prefix := "$" + name + "::"
		available := width - len(prefix) - 1
		if available < 0 {
			return candidate
		}
		content := " " + value
		if len(content) > available {
			content = content[:available]
		}
		content += strings.Repeat(" ", available-len(content))
		return []byte(prefix + content + "$")
	}
	return []byte("$" + name + ": " + value + " $")
}

func canonicalEOL(style string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "", "none":
		return []byte("\n"), nil
	case "native":
		if runtime.GOOS == "windows" {
			return []byte("\r\n"), nil
		}
		return []byte("\n"), nil
	case "lf":
		return []byte("\n"), nil
	case "crlf":
		return []byte("\r\n"), nil
	case "cr":
		return []byte("\r"), nil
	default:
		return nil, fmt.Errorf("%w: %q", svn.ErrIOUnknownEol, style)
	}
}

func expandKeywordFormat(format string, context KeywordContext) string {
	values := map[byte]string{
		'a': context.Author, 'b': context.Basename, 'd': formatKeywordDate(context.Date), 'D': context.Date.UTC().Format(time.RFC3339),
		'P': context.Path, 'r': fmt.Sprint(context.Revision), 'R': context.RootURL, 'u': context.URL, '_': " ", '%': "%",
	}
	var result strings.Builder
	for index := 0; index < len(format); index++ {
		if format[index] == '%' && index+1 < len(format) {
			if replacement, ok := values[format[index+1]]; ok {
				result.WriteString(replacement)
				index++
				continue
			}
		}
		result.WriteByte(format[index])
	}
	return result.String()
}

func formatKeywordDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02 15:04:05 +0000 (Mon, 02 Jan 2006)")
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func KeywordNames(values KeywordValues) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
