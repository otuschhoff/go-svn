package svn

import (
	"testing"
	"time"
)

func TestDateGoldenVectors(t *testing.T) {
	tests := map[string]string{
		"2002-05-13T19:00:50.966679Z":                                      "2002-05-13T19:00:50.966679Z",
		"2034-07-20T17:03:36.11379Z":                                       "2034-07-20T17:03:36.113790Z",
		"2024-06-08T13:06:14.897Z":                                         "2024-06-08T13:06:14.897000Z",
		"Mon 13 May 2002 22:00:50.966679 (day 133, dst 1, gmt_off 010800)": "2002-05-13T19:00:50.966679Z",
	}
	for input, want := range tests {
		parsed, err := ParseDate(input)
		if err != nil {
			t.Fatalf("ParseDate(%q): %v", input, err)
		}
		if got := FormatDate(parsed); got != want {
			t.Errorf("FormatDate(ParseDate(%q)) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatDateUsesUTCAndMicroseconds(t *testing.T) {
	location := time.FixedZone("test", -7*60*60)
	value := time.Date(2024, 6, 8, 6, 6, 14, 897123999, location)
	if got, want := FormatDate(value), "2024-06-08T13:06:14.897123Z"; got != want {
		t.Fatalf("FormatDate() = %q, want %q", got, want)
	}
}

func FuzzParseDate(f *testing.F) {
	for _, seed := range []string{"2002-05-13T19:00:50.966679Z", "", "not-a-date"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		parsed, err := ParseDate(value)
		if err == nil {
			if _, err := ParseDate(FormatDate(parsed)); err != nil {
				t.Fatalf("canonical date rejected: %v", err)
			}
		}
	})
}
