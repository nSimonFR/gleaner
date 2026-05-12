package logging

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := out
	SetOutput(buf)
	t.Cleanup(func() { SetOutput(prev) })
	return buf
}

func TestLog_Basic(t *testing.T) {
	buf := capture(t)
	Log("dispatch_ok",
		F("issue_id", "X"),
		F("issue_identifier", "X"),
		F("session_id", "github:X:1:abcd"),
		F("duration_ms", 1234),
	)
	got := buf.String()
	for _, want := range []string{
		"event=dispatch_ok",
		"issue_id=X",
		"session_id=github:X:1:abcd",
		"duration_ms=1234",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\ngot: %s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("line should end with newline; got: %q", got)
	}
}

func TestLog_QuoteWhenNeeded(t *testing.T) {
	buf := capture(t)
	Log("skip",
		F("reason", "short_window_ceiling_hit (claude short=78% > 75%)"),
	)
	got := buf.String()
	// Reason contains spaces, "=", and parens — must be quoted.
	if !strings.Contains(got, `reason="short_window_ceiling_hit (claude short=78% > 75%)"`) {
		t.Errorf("expected quoted reason; got: %s", got)
	}
}

func TestLog_ErrorValue(t *testing.T) {
	buf := capture(t)
	Log("dispatch_failed", F("err", errors.New("git push: bad")))
	got := buf.String()
	if !strings.Contains(got, `err="git push: bad"`) {
		t.Errorf("expected error message stringified + quoted; got: %s", got)
	}
}

func TestLog_BareKey(t *testing.T) {
	buf := capture(t)
	Log("tick_ok", F("ok", true))
	if !strings.Contains(buf.String(), "ok=true") {
		t.Errorf("expected bare ok=true; got: %s", buf.String())
	}
}

func TestLog_EmptyString(t *testing.T) {
	buf := capture(t)
	Log("e", F("k", ""))
	if !strings.Contains(buf.String(), `k=""`) {
		t.Errorf("expected k=\"\" for empty string; got: %s", buf.String())
	}
}

func TestLog_NoEqualInBareValue(t *testing.T) {
	buf := capture(t)
	Log("e", F("k", "a=b"))
	// `=` would break naive splitting — must be quoted.
	if !strings.Contains(buf.String(), `k="a=b"`) {
		t.Errorf("expected k=\"a=b\"; got: %s", buf.String())
	}
}
