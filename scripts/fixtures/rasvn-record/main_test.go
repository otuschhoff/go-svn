package main

import (
	"bytes"
	"testing"
)

func TestNormalizeClientIdentity(t *testing.T) {
	input := []byte("( 35:SVN/1.14.5 (arm-apple-darwin25.6.0) )")
	got := normalizeClientIdentity(input)
	want := []byte("( 20:SVN/fixture (go-svn) )")
	if !bytes.Equal(got, want) {
		t.Fatalf("normalizeClientIdentity() = %q, want %q", got, want)
	}
}

func TestNormalizeClientIdentityMalformed(t *testing.T) {
	input := []byte("missing-length:SVN/client")
	if got := normalizeClientIdentity(input); !bytes.Equal(got, input) {
		t.Fatalf("malformed input changed: %q", got)
	}
}
