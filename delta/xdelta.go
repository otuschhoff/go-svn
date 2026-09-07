package delta

import "bytes"

const (
	xdeltaBlockSize     = 64
	xdeltaMaxCandidates = 64
	xdeltaGoodMatch     = 1024
)

func buildSourceIndex(source []byte) map[uint32][]int {
	index := make(map[uint32][]int)
	if len(source) < xdeltaBlockSize {
		return index
	}
	hash := rollingHash(source[:xdeltaBlockSize])
	index[hash] = append(index[hash], 0)
	for position := 1; position+xdeltaBlockSize <= len(source); position++ {
		hash = rollHash(hash, source[position-1], source[position+xdeltaBlockSize-1])
		index[hash] = append(index[hash], position)
	}
	return index
}

func buildWindow(source, target []byte, sourceOffset int64) Window {
	window := Window{SourceOffset: sourceOffset, SourceLength: len(source), TargetLength: len(target)}
	index := buildSourceIndex(source)
	literalStart := 0
	position := 0
	for position < len(target) {
		matchOffset, matchLength := -1, 0
		if position+xdeltaBlockSize <= len(target) {
			hash := rollingHash(target[position : position+xdeltaBlockSize])
			candidates := index[hash]
			if len(candidates) > xdeltaMaxCandidates {
				candidates = candidates[:xdeltaMaxCandidates]
			}
			for _, candidate := range candidates {
				if !bytes.Equal(source[candidate:candidate+xdeltaBlockSize], target[position:position+xdeltaBlockSize]) {
					continue
				}
				length := xdeltaBlockSize
				for candidate+length < len(source) && position+length < len(target) && source[candidate+length] == target[position+length] {
					length++
				}
				if length > matchLength {
					matchOffset, matchLength = candidate, length
					if matchLength >= xdeltaGoodMatch {
						break
					}
				}
			}
		}
		if matchLength == 0 {
			position++
			continue
		}
		appendLiteral(&window, target[literalStart:position])
		window.Ops = append(window.Ops, Op{Kind: OpSource, Offset: matchOffset, Length: matchLength})
		position += matchLength
		literalStart = position
	}
	appendLiteral(&window, target[literalStart:])
	return window
}

func appendLiteral(window *Window, literal []byte) {
	if len(literal) == 0 {
		return
	}
	offset := len(window.NewData)
	window.NewData = append(window.NewData, literal...)
	window.Ops = append(window.Ops, Op{Kind: OpNew, Offset: offset, Length: len(literal)})
}

func rollingHash(block []byte) uint32 {
	var low, high uint32
	for index, value := range block {
		low += uint32(value)
		high += uint32(len(block)-index) * uint32(value)
	}
	return low | high<<16
}

func rollHash(hash uint32, outgoing, incoming byte) uint32 {
	low := uint32(uint16(hash)) - uint32(outgoing) + uint32(incoming)
	high := uint32(uint16(hash>>16)) - uint32(xdeltaBlockSize)*uint32(outgoing) + low
	return uint32(uint16(low)) | uint32(uint16(high))<<16
}
