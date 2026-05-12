package executor

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
)

// ErrStalled is returned by runInWorkspace when the child process
// produces no stdout/stderr for `stall_timeout`. SPEC §5.3.6.
var ErrStalled = errors.New("agent stalled — no output within stall_timeout")

// stallWriter wraps a bytes.Buffer and records the wall-clock time of
// the most recent Write. A watcher goroutine polls lastWrite() and
// kills the child process if it's been silent too long.
type stallWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	lastWrite time.Time
}

func newStallWriter() *stallWriter {
	return &stallWriter{lastWrite: time.Now()}
}

func (w *stallWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastWrite = time.Now()
	return w.buf.Write(p)
}

func (w *stallWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *stallWriter) LastWrite() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastWrite
}

// watchStall runs in its own goroutine. It polls every interval, and
// if the child has been silent for longer than `timeout`, calls
// cmd.Process.Kill() and signals the caller via the returned channel.
// The watcher exits when ctx is cancelled (typically by the caller
// when cmd.Wait returns).
//
// timeout <= 0 disables stall detection (returns immediately).
func watchStall(ctx context.Context, cmd *exec.Cmd, w *stallWriter, timeout time.Duration) <-chan struct{} {
	stalled := make(chan struct{}, 1)
	if timeout <= 0 {
		close(stalled)
		return stalled
	}
	// Poll at min(timeout/4, 5s) — granular enough to fire close to
	// the deadline, coarse enough not to burn CPU.
	interval := timeout / 4
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if cmd.Process == nil {
					continue
				}
				if time.Since(w.LastWrite()) > timeout {
					_ = cmd.Process.Kill()
					select {
					case stalled <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}()
	return stalled
}
