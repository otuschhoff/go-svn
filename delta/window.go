// Package delta implements Subversion text delta windows and editors.
package delta

import (
	"fmt"
)

// OpKind identifies the source of bytes produced by an operation.
type OpKind uint8

const (
	OpSource OpKind = iota
	OpTarget
	OpNew
)

// Op copies Length bytes from Offset in the source view, target view, or
// window NewData, according to Kind.
type Op struct {
	Kind   OpKind
	Offset int
	Length int
}

// Window transforms a source view into one target view.
type Window struct {
	SourceOffset int64
	SourceLength int
	TargetLength int
	Ops          []Op
	NewData      []byte
}

// ApplyWindow applies one delta window to its already-selected source view.
func ApplyWindow(source []byte, window Window) ([]byte, error) {
	if err := window.Validate(); err != nil {
		return nil, err
	}
	if len(source) != window.SourceLength {
		return nil, invalidOps("source view length is %d, want %d", len(source), window.SourceLength)
	}

	target := make([]byte, 0, window.TargetLength)
	for _, operation := range window.Ops {
		switch operation.Kind {
		case OpSource:
			target = append(target, source[operation.Offset:operation.Offset+operation.Length]...)
		case OpTarget:
			for index := 0; index < operation.Length; index++ {
				target = append(target, target[operation.Offset+index])
			}
		case OpNew:
			target = append(target, window.NewData[operation.Offset:operation.Offset+operation.Length]...)
		}
	}
	return target, nil
}

// Validate checks all bounds and verifies that the operations produce exactly
// TargetLength bytes.
func (window Window) Validate() error {
	if window.SourceOffset < 0 || window.SourceLength < 0 || window.TargetLength < 0 {
		return invalidOps("negative window offset or length")
	}
	produced := 0
	for index, operation := range window.Ops {
		if operation.Offset < 0 || operation.Length <= 0 {
			return invalidOps("operation %d has invalid offset or length", index)
		}
		if operation.Length > window.TargetLength-produced {
			return invalidOps("operation %d exceeds target length", index)
		}
		switch operation.Kind {
		case OpSource:
			if operation.Offset > window.SourceLength-operation.Length {
				return invalidOps("source operation %d exceeds source view", index)
			}
		case OpTarget:
			if operation.Offset >= produced {
				return invalidOps("target operation %d starts beyond produced data", index)
			}
		case OpNew:
			if operation.Offset > len(window.NewData)-operation.Length {
				return invalidOps("new-data operation %d exceeds new data", index)
			}
		default:
			return invalidOps("operation %d has unknown kind %d", index, operation.Kind)
		}
		produced += operation.Length
	}
	if produced != window.TargetLength {
		return invalidOps("operations produce %d bytes, want %d", produced, window.TargetLength)
	}
	return nil
}

func invalidOps(format string, args ...any) error {
	return fmt.Errorf("%w: %s", svnErrorInvalidOps, fmt.Sprintf(format, args...))
}
