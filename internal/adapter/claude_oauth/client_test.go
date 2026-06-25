package claude_oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCreds writes a minimal ~/.claude/.credentials.json with a valid
// (non-expired) token and returns its path.
func writeCreds(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	// expiresAt one hour in the future, unix millis.
	exp := time.Now().Add(time.Hour).UnixMilli()
	body := `{"claudeAiOauth":{"accessToken":"tok","subscriptionType":"team","expiresAt":` +
		itoa(exp) + `}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	return path
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func newServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
			t.Errorf("missing/incorrect anthropic-beta header: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("missing/incorrect Authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
}

// newResponseWithLimits is the current live shape: legacy windows plus the
// limits[] array. The session limit is active and in warning; weekly_all
// is active but normal at a lower percent; weekly_scoped is inactive.
const newResponseWithLimits = `{
  "five_hour":  {"utilization": 83.0, "resets_at": "2026-06-25T18:00:00Z"},
  "seven_day":  {"utilization": 44.0, "resets_at": "2026-06-30T00:00:00Z"},
  "seven_day_opus": null,
  "seven_day_sonnet": {"utilization": 0.0, "resets_at": null},
  "limits": [
    {"kind": "weekly_all",    "group": "all",    "percent": 44.0, "severity": "normal",  "is_active": true,  "resets_at": "2026-06-30T00:00:00Z", "scope": "all"},
    {"kind": "session",       "group": "session","percent": 83.0, "severity": "warning", "is_active": true,  "resets_at": "2026-06-25T18:00:00Z", "scope": "session"},
    {"kind": "weekly_scoped", "group": "opus",   "percent": 95.0, "severity": "warning", "is_active": false, "resets_at": "2026-06-30T00:00:00Z", "scope": "opus"}
  ],
  "extra_usage": {"is_enabled": false}
}`

// legacyResponse has no limits[] array (back-compat path).
const legacyResponse = `{
  "five_hour":  {"utilization": 12.0, "resets_at": "2026-06-25T18:00:00Z"},
  "seven_day":  {"utilization": 5.0,  "resets_at": "2026-06-30T00:00:00Z"},
  "extra_usage": {"is_enabled": true}
}`

func TestSnapshot_ParsesLimitsActiveGatingLimit(t *testing.T) {
	srv := newServer(t, newResponseWithLimits)
	defer srv.Close()

	a := &Adapter{
		CredentialsPath: writeCreds(t),
		Endpoint:        srv.URL,
		HTTPClient:      srv.Client(),
	}
	snap, err := a.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Legacy windows still parsed for back-compat.
	if w, ok := snap.Windows["short"]; !ok || w.UsedPercent != 0.83 {
		t.Errorf("short window = %+v, ok=%v; want 0.83", w, ok)
	}
	if w, ok := snap.Windows["long"]; !ok || w.UsedPercent != 0.44 {
		t.Errorf("long window = %+v, ok=%v; want 0.44", w, ok)
	}

	if snap.ActiveLimit == nil {
		t.Fatalf("ActiveLimit is nil; want the active warning session limit")
	}
	al := snap.ActiveLimit
	// Among active limits (weekly_all normal 44, session warning 83), the
	// warning one wins regardless of percent.
	if al.Kind != "session" {
		t.Errorf("ActiveLimit.Kind = %q; want session", al.Kind)
	}
	if al.UsedPercent != 0.83 {
		t.Errorf("ActiveLimit.UsedPercent = %v; want 0.83", al.UsedPercent)
	}
	if al.Severity != "warning" {
		t.Errorf("ActiveLimit.Severity = %q; want warning", al.Severity)
	}
	if !al.Capped {
		t.Errorf("ActiveLimit.Capped = false; want true (warning severity)")
	}
	if al.ResetsAt == nil {
		t.Fatalf("ActiveLimit.ResetsAt is nil; want parsed time")
	}
	want := time.Date(2026, 6, 25, 18, 0, 0, 0, time.UTC)
	if !al.ResetsAt.Equal(want) {
		t.Errorf("ActiveLimit.ResetsAt = %v; want %v", al.ResetsAt, want)
	}
}

func TestSnapshot_PrefersHighestPercentAmongSameSeverity(t *testing.T) {
	// Two active normal limits; no warning. Highest percent should win.
	payload := `{
      "limits": [
        {"kind": "session",    "percent": 30.0, "severity": "normal", "is_active": true,  "resets_at": "2026-06-25T18:00:00Z"},
        {"kind": "weekly_all", "percent": 70.0, "severity": "normal", "is_active": true,  "resets_at": "2026-06-30T00:00:00Z"},
        {"kind": "weekly_scoped", "percent": 99.0, "severity": "warning", "is_active": false}
      ]
    }`
	srv := newServer(t, payload)
	defer srv.Close()
	a := &Adapter{CredentialsPath: writeCreds(t), Endpoint: srv.URL, HTTPClient: srv.Client()}
	snap, err := a.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.ActiveLimit == nil {
		t.Fatalf("ActiveLimit is nil")
	}
	if snap.ActiveLimit.Kind != "weekly_all" {
		t.Errorf("Kind = %q; want weekly_all (highest active percent)", snap.ActiveLimit.Kind)
	}
	if snap.ActiveLimit.Capped {
		t.Errorf("Capped = true; want false (normal severity, <100%%)")
	}
}

func TestSnapshot_CappedAt100Percent(t *testing.T) {
	payload := `{
      "limits": [
        {"kind": "session", "percent": 100.0, "severity": "normal", "is_active": true, "resets_at": "2026-06-25T18:00:00Z"}
      ]
    }`
	srv := newServer(t, payload)
	defer srv.Close()
	a := &Adapter{CredentialsPath: writeCreds(t), Endpoint: srv.URL, HTTPClient: srv.Client()}
	snap, err := a.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.ActiveLimit == nil || !snap.ActiveLimit.Capped {
		t.Errorf("ActiveLimit Capped = %v; want true at 100%%", snap.ActiveLimit)
	}
}

func TestSnapshot_LegacyFallbackNoLimits(t *testing.T) {
	srv := newServer(t, legacyResponse)
	defer srv.Close()
	a := &Adapter{CredentialsPath: writeCreds(t), Endpoint: srv.URL, HTTPClient: srv.Client()}
	snap, err := a.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.ActiveLimit != nil {
		t.Errorf("ActiveLimit = %+v; want nil when limits[] absent", snap.ActiveLimit)
	}
	if w, ok := snap.Windows["short"]; !ok || w.UsedPercent != 0.12 {
		t.Errorf("short window = %+v, ok=%v; want 0.12 (legacy parse)", w, ok)
	}
	if !snap.ExtraUsageEnabled {
		t.Errorf("ExtraUsageEnabled = false; want true")
	}
}

func TestSnapshot_NoActiveLimit(t *testing.T) {
	// limits[] present but nothing active → ActiveLimit nil.
	payload := `{"limits": [{"kind": "session", "percent": 10.0, "severity": "normal", "is_active": false}]}`
	srv := newServer(t, payload)
	defer srv.Close()
	a := &Adapter{CredentialsPath: writeCreds(t), Endpoint: srv.URL, HTTPClient: srv.Client()}
	snap, err := a.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.ActiveLimit != nil {
		t.Errorf("ActiveLimit = %+v; want nil (no active entry)", snap.ActiveLimit)
	}
}
