// Package hook implements two distinct shapes of operator-supplied hooks:
//
//   - Event hooks (Fire) — v0.0.3 vintage, async, fire-and-forget. The
//     script gets an event name as $1 and a JSON payload on stdin.
//     Failures are logged and discarded; a broken event hook must not
//     block dispatch (`pr_opened`, `dispatch_failed`, …).
//
//   - Lifecycle hooks (RunLifecycle) — Milestone B, sync, blocking.
//     Modeled on Symphony SPEC §5.3.4 / §9.4. Fires at four points in
//     the workspace lifecycle: after_create, before_run, after_run,
//     before_remove. Failure semantics differ per hook: see RunLifecycle
//     doc. Execution is `bash -lc <script>` with cwd = workspace and the
//     same GLEANER_* env the executor already exports.
//
// The two shapes coexist under distinct YAML keys (`hook:` singular for
// events, `hooks:` plural for lifecycle). Existing v0.0.3 deployments
// keep working unchanged.
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Fire shells out to `script` with EVENT as $1 and JSON-encoded payload on
// stdin. Best-effort: any failure is logged to stderr and discarded. Skips
// silently if script == "".
func Fire(script, event string, payload any) {
	if script == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	data, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hook %s: marshal: %v\n", event, err)
		return
	}
	cmd := exec.CommandContext(ctx, script, event)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "hook %s (%s) failed: %v %s\n", event, script, err, stderr.String())
	}
}

// RunLifecycle runs a lifecycle hook script synchronously with cwd
// = workspace, the supplied env (appended to os.Environ), and a hard
// timeout. The script string is treated as a shell command, executed
// via `bash -lc <script>` so operators can write `/etc/.../foo.sh` OR
// inline shell — both work. Empty `script` is a no-op (returns nil).
//
// SPEC §9.2 / §9.4: cwd MUST be the workspace; stderr is captured and
// surfaced in the returned error on non-zero exit. The default timeout
// when timeout <= 0 is 60 seconds (SPEC §5.3.4 hooks.timeout_ms default).
//
// Failure semantics live at the call site, not here — Run returns the
// exit error as-is. The executor decides which hooks are "fatal" and
// which are "logged and ignored":
//
//   - after_create non-zero  → fatal to dispatch
//   - before_run  non-zero  → skip dispatch (denial, not failure)
//   - after_run   non-zero  → log and ignore
//   - before_remove non-zero → log and ignore
//
// `name` is included in error messages for operator clarity.
func RunLifecycle(ctx context.Context, name, script, cwd string, env []string, timeout time.Duration) error {
	if script == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-lc", script)
	cmd.Dir = cwd
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("hook %s: timeout after %s (stderr: %s)", name, timeout, trim(stderr.String()))
	}
	if err != nil {
		return fmt.Errorf("hook %s: %w (stderr: %s)", name, err, trim(stderr.String()))
	}
	return nil
}

func trim(s string) string {
	const max = 512
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
