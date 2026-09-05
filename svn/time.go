package svn

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

const dateLayout = "2006-01-02T15:04:05.000000Z"

var (
	canonicalDatePattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})\.(\d{1,6})Z$`)
	legacyDatePattern    = regexp.MustCompile(`^(Sun|Mon|Tue|Wed|Thu|Fri|Sat) (\d{1,2}) (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) (\d{4}) (\d{2}):(\d{2}):(\d{2})\.(\d{1,6}) \(day (\d{3}), dst ([01]), gmt_off ([+-]?\d{1,6})\)$`)
)

func FormatDate(value time.Time) string {
	return value.UTC().Truncate(time.Microsecond).Format(dateLayout)
}

func ParseDate(value string) (time.Time, error) {
	if match := canonicalDatePattern.FindStringSubmatch(value); match != nil {
		microseconds := match[7]
		for len(microseconds) < 6 {
			microseconds += "0"
		}
		parsed, err := time.Parse(dateLayout, value[:len(value)-len(match[7])-1]+microseconds+"Z")
		if err == nil {
			return parsed, nil
		}
	}
	if match := legacyDatePattern.FindStringSubmatch(value); match != nil {
		return parseLegacyDate(match)
	}
	return time.Time{}, fmt.Errorf("%w: %q", ErrBadDate, value)
}

func parseLegacyDate(match []string) (time.Time, error) {
	month, err := time.Parse("Jan", match[3])
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrBadDate, err)
	}
	values := make([]int, 0, 7)
	for _, field := range []string{match[2], match[4], match[5], match[6], match[7], match[8], match[11]} {
		value, parseErr := strconv.Atoi(field)
		if parseErr != nil {
			return time.Time{}, fmt.Errorf("%w: %v", ErrBadDate, parseErr)
		}
		values = append(values, value)
	}
	microseconds := match[8]
	for len(microseconds) < 6 {
		microseconds += "0"
	}
	microsecond, _ := strconv.Atoi(microseconds)
	offsetSeconds := values[6]
	location := time.FixedZone("legacy-svn", offsetSeconds)
	parsed := time.Date(values[1], month.Month(), values[0], values[2], values[3], values[4], microsecond*1000, location)
	if parsed.Year() != values[1] || int(parsed.Month()) != int(month.Month()) || parsed.Day() != values[0] {
		return time.Time{}, fmt.Errorf("%w: invalid calendar date", ErrBadDate)
	}
	return parsed.UTC(), nil
}
