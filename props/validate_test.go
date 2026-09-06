package props

import (
	"errors"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestCanonicalize(t *testing.T) {
	tests := []struct {
		name  string
		prop  string
		value string
		kind  svn.NodeKind
		want  string
		code  svn.Code
	}{
		{"executable", Executable, "yes", svn.NodeFile, "*", 0},
		{"needs lock", NeedsLock, "anything", svn.NodeFile, "*", 0},
		{"special", Special, "", svn.NodeSymlink, "*", 0},
		{"eol native", EOLStyle, " Native ", svn.NodeFile, "native", 0},
		{"eol crlf", EOLStyle, "crlf", svn.NodeFile, "CRLF", 0},
		{"bad eol", EOLStyle, "mixed", svn.NodeFile, "", svn.ErrIOUnknownEol},
		{"keywords", Keywords, "Rev  Author\tURL", svn.NodeFile, "Rev Author URL", 0},
		{"mime", MIMEType, "Text/Plain; charset=utf-8", svn.NodeFile, "text/plain; charset=utf-8", 0},
		{"bad mime", MIMEType, "not a mime", svn.NodeFile, "", svn.ErrBadMimeType},
		{"directory ignore", Ignore, "one\r\ntwo\r", svn.NodeDir, "one\ntwo\n", 0},
		{"log", Log, "message\r\n", svn.NodeNone, "message\n", 0},
		{"log adds newline", Log, "message", svn.NodeNone, "message\n", 0},
		{"file prop on dir", Executable, "*", svn.NodeDir, "", svn.ErrBadPropKind},
		{"dir prop on file", Ignore, "x", svn.NodeFile, "", svn.ErrBadPropKind},
		{"revision prop on node", Log, "x", svn.NodeFile, "", svn.ErrBadPropKind},
		{"unknown reserved", "svn:not-real", "x", svn.NodeFile, "", svn.ErrClientPropertyName},
		{"custom binary", "custom", "\x00", svn.NodeFile, "\x00", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Canonicalize(test.prop, []byte(test.value), test.kind)
			if test.code != 0 {
				if !errors.Is(err, test.code) {
					t.Fatalf("error = %v, want %s", err, test.code)
				}
				return
			}
			if err != nil || string(got) != test.want {
				t.Fatalf("got %q, error %v, want %q", got, err, test.want)
			}
		})
	}
}

func TestDetectMIMEType(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"readme.txt", []byte("hello"), "text/plain"},
		{"unknown", []byte("hello\nworld"), "text/plain"},
		{"unknown", []byte{'a', 0, 'b'}, "application/octet-stream"},
	}
	for _, test := range tests {
		if got := DetectMIMEType(test.name, test.data); got != test.want {
			t.Errorf("DetectMIMEType(%q) = %q, want %q", test.name, got, test.want)
		}
	}
	for _, value := range []string{"application/octet-stream", "application/xml", "application/json", "image/svg+xml", "image/png"} {
		if !IsBinaryMIMEType(value) {
			t.Errorf("%q should be binary", value)
		}
	}
	for _, value := range []string{"text/plain", "image/x-xbitmap", "image/x-xpixmap"} {
		if IsBinaryMIMEType(value) {
			t.Errorf("%q should be textual", value)
		}
	}
}

func TestComplexPropertyValidation(t *testing.T) {
	valid := []struct{ name, value string }{
		{Mergeinfo, "/trunk:1-4,7*"},
		{Externals, "-r 12 https://example.com/repo@14 vendor/repo"},
		{AutoProps, "*.txt = svn:eol-style=native;svn:mime-type=text/plain"},
		{Ignore, "*.o\nbuild"},
	}
	for _, test := range valid {
		kind := svn.NodeDir
		if _, err := Canonicalize(test.name, []byte(test.value), kind); err != nil {
			t.Errorf("%s: %v", test.name, err)
		}
	}
	invalid := []struct{ name, value string }{
		{Mergeinfo, "/trunk:4-1"},
		{Externals, "only-one-field"},
		{Externals, "-r nope https://example.com/repo target"},
		{AutoProps, "*.txt"},
		{AutoProps, "*.txt = svn:mime-type=not-a-mime"},
	}
	for _, test := range invalid {
		if _, err := Canonicalize(test.name, []byte(test.value), svn.NodeDir); err == nil {
			t.Errorf("%s accepted %q", test.name, test.value)
		}
	}
	if _, err := Canonicalize("svn:inheritable-anything", []byte("x"), svn.NodeDir); err == nil || !strings.Contains(err.Error(), "reserved property") {
		t.Fatalf("unknown inheritable property error = %v", err)
	}
}

func TestPropertyNameValidation(t *testing.T) {
	for _, name := range []string{"custom:name", "_private", "a-b.c_1"} {
		if _, err := Canonicalize(name, []byte("value"), svn.NodeFile); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", "1name", "bad name", "ümlaut", "bad/name"} {
		if _, err := Canonicalize(name, []byte("value"), svn.NodeFile); !errors.Is(err, svn.ErrClientPropertyName) {
			t.Errorf("%q error = %v", name, err)
		}
	}
}
