package props

import (
	"bytes"
	"fmt"
	"mime"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/oliver-tuschhoff/go-svn/mergeinfo"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

const (
	Executable    = "svn:executable"
	NeedsLock     = "svn:needs-lock"
	Special       = "svn:special"
	MIMEType      = "svn:mime-type"
	EOLStyle      = "svn:eol-style"
	Keywords      = "svn:keywords"
	Ignore        = "svn:ignore"
	GlobalIgnores = "svn:global-ignores"
	Externals     = "svn:externals"
	Mergeinfo     = "svn:mergeinfo"
	AutoProps     = "svn:auto-props"
	Date          = "svn:date"
	Log           = "svn:log"
)

var fileOnly = map[string]bool{
	Executable: true, NeedsLock: true, Special: true, MIMEType: true, EOLStyle: true, Keywords: true,
}

var directoryOnly = map[string]bool{
	Ignore: true, GlobalIgnores: true, Externals: true, AutoProps: true,
}

var knownNodeProps = map[string]bool{
	Executable: true, NeedsLock: true, Special: true, MIMEType: true, EOLStyle: true, Keywords: true,
	Ignore: true, GlobalIgnores: true, Externals: true, Mergeinfo: true, AutoProps: true,
}

// Canonicalize validates a property and returns its canonical Subversion value.
func Canonicalize(name string, value []byte, kind svn.NodeKind) ([]byte, error) {
	if !validPropertyName(name) {
		return nil, fmt.Errorf("%w: invalid property name %q", svn.ErrClientPropertyName, name)
	}
	if !svn.IsSVNProp(name) {
		return append([]byte(nil), value...), nil
	}
	if name == Date || name == Log {
		if kind != svn.NodeNone {
			return nil, wrongKind(name, kind)
		}
		if name == Date {
			parsed, err := svn.ParseDate(string(value))
			if err != nil {
				return nil, err
			}
			return []byte(svn.FormatDate(parsed)), nil
		}
		return normalizeText(value, true)
	}
	if !knownNodeProps[name] {
		return nil, fmt.Errorf("%w: reserved property %q", svn.ErrClientPropertyName, name)
	}
	if fileOnly[name] && kind != svn.NodeFile && kind != svn.NodeSymlink {
		return nil, wrongKind(name, kind)
	}
	if directoryOnly[name] && kind != svn.NodeDir {
		return nil, wrongKind(name, kind)
	}
	switch name {
	case Executable, NeedsLock, Special:
		return []byte("*"), nil
	case MIMEType:
		return canonicalMIME(value)
	case EOLStyle:
		style := strings.TrimSpace(string(value))
		switch strings.ToLower(style) {
		case "native":
			return []byte("native"), nil
		case "lf":
			return []byte("LF"), nil
		case "crlf":
			return []byte("CRLF"), nil
		case "cr":
			return []byte("CR"), nil
		default:
			return nil, fmt.Errorf("%w: unknown EOL style %q", svn.ErrIOUnknownEol, style)
		}
	case Keywords:
		fields := strings.Fields(string(value))
		return []byte(strings.Join(fields, " ")), nil
	case Mergeinfo:
		text, err := normalizeText(value, false)
		if err != nil {
			return nil, err
		}
		if _, err := mergeinfo.Parse(string(text)); err != nil {
			return nil, err
		}
		return text, nil
	case Externals:
		text, err := normalizeText(value, false)
		if err != nil {
			return nil, err
		}
		if err := validateExternals(string(text)); err != nil {
			return nil, err
		}
		return text, nil
	case AutoProps:
		text, err := normalizeText(value, false)
		if err != nil {
			return nil, err
		}
		if err := validateAutoProps(string(text)); err != nil {
			return nil, err
		}
		return text, nil
	case Ignore, GlobalIgnores:
		text, err := normalizeText(value, false)
		if err != nil {
			return nil, err
		}
		if err := validateIgnore(string(text)); err != nil {
			return nil, err
		}
		return text, nil
	default:
		return normalizeText(value, false)
	}
}

func validPropertyName(name string) bool {
	if name == "" || !isNameStart(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		value := name[index]
		if !isNameStart(value) && !(value >= '0' && value <= '9') && value != '-' && value != '.' {
			return false
		}
	}
	return true
}

func isNameStart(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value == ':' || value == '_'
}

func Validate(name string, value []byte, kind svn.NodeKind) error {
	_, err := Canonicalize(name, value, kind)
	return err
}

func IsBinaryMIMEType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return !strings.HasPrefix(mediaType, "text/") && mediaType != "image/x-xbitmap" && mediaType != "image/x-xpixmap"
}

