package fsfs

import (
	"context"
	"os"
	"path/filepath"
)

func withWriteLock(ctx context.Context, name string, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := lockFile(file); err != nil {
		return err
	}
	defer unlockFile(file)
	if err := ctx.Err(); err != nil {
		return err
	}
	return action()
}
