// Package claude_oauth implements QuotaSource via Anthropic's
// /api/oauth/usage endpoint. This is zero-token-cost — the endpoint is
// metadata only and does not draw down quota.
//
// Reads the OAuth access token from ~/.claude/.credentials.json
// (claudeAiOauth.accessToken). Refresh handling is out of scope for v0.0.1;
// if the token has expired, this adapter returns an error and the snapshot
// command shows the error inline so the user knows to `claude login` again.
//
// Response shape (sampled from a live Team plan):
//
//	{
//	  "five_hour":          {"utilization": 83.0, "resets_at": "..."},
//	  "seven_day":          {"utilization": 44.0, "resets_at": "..."},
//	  "seven_day_opus":     null,
//	  "seven_day_sonnet":   {"utilization": 0.0,  "resets_at": null},
//	  "seven_day_omelette": {"utilization": 0.0,  "resets_at": null},
//	  ...
//	  "extra_usage":        {"is_enabled": false, ...}
//	}
//
// utilization is 0-100; we normalize to 0-1 to match codex.
package claude_oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
)

type Adapter struct {
	CredentialsPath string // default: ~/.claude/.credentials.json
	Endpoint        string // default: https://api.anthropic.com/api/oauth/usage
	HTTPClient      *http.Client
}

func (a *Adapter) Provider() string { return "claude" }

func (a *Adapter) Snapshot(ctx context.Context) (*adapter.UsageSnapshot, error) {
	credsPath := a.CredentialsPath
	if credsPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("UserHomeDir: %w", err)
		}
		credsPath = filepath.Join(home, ".claude", ".credentials.json")
	}
	endpoint := a.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com/api/oauth/usage"
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}

	token, subscriptionType, err := readToken(credsPath)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "gleaner/0.0.1")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("usage endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("usage endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	snap := &adapter.UsageSnapshot{
		Provider:   "claude",
		Plan:       subscriptionType,
		AsOf:       time.Now(),
		Windows:    map[string]adapter.Window{},
		SubBuckets: map[string]adapter.Window{},
		SourceNote: "/api/oauth/usage",
	}

	// Top-level windows
	if w, ok := decodeWindow(raw["five_hour"], 300); ok {
		snap.Windows["short"] = w
	}
	if w, ok := decodeWindow(raw["seven_day"], 10080); ok {
		snap.Windows["long"] = w
	}

	// Sub-buckets — capture whichever ones the API returned non-null.
	// Keys with the seven_day_ prefix are model-specific buckets on
	// Team-class plans; null on plans that don't expose them.
	for k, v := range raw {
		if len(k) <= len("seven_day_") || k[:len("seven_day_")] != "seven_day_" {
			continue
		}
		if w, ok := decodeWindow(v, 10080); ok {
			label := k[len("seven_day_"):] // e.g. "opus", "sonnet", "omelette"
			snap.SubBuckets[label] = w
		}
	}

	// extra_usage
	if rawExtra, ok := raw["extra_usage"]; ok && len(rawExtra) > 0 {
		var ex struct {
			IsEnabled bool `json:"is_enabled"`
		}
		if json.Unmarshal(rawExtra, &ex) == nil {
			snap.ExtraUsageEnabled = ex.IsEnabled
		}
	}

	return snap, nil
}

func readToken(path string) (token, subscriptionType string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken      string `json:"accessToken"`
			SubscriptionType string `json:"subscriptionType"`
			ExpiresAt        int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.NewDecoder(f).Decode(&creds); err != nil {
		return "", "", fmt.Errorf("decode %s: %w", path, err)
	}
	if creds.ClaudeAiOauth.AccessToken == "" {
		return "", "", fmt.Errorf("no accessToken in %s", path)
	}
	// expiresAt is unix milliseconds in Claude's format
	if creds.ClaudeAiOauth.ExpiresAt > 0 {
		exp := time.UnixMilli(creds.ClaudeAiOauth.ExpiresAt)
		if time.Now().After(exp) {
			return "", "", fmt.Errorf("OAuth token expired at %s — run `claude login`", exp.Format(time.RFC3339))
		}
	}
	subscriptionType = creds.ClaudeAiOauth.SubscriptionType
	if subscriptionType == "" {
		subscriptionType = "unknown"
	}
	return creds.ClaudeAiOauth.AccessToken, subscriptionType, nil
}

func decodeWindow(rm json.RawMessage, defaultMinutes int) (adapter.Window, bool) {
	if len(rm) == 0 || string(rm) == "null" {
		return adapter.Window{}, false
	}
	var v struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    *string  `json:"resets_at"`
	}
	if err := json.Unmarshal(rm, &v); err != nil {
		return adapter.Window{}, false
	}
	if v.Utilization == nil {
		return adapter.Window{}, false
	}
	w := adapter.Window{
		UsedPercent: *v.Utilization / 100.0,
		Minutes:     defaultMinutes,
	}
	if v.ResetsAt != nil && *v.ResetsAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, *v.ResetsAt); err == nil {
			w.ResetsAt = &t
		} else if t, err := time.Parse(time.RFC3339, *v.ResetsAt); err == nil {
			w.ResetsAt = &t
		}
	}
	return w, true
}
