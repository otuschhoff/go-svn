package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var normalizedTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func main() {
	var source string
	var output string
	flag.StringVar(&source, "source", "", "repository directory to archive")
	flag.StringVar(&output, "output", "", "output .tar.gz path")
	flag.Parse()
	if source == "" || output == "" {
		fatalf("-source and -output are required")
	}
	if err := writeArchive(source, output); err != nil {
		fatalf("write archive: %v", err)
	}
}

func writeArchive(source, output string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".archive-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	gzipWriter, err := gzip.NewWriterLevel(temporary, gzip.BestCompression)
	if err != nil {
		_ = temporary.Close()
		return err
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)

	walkErr := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(filepath.Dir(source), path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if entry.IsDir() && !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""
		header.ModTime = normalizedTime
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.PAXRecords = nil
		header.Format = tar.FormatPAX
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = temporary.Close()
		return walkErr
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		_ = temporary.Close()
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, output)
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", values...)
	os.Exit(1)
}
