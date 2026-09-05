package rasvn

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

const (
	MaxStringLength = 64 << 20
	MaxNestingDepth = 64
	maxWordLength   = 31
)

type ItemKind uint8

const (
	NumberKind ItemKind = iota
	WordKind
	StringKind
	ListKind
)

type Item struct {
	Kind   ItemKind
	Number uint64
	Word   string
	String []byte
	List   []Item
}

func Number(value uint64) Item { return Item{Kind: NumberKind, Number: value} }
func Word(value string) Item   { return Item{Kind: WordKind, Word: value} }
func String(value []byte) Item {
	if value == nil {
		value = []byte{}
	}
	return Item{Kind: StringKind, String: value}
}
func List(values ...Item) Item { return Item{Kind: ListKind, List: values} }

type Decoder struct {
	reader *bufio.Reader
}

type Reader = Decoder

func NewReader(reader io.Reader) *Reader { return NewDecoder(reader) }

func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{reader: bufio.NewReader(reader)}
}

func (decoder *Decoder) Decode() (Item, error) {
	first, err := decoder.readNonWhitespace()
	if err != nil {
		return Item{}, err
	}
	return decoder.decode(first, 0)
}

func (decoder *Decoder) decode(first byte, depth int) (Item, error) {
	if depth >= MaxNestingDepth {
		return Item{}, malformed("items are nested too deeply")
	}
	switch {
	case first >= '0' && first <= '9':
		return decoder.decodeNumberOrString(first)
	case isAlpha(first):
		return decoder.decodeWord(first)
	case first == '(':
		return decoder.decodeList(depth)
	default:
		return Item{}, malformed("unexpected byte %q", first)
	}
}

func (decoder *Decoder) decodeNumberOrString(first byte) (Item, error) {
	digits := []byte{first}
	for {
		value, err := decoder.reader.ReadByte()
		if err != nil {
			return Item{}, malformedCause(err, "unterminated number")
		}
		if value >= '0' && value <= '9' {
			if len(digits) == 20 {
				return Item{}, malformed("number is larger than maximum")
			}
			digits = append(digits, value)
			continue
		}
		number, err := strconv.ParseUint(string(digits), 10, 64)
		if err != nil {
			return Item{}, malformed("number is larger than maximum")
		}
		if value == ':' {
			if number > MaxStringLength {
				return Item{}, malformed("string length %d exceeds limit %d", number, MaxStringLength)
			}
			data := make([]byte, int(number))
			if _, err := io.ReadFull(decoder.reader, data); err != nil {
				return Item{}, malformedCause(err, "short string")
			}
			if err := decoder.requireWhitespace(); err != nil {
				return Item{}, err
			}
			return String(data), nil
		}
		if !isWhitespace(value) {
			return Item{}, malformed("number is not followed by whitespace")
		}
		return Number(number), nil
	}
}

func (decoder *Decoder) decodeWord(first byte) (Item, error) {
	word := []byte{first}
	for {
		value, err := decoder.reader.ReadByte()
		if err != nil {
			return Item{}, malformedCause(err, "unterminated word")
		}
		if isAlpha(value) || value >= '0' && value <= '9' || value == '-' {
			if len(word) == maxWordLength {
				return Item{}, malformed("word is too long")
			}
			word = append(word, value)
			continue
		}
		if !isWhitespace(value) {
			return Item{}, malformed("word is not followed by whitespace")
		}
		return Word(string(word)), nil
	}
}

func (decoder *Decoder) decodeList(depth int) (Item, error) {
	values := make([]Item, 0)
	for {
		first, err := decoder.readNonWhitespace()
		if err != nil {
			return Item{}, malformedCause(err, "unterminated list")
		}
		if first == ')' {
			if err := decoder.requireWhitespace(); err != nil {
				return Item{}, err
			}
			return List(values...), nil
		}
		value, err := decoder.decode(first, depth+1)
		if err != nil {
			return Item{}, err
		}
		values = append(values, value)
	}
}

func (decoder *Decoder) readNonWhitespace() (byte, error) {
	for {
		value, err := decoder.reader.ReadByte()
		if err != nil {
			return 0, err
		}
		if !isWhitespace(value) {
			return value, nil
		}
	}
}

