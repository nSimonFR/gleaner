package trigger

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
)

func TestEval_Basic(t *testing.T) {
	ctx := Context{
		"claude.long_pct":  30.0,
		"claude.short_pct": 80.0,
		"claude.ok":        true,
		"codex.long_pct":   10.0,
		"codex.short_pct":  5.0,
		"codex.ok":         true,
	}
	cases := []struct {
		expr string
		want bool
	}{
		{"claude.long_pct < 50", true},
		{"claude.long_pct < 10", false},
		{"claude.long_pct < 50 && codex.short_pct < 80", true},
		{"claude.long_pct < 10 || codex.short_pct < 80", true},
		{"claude.long_pct >= 30", true},
		{"claude.long_pct == 30", true},
		{"claude.long_pct != 30", false},
		{"true == true", true},
		{"claude.ok", true},
		{"claude.ok == false", false},
		{"-5 < 0", true},
		{"50.5 > 50.4 && 1 == 1", true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(tc.expr, ctx)
			if err != nil {
				t.Fatalf("Eval(%q): %v", tc.expr, err)
			}
			if got != tc.want {
				t.Fatalf("Eval(%q) = %v, want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestEval_Errors(t *testing.T) {
	ctx := Context{"claude.ok": true}
	cases := []string{
		"unknown.field < 1",
		"claude.ok < 1",  // bool < number
		"claude.ok < ",   // unexpected end
		"1 + 1",          // unsupported op
		"true && claude.ok < 1",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := Eval(c, ctx); err == nil {
				t.Fatalf("expected error for %q", c)
			}
		})
	}
}

func TestBuildContext_MissingProviderDefaultsToInf(t *testing.T) {
	c := BuildContext([]Source{
		{Provider: "codex", Snap: nil, Err: nil}, // missing data
	})
	if v := c["claude.long_pct"]; !math.IsInf(v.(float64), 1) {
		t.Fatalf("claude.long_pct should default to +Inf, got %v", v)
	}
	if v := c["claude.ok"]; v != false {
		t.Fatalf("claude.ok should default to false, got %v", v)
	}
	// `claude.long_pct < 50` must evaluate false when missing.
	got, err := Eval("claude.long_pct < 50", c)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("missing provider should not allow the trigger")
	}
}

func TestBuildContext_FromSnapshot(t *testing.T) {
	snap := &adapter.UsageSnapshot{
		Provider:          "claude",
		Windows:           map[string]adapter.Window{"short": {UsedPercent: 0.2}, "long": {UsedPercent: 0.4}},
		ExtraUsageEnabled: true,
	}
	c := BuildContext([]Source{{Provider: "claude", Snap: snap}})
	if v := c["claude.short_pct"]; v.(float64) != 20.0 {
		t.Fatalf("short_pct: %v", v)
	}
	if v := c["claude.long_pct"]; v.(float64) != 40.0 {
		t.Fatalf("long_pct: %v", v)
	}
	if v := c["claude.extra_usage"]; v != true {
		t.Fatalf("extra_usage: %v", v)
	}
	if v := c["claude.ok"]; v != true {
		t.Fatalf("ok: %v", v)
	}
}

func TestRun_AllowedExecsCommand(t *testing.T) {
	ctx := Context{"claude.long_pct": 10.0}
	r := Run(context.Background(), "ok", "claude.long_pct < 50",
		[]string{"sh", "-c", "echo hello"}, 5*time.Second, nil, ctx, false)
	if r.ParseErr != nil || r.ExecErr != nil {
		t.Fatalf("errs: parse=%v exec=%v", r.ParseErr, r.ExecErr)
	}
	if !r.Allowed {
		t.Fatal("should be allowed")
	}
	if !strings.Contains(r.Stdout, "hello") {
		t.Fatalf("stdout: %q", r.Stdout)
	}
}

func TestRun_DeniedDoesNotExec(t *testing.T) {
	ctx := Context{"claude.long_pct": 90.0}
	r := Run(context.Background(), "denied", "claude.long_pct < 50",
		[]string{"sh", "-c", "echo should-not-run"}, 5*time.Second, nil, ctx, false)
	if r.Allowed {
		t.Fatal("should be denied")
	}
	if r.Stdout != "" {
		t.Fatalf("stdout leaked: %q", r.Stdout)
	}
}

func TestRun_DryRunSkipsExec(t *testing.T) {
	ctx := Context{"x.v": 10.0}
	r := Run(context.Background(), "dr", "x.v < 50",
		[]string{"sh", "-c", "echo nope"}, 5*time.Second, nil, ctx, true)
	if !r.Allowed {
		t.Fatal("should be allowed in dry-run")
	}
	if r.Stdout != "" {
		t.Fatalf("dry-run executed: %q", r.Stdout)
	}
}

func TestRun_Timeout(t *testing.T) {
	ctx := Context{"x.v": 10.0}
	r := Run(context.Background(), "to", "x.v < 50",
		[]string{"sh", "-c", "sleep 5"}, 50*time.Millisecond, nil, ctx, false)
	if r.ExecErr == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRun_ParseErrorReported(t *testing.T) {
	ctx := Context{"x.v": 10.0}
	r := Run(context.Background(), "bad", "this is not an expression",
		[]string{"true"}, time.Second, nil, ctx, false)
	if r.ParseErr == nil {
		t.Fatal("expected parse error")
	}
	if r.Allowed {
		t.Fatal("parse error should imply not allowed")
	}
}
