package props

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/otuschhoff/go-svn/svn"
)

func TestTranslateKeywordsAndEOL(t *testing.T) {
	values := KeywordValues{"Rev": "42", "Author": "alice"}
	input := "one\r\n$Rev$\rtwo $Author: old $\nthree"
	var output bytes.Buffer
	if err := Translate(strings.NewReader(input), &output, "LF", values, true, true); err != nil {
		t.Fatal(err)
	}
	want := "one\n$Rev: 42 $\ntwo $Author: alice $\nthree"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	var detranslated bytes.Buffer
	if err := DetranslateFile(bytes.NewReader(output.Bytes()), &detranslated, "LF", values, true); err != nil {
		t.Fatal(err)
	}
	if detranslated.String() != "one\n$Rev$\ntwo $Author$\nthree" {
		t.Fatalf("detranslated = %q", detranslated.String())
	}
}

func TestTranslateFixedWidthKeyword(t *testing.T) {
	input := "$Rev::        $"
	var output bytes.Buffer
	if err := Translate(strings.NewReader(input), &output, "LF", KeywordValues{"Rev": "123456789"}, true, false); err != nil {
		t.Fatal(err)
	}
	if output.Len() != len(input) || output.String() != "$Rev:: 1234567$" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestTranslateRejectsMixedEOLWithoutRepair(t *testing.T) {
	var output bytes.Buffer
	err := Translate(strings.NewReader("a\nb\r\n"), &output, "LF", nil, true, false)
	if !errors.Is(err, svn.ErrIOInconsistentEol) {
		t.Fatalf("error = %v", err)
	}
}

func TestParseKeywordsCustomFormats(t *testing.T) {
	context := KeywordContext{Author: "alice", Basename: "f", Date: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), Path: "trunk/f", Revision: 7, RootURL: "https://host/repo", URL: "https://host/repo/trunk/f"}
	values := ParseKeywords("Rev Id Custom=%a%_%r%_%%", context)
	if values["Rev"] != "7" || values["Custom"] != "alice 7 %" || !strings.HasPrefix(values["Id"], "f 7 ") {
		t.Fatalf("values = %#v", values)
	}
}

func TestSpecialConversion(t *testing.T) {
	encoded := EncodeSpecial("../target")
	got, err := DecodeSpecial(encoded)
	if err != nil || got != "../target" {
		t.Fatalf("decoded = %q, error %v", got, err)
	}
	if _, err := DecodeSpecial([]byte("ordinary")); !errors.Is(err, svn.ErrBadPropertyValue) {
		t.Fatalf("error = %v", err)
	}
}

func FuzzTranslateRoundTrip(f *testing.F) {
	f.Add("line\n$Rev$\n")
	f.Add("mixed\r\n$Author: old $\r")
	f.Fuzz(func(t *testing.T, input string) {
		values := KeywordValues{"Rev": "12", "Author": "user"}
		var expanded bytes.Buffer
		if err := Translate(strings.NewReader(input), &expanded, "LF", values, true, true); err != nil {
			return
		}
		var collapsed bytes.Buffer
		if err := DetranslateFile(bytes.NewReader(expanded.Bytes()), &collapsed, "LF", values, true); err != nil {
			t.Fatal(err)
		}
		var normalized bytes.Buffer
		if err := Translate(strings.NewReader(input), &normalized, "LF", values, false, true); err != nil {
			t.Fatal(err)
		}
		if collapsed.String() != normalized.String() {
			t.Fatalf("round trip differs: %q != %q", collapsed.String(), normalized.String())
		}
	})
}
