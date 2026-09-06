package wc

import "testing"

func TestResolveConflictHunks(t *testing.T) {
	contents := []byte("unchanged\n<<<<<<< .mine\nmine\n||||||| .r1\nbase\n=======\ntheirs\n>>>>>>> .r2\nmerged\n")
	if got := string(ResolveConflictHunks(contents, true)); got != "unchanged\nmine\nmerged\n" {
		t.Fatalf("mine-conflict = %q", got)
	}
	if got := string(ResolveConflictHunks(contents, false)); got != "unchanged\ntheirs\nmerged\n" {
		t.Fatalf("theirs-conflict = %q", got)
	}
}
