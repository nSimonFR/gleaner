package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// ErrStalled is returned by runInWorkspace when the child process
// produces no stdout/stderr for `stall_timeout`. SPEC §5.3.6.
//
// Use errors.Is(err, ErrStalled) for the sentinel match; the concrete
// returned error is wrapped with the elapsed-silent duration via
// stalledError so operators can see how long the child went quiet
// (matches Symphony Elixir's `"stalled for Xms"` diagnostic).
var ErrStalled = errors.New("agent stalled — no output within stall_timeout")

type stalledError struct {
	silentFor time.Duration
}

func (e *stalledError) Error() string {
	return fmt.Sprintf("agent stalled — no output for %s", e.silentFor.Round(time.Millisecond))
}
func (e *stalledError) Unwrap() error { return ErrStalled }
func (e *stalledError) Is(target error) bool {
	return target == ErrStalled
}

// newStalledError wraps the sentinel with the silence duration for
// improved diagnostics.
func newStalledError(silentFor time.Duration) error {
	return &stalledError{silentFor: silentFor}
}

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
// cmd.Process.Kill() and signals the caller via the returned channel
// with the observed silence duration. The watcher exits when ctx is
// cancelled (typically by the caller when cmd.Wait returns) or when
// it has fired a stall.
//
// The channel is closed when the watcher exits — callers can do a
// blocking receive after stopping the watcher to distinguish
// "watcher exited without firing" (closed → zero-value, ok=false)
// from "watcher fired a stall" (buffered value present → ok=true,
// duration carries the silence). This eliminates the race where the
// main goroutine reads the channel before the watcher's send lands.
//
// timeout <= 0 disables stall detection (returns an already-closed
// channel).
func watchStall(ctx context.Context, cmd *exec.Cmd, w *stallWriter, timeout time.Duration) <-chan time.Duration {
	stalled := make(chan time.Duration, 1)
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
		defer close(stalled)
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
				silent := time.Since(w.LastWrite())
				if silent > timeout {
					_ = cmd.Process.Kill()
					stalled <- silent
					return
				}
			}
		}
	}()
	return stalled
}
