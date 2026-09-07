package servers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"testing"
	"time"
)

const (
	defaultStartupTimeout = 10 * time.Second
	processWaitDelay      = time.Second
)

type process struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	done   chan struct{}
	output *lockedBuffer
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func startProcess(t testing.TB, executable string, args ...string) *process {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	output := &lockedBuffer{}
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout = output
	command.Stderr = output
	command.WaitDelay = processWaitDelay
	if err := command.Start(); err != nil {
		cancel()
		t.Fatalf("start %s: %v", executable, err)
	}
	serverProcess := &process{
		cancel: cancel,
		cmd:    command,
		done:   make(chan struct{}),
		output: output,
	}
	go func() {
		serverProcess.mu.Lock()
		serverProcess.err = command.Wait()
		serverProcess.mu.Unlock()
		close(serverProcess.done)
	}()
	t.Cleanup(serverProcess.stop)
	return serverProcess
}

func (p *process) stop() {
	p.once.Do(func() {
		p.cancel()
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			<-p.done
		}
	})
}

func (p *process) waitForTCP(address string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultStartupTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-p.done:
			p.mu.Lock()
			processErr := p.err
			p.mu.Unlock()
			if processErr == nil {
				processErr = errors.New("process exited without an error")
			}
			return fmt.Errorf("process exited before listening: %w; output: %s", processErr, p.output.String())
		case <-ctx.Done():
			return fmt.Errorf("wait for %s: %w; output: %s", address, ctx.Err(), p.output.String())
		case <-ticker.C:
		}
	}
}

func reserveAddress(host string) (string, string, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return "", "", err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", "", err
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", "", err
	}
	return address, port, nil
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