func (decoder *Decoder) requireWhitespace() error {
	value, err := decoder.reader.ReadByte()
	if err != nil {
		return malformedCause(err, "item has no trailing whitespace")
	}
	if !isWhitespace(value) {
		return malformed("item has no trailing whitespace")
	}
	return nil
}

type Encoder struct {
	writer *bufio.Writer
}

type Writer = Encoder

func NewWriter(writer io.Writer) *Writer { return NewEncoder(writer) }

func NewEncoder(writer io.Writer) *Encoder {
	return &Encoder{writer: bufio.NewWriter(writer)}
}

func (encoder *Encoder) Encode(item Item) error {
	return encoder.encode(item, 0)
}

func (encoder *Encoder) Flush() error { return encoder.writer.Flush() }

func (encoder *Encoder) WriteTuple(pattern string, values ...any) error {
	items, wrapped, err := buildTuple(pattern, values)
	if err != nil {
		return err
	}
	if wrapped {
		return encoder.Encode(List(items...))
	}
	for _, item := range items {
		if err := encoder.Encode(item); err != nil {
			return err
		}
	}
	return nil
}

func (encoder *Encoder) encode(item Item, depth int) error {
	if depth >= MaxNestingDepth {
		return malformed("items are nested too deeply")
	}
	switch item.Kind {
	case NumberKind:
		_, err := fmt.Fprintf(encoder.writer, "%d ", item.Number)
		return err
	case WordKind:
		if !validWord(item.Word) {
			return malformed("invalid word %q", item.Word)
		}
		_, err := io.WriteString(encoder.writer, item.Word+" ")
		return err
	case StringKind:
		if len(item.String) > MaxStringLength {
			return malformed("string length %d exceeds limit %d", len(item.String), MaxStringLength)
		}
		if _, err := fmt.Fprintf(encoder.writer, "%d:", len(item.String)); err != nil {
			return err
		}
		if _, err := encoder.writer.Write(item.String); err != nil {
			return err
		}
		return encoder.writer.WriteByte(' ')
	case ListKind:
		if _, err := io.WriteString(encoder.writer, "( "); err != nil {
			return err
		}
		for _, value := range item.List {
			if err := encoder.encode(value, depth+1); err != nil {
				return err
			}
		}
		_, err := io.WriteString(encoder.writer, ") ")
		return err
	default:
		return malformed("unknown item kind %d", item.Kind)
	}
}

func validWord(word string) bool {
	if len(word) == 0 || len(word) > maxWordLength || !isAlpha(word[0]) {
		return false
	}
	for index := 1; index < len(word); index++ {
		value := word[index]
		if !isAlpha(value) && !(value >= '0' && value <= '9') && value != '-' {
			return false
		}
	}
	return true
}

func isAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isWhitespace(value byte) bool { return value == ' ' || value == '\n' }

func malformed(format string, args ...any) error {
	return &ProtocolError{Message: fmt.Sprintf(format, args...)}
}

func malformedCause(cause error, format string, args ...any) error {
	return &ProtocolError{Message: fmt.Sprintf(format, args...), Cause: cause}
}

const UnspecifiedNumber = ^uint64(0)

type TriState int8

const (
	TriStateUnknown TriState = iota
	TriStateFalse
	TriStateTrue
)

func ParseTuple(items []Item, pattern string, values ...any) error {
	patternIndex, valueIndex := 0, 0
	if len(pattern) > 0 && pattern[0] == '!' {
		patternIndex++
	}
	_, err := parseTupleItems(items, pattern, &patternIndex, values, &valueIndex, false)
	if err != nil {
		return err
	}
	if valueIndex != len(values) {
		return malformed("tuple pattern consumed %d of %d destinations", valueIndex, len(values))
	}
	return nil
}

