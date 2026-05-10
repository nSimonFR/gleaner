// Package hook fork-execs a user-supplied script for events. The script
// gets the event name as $1 and the event JSON on stdin. Errors are logged
// (stderr) but never propagate — a broken hook must not block dispatch.
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
