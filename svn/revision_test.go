package svn

import (
	"testing"
	"time"
)

func TestParseRevision(t *testing.T) {
	tests := []struct {
		input string
		kind  RevisionKind
		want  string
	}{
		{"HEAD", RevisionHead, "HEAD"},
		{"head", RevisionHead, "HEAD"},
		{"BASE", RevisionBase, "BASE"},
		{"COMMITTED", RevisionCommitted, "COMMITTED"},
		{"PREV", RevisionPrevious, "PREV"},
		{"PREVIOUS", RevisionPrevious, "PREV"},
		{"WORKING", RevisionWorking, "WORKING"},
		{"r123", RevisionNumber, "123"},
		{"R123", RevisionNumber, "123"},
		{"123", RevisionNumber, "123"},
		{"{2024-01-01}", RevisionDate, "{2024-01-01T00:00:00Z}"},
		{"{2024-01-01T12:34:56.123456Z}", RevisionDate, "{2024-01-01T12:34:56.123456Z}"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := ParseRevision(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != test.kind || got.String() != test.want {
				t.Fatalf("ParseRevision(%q) = %#v (%q), want kind %v and %q", test.input, got, got, test.kind, test.want)
			}
		})
	}
}

func TestParseRevisionRejectsInvalid(t *testing.T) {
	for _, input := range []string{"", "r", "-1", "1.2", "{today}", "{2024-99-99}"} {
		if _, err := ParseRevision(input); err == nil {
			t.Errorf("ParseRevision(%q) succeeded", input)
		}
	}
}

func TestParseRevisionRange(t *testing.T) {
	tests := map[string]string{
		"1":                                   "1",
		"1:HEAD":                              "1:HEAD",
		"HEAD:1":                              "HEAD:1",
		"{2024-01-01T12:30:00Z}:COMMITTED":    "{2024-01-01T12:30:00Z}:COMMITTED",
		"{2024-01-01T12:30:00Z}:{2024-02-01}": "{2024-01-01T12:30:00Z}:{2024-02-01T00:00:00Z}",
	}
	for input, want := range tests {
		got, err := ParseRevisionRange(input)
		if err != nil {
			t.Fatalf("ParseRevisionRange(%q): %v", input, err)
		}
		if got.String() != want {
			t.Errorf("ParseRevisionRange(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDepthRoundTrip(t *testing.T) {
	for _, want := range []Depth{DepthUnknown, DepthExclude, DepthEmpty, DepthFiles, DepthImmediates, DepthInfinity} {
		got, err := ParseDepth(want.String())
		if err != nil || got != want {
			t.Fatalf("ParseDepth(%q) = %v, %v", want, got, err)
		}
	}
}

func TestRevisionDateComparisonSupportsComparableRevision(t *testing.T) {
	date := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	first := Revision{Kind: RevisionDate, Date: date}
	second := Revision{Kind: RevisionDate, Date: date}
	if first != second {
		t.Fatal("equal date revisions differ")
	}
}