func parseTupleItems(items []Item, pattern string, patternIndex *int, values []any, valueIndex *int, nested bool) (int, error) {
	itemIndex := 0
	optional := false
	for *patternIndex < len(pattern) {
		code := pattern[*patternIndex]
		*patternIndex++
		switch code {
		case '?':
			optional = true
			continue
		case ')':
			if nested {
				return itemIndex, nil
			}
			return 0, malformed("unmatched tuple close")
		case '!':
			if *patternIndex == len(pattern) {
				return itemIndex, nil
			}
			return 0, malformed("invalid ! in tuple pattern")
		}
		if itemIndex == len(items) {
			if !optional {
				return 0, malformed("tuple ended before required %c item", code)
			}
			if err := setMissingTupleValue(code, pattern, patternIndex, values, valueIndex); err != nil {
				return 0, err
			}
			continue
		}
		if code == '(' {
			if items[itemIndex].Kind != ListKind {
				return 0, malformed("tuple item %d is not a list", itemIndex)
			}
			if _, err := parseTupleItems(items[itemIndex].List, pattern, patternIndex, values, valueIndex, true); err != nil {
				return 0, err
			}
			itemIndex++
			optional = false
			continue
		}
		if err := assignTupleValue(items[itemIndex], code, values, valueIndex); err != nil {
			return 0, err
		}
		itemIndex++
	}
	if nested {
		return 0, malformed("unterminated tuple group")
	}
	return itemIndex, nil
}

func assignTupleValue(item Item, code byte, values []any, valueIndex *int) error {
	if *valueIndex >= len(values) {
		return malformed("missing destination for tuple item %c", code)
	}
	destination := values[*valueIndex]
	*valueIndex++
	switch code {
	case 'c':
		if item.Kind != StringKind {
			return malformed("tuple item is not a string")
		}
		pointer, ok := destination.(*string)
		if !ok {
			return malformed("destination for c is %T", destination)
		}
		*pointer = string(item.String)
	case 's':
		if item.Kind != StringKind {
			return malformed("tuple item is not a string")
		}
		pointer, ok := destination.(*[]byte)
		if !ok {
			return malformed("destination for s is %T", destination)
		}
		*pointer = append((*pointer)[:0], item.String...)
	case 'w':
		if item.Kind != WordKind {
			return malformed("tuple item is not a word")
		}
		pointer, ok := destination.(*string)
		if !ok {
			return malformed("destination for w is %T", destination)
		}
		*pointer = item.Word
	case 'r':
		if item.Kind != NumberKind || item.Number > uint64(^uint64(0)>>1) {
			return malformed("tuple item is not a valid revision")
		}
		pointer, ok := destination.(*svn.Revnum)
		if !ok {
			return malformed("destination for r is %T", destination)
		}
		*pointer = svn.Revnum(item.Number)
	case 'n':
		if item.Kind != NumberKind {
			return malformed("tuple item is not a number")
		}
		pointer, ok := destination.(*uint64)
		if !ok {
			return malformed("destination for n is %T", destination)
		}
		*pointer = item.Number
	case 'b', 'B', '3':
		if item.Kind != WordKind || item.Word != "true" && item.Word != "false" {
			return malformed("tuple item is not a boolean")
		}
		if code == 'b' {
			pointer, ok := destination.(*bool)
			if !ok {
				return malformed("destination for b is %T", destination)
			}
			*pointer = item.Word == "true"
		} else if code == 'B' {
			pointer, ok := destination.(*uint64)
			if !ok {
				return malformed("destination for B is %T", destination)
			}
			if item.Word == "true" {
				*pointer = 1
			} else {
				*pointer = 0
			}
		} else {
			pointer, ok := destination.(*TriState)
			if !ok {
				return malformed("destination for 3 is %T", destination)
			}
			if item.Word == "true" {
				*pointer = TriStateTrue
			} else {
				*pointer = TriStateFalse
			}
		}
	case 'l':
		if item.Kind != ListKind {
			return malformed("tuple item is not a list")
		}
		pointer, ok := destination.(*[]Item)
		if !ok {
			return malformed("destination for l is %T", destination)
		}
		*pointer = item.List
	default:
		return malformed("unknown tuple pattern %c", code)
	}
	return nil
}

