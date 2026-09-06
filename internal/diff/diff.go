package diff

import (
	"bytes"
	"fmt"
	"strings"
)

type Options struct {
	IgnoreAllSpace    bool
	IgnoreSpaceChange bool
	IgnoreEOL         bool
	ContextLines      int
	ContextSet        bool
}

type OperationKind uint8

const (
	Equal OperationKind = iota
	Delete
	Add
)

type Operation struct {
	Kind OperationKind
	Line string
}

func Lines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func normalize(line string, options Options) string {
	body, ending := splitLineEnding(line)
	if options.IgnoreEOL {
		ending = ""
	}
	if options.IgnoreAllSpace {
		return strings.Join(strings.Fields(body), "") + ending
	}
	if options.IgnoreSpaceChange {
		return strings.Join(strings.Fields(body), " ") + ending
	}
	return body + ending
}

func splitLineEnding(line string) (string, string) {
	if strings.HasSuffix(line, "\r\n") {
		return strings.TrimSuffix(line, "\r\n"), "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return strings.TrimSuffix(line, "\n"), "\n"
	}
	if strings.HasSuffix(line, "\r") {
		return strings.TrimSuffix(line, "\r"), "\r"
	}
	return line, ""
}

func Operations(oldData, newData []byte, options Options) []Operation {
	oldLines, newLines := Lines(oldData), Lines(newData)
	table := make([][]int, len(oldLines)+1)
	for index := range table {
		table[index] = make([]int, len(newLines)+1)
	}
	for oldIndex := len(oldLines) - 1; oldIndex >= 0; oldIndex-- {
		for newIndex := len(newLines) - 1; newIndex >= 0; newIndex-- {
			if normalize(oldLines[oldIndex], options) == normalize(newLines[newIndex], options) {
				table[oldIndex][newIndex] = table[oldIndex+1][newIndex+1] + 1
			} else if table[oldIndex+1][newIndex] >= table[oldIndex][newIndex+1] {
				table[oldIndex][newIndex] = table[oldIndex+1][newIndex]
			} else {
				table[oldIndex][newIndex] = table[oldIndex][newIndex+1]
			}
		}
	}
	operations := make([]Operation, 0, len(oldLines)+len(newLines))
	for oldIndex, newIndex := 0, 0; oldIndex < len(oldLines) || newIndex < len(newLines); {
		switch {
		case oldIndex < len(oldLines) && newIndex < len(newLines) && normalize(oldLines[oldIndex], options) == normalize(newLines[newIndex], options):
			operations = append(operations, Operation{Kind: Equal, Line: newLines[newIndex]})
			oldIndex++
			newIndex++
		case newIndex < len(newLines) && (oldIndex == len(oldLines) || table[oldIndex][newIndex+1] > table[oldIndex+1][newIndex]):
			operations = append(operations, Operation{Kind: Add, Line: newLines[newIndex]})
			newIndex++
		default:
			operations = append(operations, Operation{Kind: Delete, Line: oldLines[oldIndex]})
			oldIndex++
		}
	}
	return operations
}

func Unified(oldData, newData []byte, oldLabel, newLabel string, options Options) []byte {
	return unified(oldData, newData, oldLabel, newLabel, options, true, "@@", "file")
}

func UnifiedProperty(oldData, newData []byte) []byte {
	return unified(oldData, newData, "", "", Options{}, false, "##", "property")
}

func unified(oldData, newData []byte, oldLabel, newLabel string, options Options, headers bool, marker, noun string) []byte {
	operations := Operations(oldData, newData, options)
	var changes []int
	for index, operation := range operations {
		if operation.Kind != Equal {
			changes = append(changes, index)
		}
	}
	if len(changes) == 0 {
		return nil
	}
	var output bytes.Buffer
	if headers {
		fmt.Fprintf(&output, "--- %s\n+++ %s\n", oldLabel, newLabel)
	}
	contextLines := 3
	if options.ContextSet {
		contextLines = options.ContextLines
		if contextLines < 0 {
			contextLines = 0
		}
	}
	for changeIndex := 0; changeIndex < len(changes); {
		start := changes[changeIndex] - contextLines
		if start < 0 {
			start = 0
		}
		last := changes[changeIndex]
		changeIndex++
		for changeIndex < len(changes) && changes[changeIndex]-last <= 2*contextLines+1 {
			last = changes[changeIndex]
			changeIndex++
		}
		end := last + contextLines + 1
		if end > len(operations) {
			end = len(operations)
		}
		oldStart, newStart := linePositions(operations, start)
		oldCount, newCount := 0, 0
		for _, operation := range operations[start:end] {
			if operation.Kind != Add {
				oldCount++
			}
			if operation.Kind != Delete {
				newCount++
			}
		}
		fmt.Fprintf(&output, "%s -%s +%s %s\n", marker, formatRange(oldStart, oldCount), formatRange(newStart, newCount), marker)
		for _, operation := range operations[start:end] {
			prefix := byte(' ')
			if operation.Kind == Delete {
				prefix = '-'
			} else if operation.Kind == Add {
				prefix = '+'
			}
			output.WriteByte(prefix)
			output.WriteString(operation.Line)
			if !strings.HasSuffix(operation.Line, "\n") {
				fmt.Fprintf(&output, "\n\\ No newline at end of %s\n", noun)
			}
		}
	}
	return output.Bytes()
}

func linePositions(operations []Operation, end int) (int, int) {
	oldLine, newLine := 1, 1
	for _, operation := range operations[:end] {
		if operation.Kind != Add {
			oldLine++
		}
		if operation.Kind != Delete {
			newLine++
		}
	}
	return oldLine, newLine
}

func formatRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start-1)
	}
	if count == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func CarryOrigins(oldData, newData []byte, oldOrigins []int, revision int, options Options) []int {
	operations := Operations(oldData, newData, options)
	result := make([]int, 0, len(Lines(newData)))
	oldIndex := 0
	for _, operation := range operations {
		switch operation.Kind {
		case Equal:
			if oldIndex < len(oldOrigins) {
				result = append(result, oldOrigins[oldIndex])
			} else {
				result = append(result, revision)
			}
			oldIndex++
		case Delete:
			oldIndex++
		case Add:
			result = append(result, revision)
		}
	}
	return result
}
