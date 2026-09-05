package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteArchiveIsDeterministic(t *testing.T) {
	source := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(source, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, "db", "current")
	if err := os.WriteFile(file, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(t.TempDir(), "first.tar.gz")
	second := filepath.Join(t.TempDir(), "second.tar.gz")
	if err := writeArchive(source, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, normalizedTime.Add(time.Hour), normalizedTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := writeArchive(source, second); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("archives differ after source timestamp changed")
	}
}