func setMissingTupleValue(code byte, pattern string, patternIndex *int, values []any, valueIndex *int) error {
	if code == '(' {
		depth := 1
		for *patternIndex < len(pattern) && depth > 0 {
			nestedCode := pattern[*patternIndex]
			*patternIndex++
			if nestedCode == '(' {
				depth++
			} else if nestedCode == ')' {
				depth--
			} else if nestedCode != '?' {
				if err := setMissingTupleValue(nestedCode, pattern, patternIndex, values, valueIndex); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if *valueIndex >= len(values) {
		return malformed("missing destination for optional tuple item %c", code)
	}
	destination := values[*valueIndex]
	*valueIndex++
	switch pointer := destination.(type) {
	case *string:
		*pointer = ""
	case *[]byte:
		*pointer = nil
	case *svn.Revnum:
		*pointer = svn.InvalidRevnum
	case *uint64:
		*pointer = UnspecifiedNumber
	case *bool:
		*pointer = false
	case *TriState:
		*pointer = TriStateUnknown
	case *[]Item:
		*pointer = nil
	default:
		return malformed("unsupported optional destination %T", destination)
	}
	return nil
}

func buildTuple(pattern string, values []any) ([]Item, bool, error) {
	wrapped := true
	patternIndex := 0
	if len(pattern) > 0 && pattern[0] == '!' {
		wrapped = false
		patternIndex++
	}
	valueIndex := 0
	items, _, err := buildTupleItems(pattern, &patternIndex, values, &valueIndex, false)
	if err != nil {
		return nil, false, err
	}
	if valueIndex != len(values) {
		return nil, false, malformed("tuple pattern consumed %d of %d values", valueIndex, len(values))
	}
	return items, wrapped, nil
}

func buildTupleItems(pattern string, patternIndex *int, values []any, valueIndex *int, nested bool) ([]Item, bool, error) {
	items := make([]Item, 0)
	optional := false
	for *patternIndex < len(pattern) {
		code := pattern[*patternIndex]
		*patternIndex++
		switch code {
		case '?':
			optional = true
			continue
		case ')':
			if !nested {
				return nil, false, malformed("unmatched tuple close")
			}
			return items, true, nil
		case '!':
			if *patternIndex == len(pattern) {
				return items, false, nil
			}
			return nil, false, malformed("invalid ! in tuple pattern")
		case '(':
			nestedItems, present, err := buildTupleItems(pattern, patternIndex, values, valueIndex, true)
			if err != nil {
				return nil, false, err
			}
			if present || !optional {
				items = append(items, List(nestedItems...))
			}
			optional = false
			continue
		}
		if *valueIndex >= len(values) {
			return nil, false, malformed("missing value for tuple item %c", code)
		}
		value := values[*valueIndex]
		*valueIndex++
		item, present, err := tupleItem(code, value)
		if err != nil {
			return nil, false, err
		}
		if present {
			items = append(items, item)
		} else if !optional {
			return nil, false, malformed("required tuple item %c is absent", code)
		}
	}
	if nested {
		return nil, false, malformed("unterminated tuple group")
	}
	return items, true, nil
}

func tupleItem(code byte, value any) (Item, bool, error) {
	switch code {
	case 'c':
		switch value := value.(type) {
		case string:
			return String([]byte(value)), true, nil
		case *string:
			if value == nil {
				return Item{}, false, nil
			}
			return String([]byte(*value)), true, nil
		}
	case 's':
		switch value := value.(type) {
		case []byte:
			return String(value), true, nil
		case *[]byte:
			if value == nil {
				return Item{}, false, nil
			}
			return String(*value), true, nil
		}
	case 'w':
		if word, ok := value.(string); ok {
			return Word(word), true, nil
		}
	case 'r':
		if revision, ok := value.(svn.Revnum); ok {
			if revision == svn.InvalidRevnum {
				return Item{}, false, nil
			}
			if revision < 0 {
				return Item{}, false, malformed("negative revision %d", revision)
			}
			return Number(uint64(revision)), true, nil
		}
	case 'n':
		if number, ok := value.(uint64); ok {
			return Number(number), true, nil
		}
	case 'b':
		if boolean, ok := value.(bool); ok {
			if boolean {
				return Word("true"), true, nil
			}
			return Word("false"), true, nil
		}
	case 'l':
		switch value := value.(type) {
		case []Item:
			return List(value...), true, nil
		case *[]Item:
			if value == nil {
				return Item{}, false, nil
			}
			return List((*value)...), true, nil
		}
	}
	return Item{}, false, malformed("value %T does not match tuple pattern %c", value, code)
}

type ProtocolError struct {
	Message string
	Cause   error
}

func (err *ProtocolError) Error() string {
	if err.Cause == nil {
		return "ra_svn: " + err.Message
	}
	return "ra_svn: " + err.Message + ": " + err.Cause.Error()
}

func (err *ProtocolError) Unwrap() error {
	if err.Cause != nil {
		return errors.Join(err.Cause, errCodeMalformed)
	}
	return errCodeMalformed
}
