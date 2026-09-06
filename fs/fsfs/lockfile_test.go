package fsfs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestWithWriteLockSerializes(t *testing.T) {
	name := filepath.Join(t.TempDir(), "write-lock")
	entered := make(chan struct{})
	release := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		if err := withWriteLock(context.Background(), name, func() error {
			close(entered)
			<-release
			return nil
		}); err != nil {
			t.Errorf("first lock: %v", err)
		}
	}()
	<-entered

	secondEntered := make(chan struct{})
	wait.Add(1)
	go func() {
		defer wait.Done()
		if err := withWriteLock(context.Background(), name, func() error {
			close(secondEntered)
			return nil
		}); err != nil {
			t.Errorf("second lock: %v", err)
		}
	}()
	select {
	case <-secondEntered:
		t.Fatal("second writer entered before the first released the lock")
	default:
	}
	close(release)
	wait.Wait()
	select {
	case <-secondEntered:
	default:
		t.Fatal("second writer never acquired the lock")
	}
}

func TestWithWriteLockRejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := withWriteLock(ctx, filepath.Join(t.TempDir(), "write-lock"), func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("error = %v, action called = %t", err, called)
	}
}
