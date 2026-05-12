// Package logging emits stable `event=value key=value …` lines per
// Symphony SPEC §13.1–§13.2. Required context fields:
//
//   - issue_id          — tracker-native ID (e.g. nSimonFR/nic-os#60 or
//                         a Linear UUID)
//   - issue_identifier  — human-readable (e.g. nSimonFR/nic-os#60,
//                         MT-649). Often the same as issue_id for github.
//   - session_id        — orchestrator session (`<kind>:<id>:<attempt>:
//                         <rand4>`).
//
// Values are space-quoted only when they contain spaces or quotes;
// otherwise emitted bare. This format is grep-friendly and parses
// with one-liner awk (`awk -F= '{ for (i=1; i<=NF; i++) ... }'`).
// JSON is deliberately avoided: nic-os has no log aggregator and the
// homepage widget queries the HTTP /api/v1/* surface, not journalctl.
//
// All log lines go to stderr so they appear in `journalctl -u gleaner`
// without competing with stdout (which carries `dispatch_ok: …`-style
// human-readable summaries from the orchestrator's main loop).
package logging

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Field is a single key=value pair.
type Field struct {
	Key   string
	Value any
}

// F is a short helper for inline field construction:
//
//	logging.Log("dispatch_ok", logging.F("issue_id", "X"), …)
func F(key string, value any) Field {
	return Field{Key: key, Value: value}
}

var (
	mu  sync.Mutex
	out io.Writer = os.Stderr
)

// SetOutput redirects log lines (used in tests).
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out = w
}

// Log writes `event=<event> key=value …` to the configured output.
// SPEC §13.1 expects `issue_id`/`issue_identifier`/`session_id` on
// issue-related events; the caller is responsible for supplying
// them. This package doesn't enforce them — too much false-positive
// noise on bootstrap/snapshot lines that don't have an issue.
func Log(event string, fields ...Field) {
	var b strings.Builder
	b.WriteString("event=")
	b.WriteString(quoteIfNeeded(event))
	for _, f := range fields {
		b.WriteByte(' ')
		b.WriteString(f.Key)
		b.WriteByte('=')
		b.WriteString(quoteIfNeeded(stringify(f.Value)))
	}
	b.WriteByte('\n')

	mu.Lock()
	defer mu.Unlock()
	_, _ = io.WriteString(out, b.String())
}

func stringify(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case error:
		if s == nil {
			return ""
		}
		return s.Error()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// quoteIfNeeded wraps in double quotes when the value contains
// whitespace, quotes, or '=' — the characters that would otherwise
// break naive key=value splitting on space.
func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\r\"=") {
		// Escape embedded quotes by backslash-quoting.
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
