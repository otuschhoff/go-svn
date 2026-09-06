package repos

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLoadFixturesAllWritableFormats(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	fixtures, err := filepath.Glob(filepath.Join("..", "testdata", "repos", "*.dump"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("dump fixtures: %v, count %d", err, len(fixtures))
	}
	for _, format := range []int{6, 7, 8} {
		for _, fixture := range fixtures {
			format, fixture := format, fixture
			t.Run(filepath.Base(fixture)+"-format"+string(rune('0'+format)), func(t *testing.T) {
				repositoryPath := filepath.Join(t.TempDir(), "repository")
				repository, err := Create(context.Background(), repositoryPath, CreateOptions{Format: format})
				if err != nil {
					t.Fatal(err)
				}
				file, err := os.Open(fixture)
				if err != nil {
					t.Fatal(err)
				}
				loadErr := repository.Load(context.Background(), file, LoadOptions{UUID: UUIDForce})
				file.Close()
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if output, err := exec.Command(svnadmin, "verify", repositoryPath).CombinedOutput(); err != nil {
					t.Fatalf("svnadmin verify: %v\n%s", err, output)
				}
			})
		}
	}
}

func TestDumpLoadsWithSVNAdmin(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	sourcePath := filepath.Join(t.TempDir(), "source")
	source, err := Create(context.Background(), sourcePath, CreateOptions{Format: 8})
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.Open(filepath.Join("..", "testdata", "repos", "basic.dump"))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Load(context.Background(), fixture, LoadOptions{UUID: UUIDForce}); err != nil {
		fixture.Close()
		t.Fatal(err)
	}
	fixture.Close()
	revisionZero, err := source.RevisionProps(context.Background(), 0)
	if err != nil || string(revisionZero["svn:date"]) != "2020-01-01T00:00:00.000000Z\n" {
		t.Fatalf("revision zero properties = %v, error = %v", revisionZero, err)
	}
	var fulltextDump bytes.Buffer
	if err := source.Dump(context.Background(), &fulltextDump, DumpOptions{}); err != nil {
		t.Fatal(err)
	}
	referenceDump, err := exec.Command(svnadmin, "dump", "--quiet", sourcePath).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fulltextDump.Bytes(), referenceDump) {
		t.Fatalf("dump differs from svnadmin output\nGo dump:\n%s\nReference dump:\n%s", fulltextDump.Bytes(), referenceDump)
	}
	var rangeDump bytes.Buffer
	if err := source.Dump(context.Background(), &rangeDump, DumpOptions{StartRevision: 3, EndRevision: 3}); err != nil {
		t.Fatal(err)
	}
	rangeTargetPath := filepath.Join(t.TempDir(), "range-target")
	rangeTarget, err := Create(context.Background(), rangeTargetPath, CreateOptions{Format: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := rangeTarget.Load(context.Background(), bytes.NewReader(rangeDump.Bytes()), LoadOptions{}); err != nil {
		t.Fatalf("load non-incremental range: %v", err)
	}
	if output, err := exec.Command(svnadmin, "verify", rangeTargetPath).CombinedOutput(); err != nil {
		t.Fatalf("range svnadmin verify: %v\n%s", err, output)
	}
	for _, useDeltas := range []bool{false, true} {
		var dump bytes.Buffer
		if err := source.Dump(context.Background(), &dump, DumpOptions{UseDeltas: useDeltas}); err != nil {
			t.Fatal(err)
		}
		goTargetPath := filepath.Join(t.TempDir(), "go-target")
		goTarget, err := Create(context.Background(), goTargetPath, CreateOptions{Format: 8})
		if err != nil {
			t.Fatal(err)
		}
		if err := goTarget.Load(context.Background(), bytes.NewReader(dump.Bytes()), LoadOptions{UUID: UUIDForce}); err != nil {
			t.Fatalf("Go load (deltas=%t): %v", useDeltas, err)
		}
		if output, err := exec.Command(svnadmin, "verify", goTargetPath).CombinedOutput(); err != nil {
			t.Fatalf("Go-loaded svnadmin verify: %v\n%s", err, output)
		}
		targetPath := filepath.Join(t.TempDir(), "target")
		if output, err := exec.Command(svnadmin, "create", targetPath).CombinedOutput(); err != nil {
			t.Fatalf("svnadmin create: %v\n%s", err, output)
		}
		command := exec.Command(svnadmin, "load", "--force-uuid", targetPath)
		command.Stdin = bytes.NewReader(dump.Bytes())
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("svnadmin load (deltas=%t): %v\n%s\ndump:\n%s", useDeltas, err, output, dump.Bytes())
		}
		if output, err := exec.Command(svnadmin, "verify", targetPath).CombinedOutput(); err != nil {
			t.Fatalf("svnadmin verify: %v\n%s", err, output)
		}
	}
}
