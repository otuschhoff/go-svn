package svn

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Revnum int64

const InvalidRevnum Revnum = -1

func (r Revnum) IsValid() bool { return r >= 0 }

type RevisionKind uint8

const (
	RevisionUnspecified RevisionKind = iota
	RevisionNumber
	RevisionDate
	RevisionCommitted
	RevisionPrevious
	RevisionBase
	RevisionWorking
	RevisionHead
)

type Revision struct {
	Kind   RevisionKind
	Number Revnum
	Date   time.Time
}

type RevisionRange struct {
	Start Revision
	End   Revision
}

func ParseRevision(value string) (Revision, error) {
	value = strings.TrimSpace(value)
	switch strings.ToUpper(value) {
	case "HEAD":
		return Revision{Kind: RevisionHead}, nil
	case "BASE":
		return Revision{Kind: RevisionBase}, nil
	case "COMMITTED":
		return Revision{Kind: RevisionCommitted}, nil
	case "PREV", "PREVIOUS":
		return Revision{Kind: RevisionPrevious}, nil
	case "WORKING":
		return Revision{Kind: RevisionWorking}, nil
	}
	if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		date, err := parseRevisionDate(value[1 : len(value)-1])
		if err != nil {
			return Revision{}, fmt.Errorf("invalid revision %q: %w", value, err)
		}
		return Revision{Kind: RevisionDate, Date: date}, nil
	}
	number := value
	if len(number) > 0 && (number[0] == 'r' || number[0] == 'R') {
		number = number[1:]
	}
	parsed, err := strconv.ParseInt(number, 10, 64)
	if err != nil || parsed < 0 {
		return Revision{}, fmt.Errorf("invalid revision %q", value)
	}
	return Revision{Kind: RevisionNumber, Number: Revnum(parsed)}, nil
}

func ParseRevisionRange(value string) (RevisionRange, error) {
	separator := revisionRangeSeparator(value)
	if separator < 0 {
		revision, err := ParseRevision(value)
		return RevisionRange{Start: revision, End: revision}, err
	}
	start, err := ParseRevision(value[:separator])
	if err != nil {
		return RevisionRange{}, err
	}
	end, err := ParseRevision(value[separator+1:])
	if err != nil {
		return RevisionRange{}, err
	}
	return RevisionRange{Start: start, End: end}, nil
}

func revisionRangeSeparator(value string) int {
	braces := 0
	for index, char := range value {
		switch char {
		case '{':
			braces++
		case '}':
			braces--
		case ':':
			if braces == 0 {
				return index
			}
		}
	}
	return -1
}

func parseRevisionDate(value string) (time.Time, error) {
	formats := []string{"2006-01-02", time.RFC3339Nano}
	for _, format := range formats {
		if parsed, err := time.Parse(format, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date format")
}

func (r Revision) String() string {
	switch r.Kind {
	case RevisionNumber:
		return strconv.FormatInt(int64(r.Number), 10)
	case RevisionDate:
		return "{" + r.Date.Format(time.RFC3339Nano) + "}"
	case RevisionCommitted:
		return "COMMITTED"
	case RevisionPrevious:
		return "PREV"
	case RevisionBase:
		return "BASE"
	case RevisionWorking:
		return "WORKING"
	case RevisionHead:
		return "HEAD"
	default:
		return ""
	}
}

func (r RevisionRange) String() string {
	if r.Start == r.End {
		return r.Start.String()
	}
	return r.Start.String() + ":" + r.End.String()
}