func DetectMIMEType(name string, data []byte) string {
	if extension := filepath.Ext(name); extension != "" {
		if detected := mime.TypeByExtension(extension); detected != "" {
			mediaType, _, _ := mime.ParseMediaType(detected)
			return mediaType
		}
	}
	if bytes.IndexByte(data, 0) >= 0 || hasBinaryControls(data) {
		return "application/octet-stream"
	}
	return "text/plain"
}

func canonicalMIME(value []byte) ([]byte, error) {
	text := strings.TrimSpace(string(value))
	if !utf8.ValidString(text) || strings.ContainsAny(text, "\x00\r\n") {
		return nil, fmt.Errorf("%w: %q", svn.ErrBadMimeType, text)
	}
	mediaType, params, err := mime.ParseMediaType(text)
	if err != nil || !strings.Contains(mediaType, "/") {
		return nil, fmt.Errorf("%w: %q", svn.ErrBadMimeType, text)
	}
	return []byte(mime.FormatMediaType(strings.ToLower(mediaType), params)), nil
}

func validateIgnore(value string) error {
	for _, line := range strings.Split(value, "\n") {
		if strings.IndexByte(line, 0) >= 0 {
			return fmt.Errorf("%w: invalid ignore pattern", svn.ErrBadPropertyValue)
		}
	}
	return nil
}

func validateAutoProps(value string) error {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pattern, assignments, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(pattern) == "" || strings.TrimSpace(assignments) == "" {
			return fmt.Errorf("%w: invalid auto-props line %q", svn.ErrBadPropertyValue, line)
		}
		for _, assignment := range strings.Split(assignments, ";") {
			assignment = strings.TrimSpace(assignment)
			if assignment == "" {
				continue
			}
			name, rawValue, hasValue := strings.Cut(assignment, "=")
			name = strings.TrimSpace(name)
			if name == "" || strings.ContainsAny(name, " \t\r\n") {
				return fmt.Errorf("%w: invalid auto-props assignment %q", svn.ErrBadPropertyValue, assignment)
			}
			if hasValue && name == MIMEType {
				if _, err := canonicalMIME([]byte(strings.TrimSpace(rawValue))); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateExternals(value string) error {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("%w: invalid external %q", svn.ErrBadPropertyValue, line)
		}
		for len(fields) > 0 && (fields[0] == "-r" || strings.HasPrefix(fields[0], "-r")) {
			if fields[0] == "-r" {
				fields = fields[1:]
				if len(fields) == 0 {
					return fmt.Errorf("%w: missing external revision", svn.ErrBadPropertyValue)
				}
			}
			revision := strings.TrimPrefix(fields[0], "-r")
			if _, err := strconv.ParseInt(revision, 10, 64); err != nil {
				return fmt.Errorf("%w: invalid external revision %q", svn.ErrBadPropertyValue, revision)
			}
			fields = fields[1:]
		}
		if len(fields) != 2 {
			return fmt.Errorf("%w: invalid external %q", svn.ErrBadPropertyValue, line)
		}
		if strings.Contains(fields[1], "@") {
			base, peg, _ := strings.Cut(fields[1], "@")
			if base != "" && peg != "" {
				if _, err := strconv.ParseInt(peg, 10, 64); err != nil {
					return fmt.Errorf("%w: invalid peg revision %q", svn.ErrBadPropertyValue, peg)
				}
			}
		}
		if parsed, err := url.Parse(fields[1]); err != nil || parsed.Scheme != "" && parsed.Host == "" {
			return fmt.Errorf("%w: invalid external URL %q", svn.ErrBadPropertyValue, fields[1])
		}
	}
	return nil
}

func normalizeText(value []byte, ensureTrailingLF bool) ([]byte, error) {
	if !utf8.Valid(value) || bytes.IndexByte(value, 0) >= 0 {
		return nil, fmt.Errorf("%w: property value is not valid UTF-8 text", svn.ErrBadPropertyValue)
	}
	result := bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n"))
	result = bytes.ReplaceAll(result, []byte("\r"), []byte("\n"))
	if ensureTrailingLF && len(result) > 0 && result[len(result)-1] != '\n' {
		result = append(result, '\n')
	}
	return result, nil
}

func hasBinaryControls(data []byte) bool {
	for _, value := range data {
		if value < 0x20 && value != '\t' && value != '\n' && value != '\r' && value != '\f' && value != '\b' {
			return true
		}
	}
	return false
}

func wrongKind(name string, kind svn.NodeKind) error {
	return fmt.Errorf("%w: property %q cannot be set on %s", svn.ErrBadPropKind, name, kind)
}
