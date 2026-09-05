package svn

import (
	"errors"
	"fmt"
	"testing"
)

func TestKnownErrorCodes(t *testing.T) {
	tests := map[Code]int32{
		ErrFSNotFound:      160013,
		ErrRANotAuthorized: 170001,
		ErrWCLocked:        155004,
		ErrFSTxnOutOfDate:  160028,
		ErrRASvnUnknownCmd: 210001,
	}
	for code, want := range tests {
		if int32(code) != want {
			t.Errorf("%s = %d, want %d", code, code, want)
		}
	}
	if len(errorDefinitions) < 300 {
		t.Fatalf("generated error table has %d entries", len(errorDefinitions))
	}
}

func TestErrorMatchingByCode(t *testing.T) {
	root := NewError(ErrFSNotFound, "missing trunk")
	err := Wrap(ErrRASvnCmdErr, "server command failed", root)
	if !errors.Is(err, ErrFSNotFound) || !errors.Is(err, ErrRASvnCmdErr) {
		t.Fatalf("errors.Is did not traverse SVN code chain: %v", err)
	}
	if errors.Is(err, ErrWCLocked) {
		t.Fatal("errors.Is matched unrelated code")
	}
	if code, ok := ErrorCode(fmt.Errorf("context: %w", root)); !ok || code != ErrFSNotFound {
		t.Fatalf("ErrorCode() = %v, %v", code, ok)
	}
}

func TestTracePreservesCode(t *testing.T) {
	err := Trace(NewError(ErrFSNotFound, "specific message"))
	if !errors.Is(err, ErrFSNotFound) {
		t.Fatalf("Trace lost code: %v", err)
	}
	if got := err.Error(); got != ErrFSNotFound.DefaultMessage() {
		t.Fatalf("Trace error = %q, want default %q", got, ErrFSNotFound.DefaultMessage())
	}
}
