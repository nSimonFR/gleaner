package picker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
)

type fakeTracker struct {
	issues   []tracker.Issue
	assigned []assignCall
	listErr  error
	assignErr error
}

type assignCall struct{ id, user string }

func (f *fakeTracker) Kind() string                                        { return "linear" }
func (f *fakeTracker) EnforceAuth(ctx context.Context) error               { return nil }
func (f *fakeTracker) ListActive(ctx context.Context) ([]tracker.Issue, error) {
	return f.issues, f.listErr
}
func (f *fakeTracker) Assign(ctx context.Context, id, user string) error {
	if f.assignErr != nil {
		return f.assignErr
	}
	f.assigned = append(f.assigned, assignCall{id, user})
	return nil
}

type fakeQuota struct {
	provider string
	snap     adapter.UsageSnapshot
	err      error
}

func (f *fakeQuota) Provider() string { return f.provider }
func (f *fakeQuota) Snapshot(ctx context.Context) (*adapter.UsageSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	s := f.snap
	return &s, nil
}

func minimalCfg(t *testing.T) *config.Config {
	t.Helper()
	c := config.Defaults()
	c.Tracker.APIKeyFile = "/dev/null"
	c.Tracker.TeamKey = "NSI"
	c.Tracker.CyrusUserID = "user_cyrus"
	c.Safety.KillSwitch = ""    // no kill file in tests
	c.Hours.Active = ""          // unrestricted
	c.Hours.Drain = ""           // unrestricted
	return &c
}

func okQuota() *fakeQuota {
	return &fakeQuota{
		provider: "claude",
		snap: adapter.UsageSnapshot{
			Plan: "team",
			Windows: map[string]adapter.Window{
				"short": {UsedPercent: 0.1},
				"long":  {UsedPercent: 0.1},
			},
		},
	}
}

func TestTick_PicksHighestPriorityUnassigned(t *testing.T) {
	tr := &fakeTracker{issues: []tracker.Issue{
		{ID: "i_low", Identifier: "NSI-3", State: "Todo", Priority: 4, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "i_urgent", Identifier: "NSI-1", State: "Todo", Priority: 1, CreatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: "i_medium", Identifier: "NSI-2", State: "Todo", Priority: 3, CreatedAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
	}}
	out, err := Tick(context.Background(), Inputs{
		Cfg: minimalCfg(t), Tracker: tr, QuotaSources: []adapter.QuotaSource{okQuota()},
	})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.Picked == nil || out.Picked.ID != "i_urgent" {
		t.Fatalf("Picked = %+v; want NSI-1 (urgent)", out.Picked)
	}
	if len(tr.assigned) != 1 || tr.assigned[0] != (assignCall{"i_urgent", "user_cyrus"}) {
		t.Errorf("assigned calls = %+v", tr.assigned)
	}
}

func TestTick_TreatsPriorityZeroAsLowest(t *testing.T) {
	tr := &fakeTracker{issues: []tracker.Issue{
		{ID: "i_zero", Identifier: "NSI-1", State: "Todo", Priority: 0, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "i_low", Identifier: "NSI-2", State: "Todo", Priority: 4, CreatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
	}}
	out, err := Tick(context.Background(), Inputs{
		Cfg: minimalCfg(t), Tracker: tr, QuotaSources: []adapter.QuotaSource{okQuota()},
	})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.Picked == nil || out.Picked.ID != "i_low" {
		t.Fatalf("Picked = %+v; want NSI-2 (priority 4, beats 0)", out.Picked)
	}
}

func TestTick_SkipsBlockedAndHumanAssigned(t *testing.T) {
	tr := &fakeTracker{issues: []tracker.Issue{
		{ID: "i_blocked", Identifier: "NSI-1", State: "Todo", Priority: 1, BlockedBy: []string{"x"}},
		{ID: "i_human", Identifier: "NSI-2", State: "Todo", Priority: 1, AssigneeID: "user_human"},
		{ID: "i_ok", Identifier: "NSI-3", State: "Todo", Priority: 2},
	}}
	out, err := Tick(context.Background(), Inputs{
		Cfg: minimalCfg(t), Tracker: tr, QuotaSources: []adapter.QuotaSource{okQuota()},
	})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.Picked == nil || out.Picked.ID != "i_ok" {
		t.Fatalf("Picked = %+v; want NSI-3", out.Picked)
	}
}

func TestTick_NoOpWhenAlreadyAssignedToCyrus(t *testing.T) {
	tr := &fakeTracker{issues: []tracker.Issue{
		{ID: "i1", Identifier: "NSI-1", State: "Todo", Priority: 1, AssigneeID: "user_cyrus"},
	}}
	out, err := Tick(context.Background(), Inputs{
		Cfg: minimalCfg(t), Tracker: tr, QuotaSources: []adapter.QuotaSource{okQuota()},
	})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !out.AlreadyAssigned || out.Picked == nil {
		t.Fatalf("want AlreadyAssigned with Picked set; got %+v", out)
	}
	if len(tr.assigned) != 0 {
		t.Errorf("no Assign call expected; got %v", tr.assigned)
	}
}

func TestTick_QuotaBlocked(t *testing.T) {
	tr := &fakeTracker{issues: []tracker.Issue{
		{ID: "i1", Identifier: "NSI-1", State: "Todo", Priority: 1},
	}}
	highQuota := okQuota()
	highQuota.snap.Windows["short"] = adapter.Window{UsedPercent: 0.95}
	out, err := Tick(context.Background(), Inputs{
		Cfg: minimalCfg(t), Tracker: tr, QuotaSources: []adapter.QuotaSource{highQuota},
	})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.Picked != nil {
		t.Errorf("expected no pick, got %+v", out.Picked)
	}
	if out.Skipped == "" {
		t.Errorf("expected Skipped reason; got empty")
	}
	if len(tr.assigned) != 0 {
		t.Errorf("no Assign call expected on quota block; got %v", tr.assigned)
	}
}

func TestTick_DryRunSkipsAssign(t *testing.T) {
	tr := &fakeTracker{issues: []tracker.Issue{
		{ID: "i1", Identifier: "NSI-1", State: "Todo", Priority: 1},
	}}
	out, err := Tick(context.Background(), Inputs{
		Cfg: minimalCfg(t), Tracker: tr, QuotaSources: []adapter.QuotaSource{okQuota()}, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.Picked == nil || out.Picked.ID != "i1" {
		t.Fatalf("Picked = %+v; want i1", out.Picked)
	}
	if len(tr.assigned) != 0 {
		t.Errorf("dry-run must not Assign; got %v", tr.assigned)
	}
}

func TestTick_KillSwitchDenies(t *testing.T) {
	tr := &fakeTracker{issues: []tracker.Issue{{ID: "i1", Identifier: "NSI-1", State: "Todo", Priority: 1}}}
	cfg := minimalCfg(t)
	cfg.Safety.KillSwitch = "/etc/hostname" // a file that exists
	out, err := Tick(context.Background(), Inputs{
		Cfg: cfg, Tracker: tr, QuotaSources: []adapter.QuotaSource{okQuota()},
	})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.Picked != nil {
		t.Errorf("kill switch should block; got pick %+v", out.Picked)
	}
	if out.Skipped == "" {
		t.Errorf("expected Skipped reason")
	}
}

func TestTick_ListError(t *testing.T) {
	tr := &fakeTracker{listErr: errors.New("boom")}
	_, err := Tick(context.Background(), Inputs{
		Cfg: minimalCfg(t), Tracker: tr, QuotaSources: []adapter.QuotaSource{okQuota()},
	})
	if err == nil {
		t.Fatal("expected error from ListActive failure")
	}
}
