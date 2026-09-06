package diff3

import "bytes"

type Result struct {
	Contents   []byte
	Conflicted bool
}

type hunk struct {
	start, end int
	lines      [][]byte
}

func Merge(base, mine, theirs []byte, oldLabel, newLabel string) Result {
	baseLines := splitLines(base)
	mineHunks := differences(baseLines, splitLines(mine))
	theirHunks := differences(baseLines, splitLines(theirs))
	var output bytes.Buffer
	position, mineIndex, theirIndex := 0, 0, 0
	conflicted := false
	for mineIndex < len(mineHunks) || theirIndex < len(theirHunks) {
		next := len(baseLines)
		if mineIndex < len(mineHunks) && mineHunks[mineIndex].start < next {
			next = mineHunks[mineIndex].start
		}
		if theirIndex < len(theirHunks) && theirHunks[theirIndex].start < next {
			next = theirHunks[theirIndex].start
		}
		writeLines(&output, baseLines[position:next])
		end := next
		mineEnd, theirEnd := mineIndex, theirIndex
		for {
			changed := false
			for mineEnd < len(mineHunks) && touches(mineHunks[mineEnd], next, end) {
				if mineHunks[mineEnd].end > end {
					end = mineHunks[mineEnd].end
				}
				mineEnd++
				changed = true
			}
			for theirEnd < len(theirHunks) && touches(theirHunks[theirEnd], next, end) {
				if theirHunks[theirEnd].end > end {
					end = theirHunks[theirEnd].end
				}
				theirEnd++
				changed = true
			}
			if !changed {
				break
			}
		}
		mineChanged := mineEnd > mineIndex
		theirsChanged := theirEnd > theirIndex
		mineResult := apply(baseLines, next, end, mineHunks[mineIndex:mineEnd])
		theirResult := apply(baseLines, next, end, theirHunks[theirIndex:theirEnd])
		switch {
		case !mineChanged:
			writeLines(&output, theirResult)
		case !theirsChanged || equalLines(mineResult, theirResult):
			writeLines(&output, mineResult)
		default:
			conflicted = true
			output.WriteString("<<<<<<< .mine\n")
			writeLines(&output, mineResult)
			output.WriteString("||||||| ")
			output.WriteString(oldLabel)
			output.WriteByte('\n')
			writeLines(&output, baseLines[next:end])
			output.WriteString("=======\n")
			writeLines(&output, theirResult)
			output.WriteString(">>>>>>> ")
			output.WriteString(newLabel)
			output.WriteByte('\n')
		}
		position, mineIndex, theirIndex = end, mineEnd, theirEnd
	}
	writeLines(&output, baseLines[position:])
	return Result{Contents: output.Bytes(), Conflicted: conflicted}
}

func touches(change hunk, start, end int) bool {
	if start == end {
		return change.start == start
	}
	if change.start == change.end {
		return change.start >= start && change.start < end
	}
	return change.start < end && change.end > start
}

func apply(base [][]byte, start, end int, changes []hunk) [][]byte {
	var result [][]byte
	position := start
	for _, change := range changes {
		result = append(result, base[position:change.start]...)
		result = append(result, change.lines...)
		position = change.end
	}
	return append(result, base[position:end]...)
}

func differences(base, target [][]byte) []hunk {
	lengths := make([][]int, len(base)+1)
	for index := range lengths {
		lengths[index] = make([]int, len(target)+1)
	}
	for left := len(base) - 1; left >= 0; left-- {
		for right := len(target) - 1; right >= 0; right-- {
			if bytes.Equal(base[left], target[right]) {
				lengths[left][right] = lengths[left+1][right+1] + 1
			} else if lengths[left+1][right] >= lengths[left][right+1] {
				lengths[left][right] = lengths[left+1][right]
			} else {
				lengths[left][right] = lengths[left][right+1]
			}
		}
	}
	left, right := 0, 0
	var result []hunk
	for left < len(base) || right < len(target) {
		if left < len(base) && right < len(target) && bytes.Equal(base[left], target[right]) {
			left++
			right++
			continue
		}
		start := left
		var replacement [][]byte
		for left < len(base) || right < len(target) {
			if left < len(base) && right < len(target) && bytes.Equal(base[left], target[right]) {
				break
			}
			if right < len(target) && (left == len(base) || lengths[left][right+1] > lengths[left+1][right]) {
				replacement = append(replacement, target[right])
				right++
			} else {
				left++
			}
		}
		result = append(result, hunk{start: start, end: left, lines: replacement})
	}
	return result
}

func splitLines(contents []byte) [][]byte {
	if len(contents) == 0 {
		return nil
	}
	var lines [][]byte
	for len(contents) > 0 {
		index := bytes.IndexByte(contents, '\n')
		if index < 0 {
			lines = append(lines, append([]byte(nil), contents...))
			break
		}
		lines = append(lines, append([]byte(nil), contents[:index+1]...))
		contents = contents[index+1:]
	}
	return lines
}

func writeLines(output *bytes.Buffer, lines [][]byte) {
	for _, line := range lines {
		output.Write(line)
	}
}

func equalLines(left, right [][]byte) bool {
	return bytes.Equal(join(left), join(right))
}

func join(lines [][]byte) []byte {
	return bytes.Join(lines, nil)
}
