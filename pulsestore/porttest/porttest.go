// Package porttest is the shared contract suite for go-pulse's storage ports.
//
// Every implementation runs the same tests: the in-memory fake, the SQL store
// on SQLite, the SQL store on PostgreSQL, and an application's own adapter
// over its existing tables. That is the point — an adapter that passes this
// suite can be swapped in with confidence, and a semantic the suite does not
// cover is a semantic the framework cannot rely on.
//
// Usage from an application that wrote its own adapter:
//
//	func TestMyAdapter(t *testing.T) {
//	    porttest.Run(t, func(t *testing.T) porttest.Stores {
//	        db := setupMyTestDatabase(t)
//	        return porttest.Stores{Agents: db.AgentPort(), ...}
//	    })
//	}
package porttest

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seanly/go-pulse/pulse"
	"github.com/seanly/go-pulse/pulsereg"
)

// Stores is one implementation's four ports, plus the clock it reads.
type Stores struct {
	Agents  pulse.AgentStore
	Tokens  pulse.TokenStore
	Configs pulse.ConfigStore
	Jobs    pulse.JobStore
	// Clock is the store's own notion of time, which tests both read and
	// advance. Reading matters as much as advancing: a test that builds an
	// expiry from the wall clock while the store runs on a fixed test clock
	// compares two unrelated timelines and passes or fails by accident.
	//
	// An implementation whose clock cannot be controlled leaves this nil, and
	// the time-dependent tests skip.
	Clock Clock
	// CreateAgent inserts an agent row directly, bypassing enrollment. Needed
	// because most tests want an agent without minting a token first.
	CreateAgent func(ctx context.Context, a *pulse.Agent) error
}

// Clock is a controllable clock: the suite reads it to build timestamps on the
// store's timeline and advances it to make deadlines pass.
type Clock interface {
	Now() time.Time
	Advance(time.Duration)
}

// Factory builds a fresh, empty set of stores for one test.
type Factory func(t *testing.T) Stores

// Run executes the whole contract suite against one implementation.
func Run(t *testing.T, newStores Factory) {
	t.Helper()
	tests := []struct {
		name string
		fn   func(*testing.T, Stores)
	}{
		{"AgentNotFound", testAgentNotFound},
		{"AgentRoundTrip", testAgentRoundTrip},
		{"EffectiveLabelsComputedOnWrite", testEffectiveLabels},
		{"LabelTombstones", testLabelTombstones},
		{"HeartbeatAndStaleness", testHeartbeatAndStaleness},
		{"HeartbeatDoesNotRevivesDisabled", testHeartbeatDisabled},
		{"TokenVersionRevocation", testTokenVersion},
		{"DeleteAgent", testDeleteAgent},
		{"DispatchableExcludesOfflineAndDisabled", testDispatchable},
		{"EnrollClaimsTokenOnce", testEnrollClaimsOnce},
		{"EnrollRejectsUnknownUsedExpiredIdentically", testEnrollRejects},
		{"EnrollIsAtomic", testEnrollAtomic},
		{"ConfigVersionAdvances", testConfigVersion},
		{"ConfigNilMeansNoOpinion", testConfigNilFields},
		{"ConfigNotFoundIsNormal", testConfigNotFound},
		{"JobRoundTrip", testJobRoundTrip},
		{"LeaseIsFIFOAndBounded", testLeaseFIFO},
		{"LeaseIsExclusive", testLeaseExclusive},
		{"LeaseOneConflictsWhenNotPending", testLeaseOneConflict},
		{"CompleteRequiresLeased", testCompleteRequiresLeased},
		{"FailRetriesThenTerminates", testFailRetries},
		{"RequeueDoesNotBurnAttempts", testRequeueAttempts},
		{"RequeueCeilingFailsTheJob", testRequeueCeiling},
		{"ReclaimRequeuesAndExpires", testReclaim},
		{"CancelIsTerminal", testCancel},
		{"CountsReflectStatus", testCounts},
		{"PurgeFinished", testPurge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, newStores(t))
		})
	}
}

// --- helpers ---

func ctx() context.Context { return context.Background() }

func mustAgent(t *testing.T, s Stores, a pulse.Agent) pulse.Agent {
	t.Helper()
	if a.ID == "" {
		a.ID = "agent-" + a.Name
	}
	if a.Status == "" {
		a.Status = pulse.AgentOffline
	}
	if err := s.CreateAgent(ctx(), &a); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	got, err := s.Agents.GetAgent(ctx(), a.ID)
	if err != nil {
		t.Fatalf("get agent after create: %v", err)
	}
	return got
}

// mustToken stores a token and returns its hash. expiresIn is relative to the
// STORE's clock, not the wall clock: those are different timelines whenever a
// test clock is in use.
func mustToken(t *testing.T, s Stores, labels map[string]string, expiresIn time.Duration) (hash string) {
	t.Helper()
	seq := nextSeq()
	hash = pulsereg.HashToken(fmt.Sprintf("plain-token-%d", seq))
	now := time.Now().UTC()
	if s.Clock != nil {
		now = s.Clock.Now()
	}
	var expires time.Time
	if expiresIn > 0 {
		expires = now.Add(expiresIn)
	}
	tok := pulse.BootstrapToken{
		ID:        fmt.Sprintf("tok-%d", seq),
		TokenHash: hash,
		Labels:    labels,
		ExpiresAt: expires,
		CreatedAt: now,
	}
	if err := s.Tokens.CreateBootstrapToken(ctx(), &tok); err != nil {
		t.Fatalf("create token: %v", err)
	}
	return hash
}

func enqueue(t *testing.T, s Stores, jobs ...*pulse.Job) {
	t.Helper()
	for _, j := range jobs {
		if j.Status == "" {
			j.Status = pulse.JobPending
		}
		if j.MaxAttempts == 0 {
			j.MaxAttempts = 3
		}
		if j.CreatedAt.IsZero() {
			// Distinct, increasing timestamps so FIFO order is well defined.
			j.CreatedAt = time.Now().UTC().Add(time.Duration(len(j.ID)) * time.Millisecond)
		}
	}
	if err := s.Jobs.Enqueue(ctx(), jobs); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

func requireClock(t *testing.T, s Stores) {
	t.Helper()
	if s.Clock == nil {
		t.Skip("implementation does not support a controllable clock")
	}
}

// seq gives tokens and jobs unique identifiers without reaching for the wall
// clock, which a test clock makes unreliable as a source of uniqueness.
var seqCounter atomic.Int64

func nextSeq() int64 { return seqCounter.Add(1) }

// --- agent tests ---

func testAgentNotFound(t *testing.T, s Stores) {
	// ErrNotFound must be distinguishable from a fault: the control plane
	// branches on it.
	if _, err := s.Agents.GetAgent(ctx(), "nope"); !errors.Is(err, pulse.ErrNotFound) {
		t.Errorf("GetAgent error = %v, want ErrNotFound", err)
	}
	if _, err := s.Agents.GetAgentByName(ctx(), "nope"); !errors.Is(err, pulse.ErrNotFound) {
		t.Errorf("GetAgentByName error = %v, want ErrNotFound", err)
	}
}

func testAgentRoundTrip(t *testing.T, s Stores) {
	want := pulse.Agent{
		ID:             "a1",
		Name:           "probe-1",
		ReportedLabels: map[string]string{"region": "east"},
		Capabilities:   []string{"tlsprobe", "http"},
		Version:        "1.2.3",
		Status:         pulse.AgentOffline,
		Description:    "rack 4",
		ObservedIP:     "198.51.100.5",
		TokenVersion:   1,
		LocalConfig: pulse.LocalConfig{
			Concurrency: 8, QPS: 20, Timeout: 30 * time.Second, LogLevel: "info",
			Extra: map[string]string{"allow_loopback": "false"},
		},
	}
	got := mustAgent(t, s, want)

	if got.Name != want.Name || got.Version != want.Version || got.Description != want.Description {
		t.Errorf("scalar fields did not round trip: %+v", got)
	}
	if got.ObservedIP != want.ObservedIP {
		t.Errorf("observed IP = %q, want %q", got.ObservedIP, want.ObservedIP)
	}
	if got.ReportedLabels["region"] != "east" {
		t.Errorf("reported labels = %v", got.ReportedLabels)
	}
	if len(got.Capabilities) != 2 {
		t.Errorf("capabilities = %v, want two entries", got.Capabilities)
	}
	if got.LocalConfig.Concurrency != 8 || got.LocalConfig.Timeout != 30*time.Second {
		t.Errorf("local config did not round trip: %+v", got.LocalConfig)
	}
	if got.LocalConfig.Extra["allow_loopback"] != "false" {
		t.Errorf("local config extra = %v", got.LocalConfig.Extra)
	}
	// Timestamps must come back as UTC, or comparisons across a restart start
	// behaving differently depending on the server's zone.
	if got.RegisteredAt.IsZero() {
		t.Error("registered_at not set")
	}
	if loc := got.RegisteredAt.Location(); loc != time.UTC {
		t.Errorf("registered_at location = %v, want UTC", loc)
	}

	byName, err := s.Agents.GetAgentByName(ctx(), "probe-1")
	if err != nil {
		t.Fatalf("GetAgentByName: %v", err)
	}
	if byName.ID != "a1" {
		t.Errorf("lookup by name gave %q", byName.ID)
	}
}

func testEffectiveLabels(t *testing.T, s Stores) {
	// Labels is derived and must be computed by the store, not trusted from
	// the caller: dispatch reads it on the hot path and it cannot be allowed
	// to drift from its inputs.
	a := mustAgent(t, s, pulse.Agent{
		ID: "a1", Name: "probe-1",
		ReportedLabels: map[string]string{"region": "east", "tier": "gold"},
		// A deliberately wrong derived value, as a caller might pass.
		Labels: map[string]string{"bogus": "value"},
	})
	if a.Labels["bogus"] != "" {
		t.Error("the store trusted a caller-supplied derived label set")
	}
	if a.Labels["region"] != "east" || a.Labels["tier"] != "gold" {
		t.Errorf("effective labels = %v, want them derived from reported", a.Labels)
	}
}

func testLabelTombstones(t *testing.T, s Stores) {
	mustAgent(t, s, pulse.Agent{
		ID: "a1", Name: "probe-1",
		ReportedLabels: map[string]string{"region": "east", "tier": "gold"},
	})

	west := "west"
	if err := s.Agents.SetLabelOverrides(ctx(), "a1", map[string]*string{
		"region": &west,
		"tier":   nil, // tombstone
	}); err != nil {
		t.Fatalf("SetLabelOverrides: %v", err)
	}

	a, err := s.Agents.GetAgent(ctx(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Labels["region"] != "west" {
		t.Errorf("region = %q, want the override to win", a.Labels["region"])
	}
	// A nil override is the only way to express "delete a label the agent
	// keeps reporting", so it must suppress rather than be ignored.
	if _, present := a.Labels["tier"]; present {
		t.Error("a tombstoned label is still in the effective set")
	}
	// The reported value is kept, so removing the override can fall back.
	if a.ReportedLabels["tier"] != "gold" {
		t.Errorf("reported labels lost the tombstoned key: %v", a.ReportedLabels)
	}
}

func testHeartbeatAndStaleness(t *testing.T, s Stores) {
	requireClock(t, s)
	mustAgent(t, s, pulse.Agent{ID: "a1", Name: "probe-1", Status: pulse.AgentOffline})

	if err := s.Agents.Heartbeat(ctx(), "a1", "2.0.0", "203.0.113.9"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	a, err := s.Agents.GetAgent(ctx(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	// A heartbeat brings a stale agent back without an operator doing anything.
	if a.Status != pulse.AgentOnline {
		t.Errorf("status = %q, want online after a heartbeat", a.Status)
	}
	if a.Version != "2.0.0" {
		t.Errorf("version = %q, want it updated by the heartbeat", a.Version)
	}
	if a.ObservedIP != "203.0.113.9" {
		t.Errorf("observed IP = %q", a.ObservedIP)
	}
	if a.LastHeartbeatAt.IsZero() {
		t.Error("heartbeat timestamp not recorded")
	}

	// Inside the window: nothing happens.
	s.Clock.Advance(pulse.StaleTimeout - time.Second)
	ids, err := s.Agents.MarkStaleAgentsOffline(ctx(), pulse.StaleTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("marked %v offline too early", ids)
	}

	// Past it: offline, and the ID is reported so the caller can log it.
	s.Clock.Advance(2 * time.Second)
	ids, err = s.Agents.MarkStaleAgentsOffline(ctx(), pulse.StaleTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "a1" {
		t.Errorf("stale IDs = %v, want [a1]", ids)
	}
	a, _ = s.Agents.GetAgent(ctx(), "a1")
	if a.Status != pulse.AgentOffline {
		t.Errorf("status = %q, want offline", a.Status)
	}
}

func testHeartbeatDisabled(t *testing.T, s Stores) {
	mustAgent(t, s, pulse.Agent{ID: "a1", Name: "probe-1", Status: pulse.AgentDisabled})
	if err := s.Agents.Heartbeat(ctx(), "a1", "", ""); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	a, err := s.Agents.GetAgent(ctx(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	// Disabled is an operator's decision. Liveness must not overturn it, or a
	// disabled agent would quietly come back by connecting.
	if a.Status != pulse.AgentDisabled {
		t.Errorf("status = %q, want it still disabled", a.Status)
	}
}

func testTokenVersion(t *testing.T, s Stores) {
	mustAgent(t, s, pulse.Agent{ID: "a1", Name: "probe-1", TokenVersion: 1})
	v, err := s.Agents.BumpTokenVersion(ctx(), "a1")
	if err != nil {
		t.Fatalf("BumpTokenVersion: %v", err)
	}
	if v != 2 {
		t.Errorf("new version = %d, want 2", v)
	}
	a, _ := s.Agents.GetAgent(ctx(), "a1")
	if a.TokenVersion != 2 {
		t.Errorf("stored version = %d, want 2", a.TokenVersion)
	}
}

func testDeleteAgent(t *testing.T, s Stores) {
	mustAgent(t, s, pulse.Agent{ID: "a1", Name: "probe-1"})
	if err := s.Agents.DeleteAgent(ctx(), "a1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	// The lookup failing is what makes a deleted agent's credential stop
	// verifying, so it has to be ErrNotFound specifically.
	if _, err := s.Agents.GetAgent(ctx(), "a1"); !errors.Is(err, pulse.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	if err := s.Agents.DeleteAgent(ctx(), "a1"); !errors.Is(err, pulse.ErrNotFound) {
		t.Errorf("second delete error = %v, want ErrNotFound", err)
	}
}

func testDispatchable(t *testing.T, s Stores) {
	mustAgent(t, s, pulse.Agent{ID: "a1", Name: "online-1", Status: pulse.AgentOnline})
	mustAgent(t, s, pulse.Agent{ID: "a2", Name: "offline-1", Status: pulse.AgentOffline})
	mustAgent(t, s, pulse.Agent{ID: "a3", Name: "disabled-1", Status: pulse.AgentDisabled})

	got, err := s.Agents.ListDispatchableAgents(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a1" {
		ids := make([]string, 0, len(got))
		for _, a := range got {
			ids = append(ids, a.ID)
		}
		t.Errorf("dispatchable = %v, want only the online agent", ids)
	}

	all, err := s.Agents.ListAgents(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("ListAgents returned %d, want 3", len(all))
	}
}

// --- token tests ---

func testEnrollClaimsOnce(t *testing.T, s Stores) {
	hash := mustToken(t, s, map[string]string{"fleet": "edge"}, 0)

	a := pulse.Agent{ID: "a1", Name: "probe-1", TokenVersion: 1,
		ReportedLabels: map[string]string{"region": "east"}}
	tok, err := s.Tokens.Enroll(ctx(), &a, hash)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if tok.Labels["fleet"] != "edge" {
		t.Errorf("claimed token labels = %v, want the preset", tok.Labels)
	}
	if tok.UsedBy != "a1" {
		t.Errorf("used_by = %q, want a1", tok.UsedBy)
	}
	if tok.UsedAt.IsZero() {
		t.Error("used_at not recorded")
	}

	// The token's presets must be PERSISTED, not merely returned. Returning
	// them while storing only what the agent reported is a bug that hides
	// itself: enrollment looks right, and every later read of the row is
	// missing the operator's labels — so dispatch selectors never match them.
	stored, err := s.Agents.GetAgent(ctx(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReportedLabels["fleet"] != "edge" {
		t.Errorf("stored reported labels = %v, want the token's preset merged in",
			stored.ReportedLabels)
	}
	if stored.Labels["fleet"] != "edge" {
		t.Errorf("stored effective labels = %v, want the token's preset", stored.Labels)
	}
	if stored.Labels["region"] != "east" {
		t.Errorf("stored effective labels = %v, want the agent's own label kept", stored.Labels)
	}
	// Enroll updates the agent in place, so a caller need not re-read the row
	// to learn what was stored.
	if a.Labels["fleet"] != "edge" {
		t.Errorf("Enroll left the caller's agent at %v, want the stored labels", a.Labels)
	}

	// Single use: the same token must not enroll a second agent.
	b := pulse.Agent{ID: "a2", Name: "probe-2", TokenVersion: 1}
	if _, err := s.Tokens.Enroll(ctx(), &b, hash); !errors.Is(err, pulse.ErrInvalidBootstrap) {
		t.Errorf("second enroll error = %v, want ErrInvalidBootstrap", err)
	}
}

func testEnrollRejects(t *testing.T, s Stores) {
	requireClock(t, s)
	used := mustToken(t, s, nil, 0)
	a := pulse.Agent{ID: "a1", Name: "probe-1", TokenVersion: 1}
	if _, err := s.Tokens.Enroll(ctx(), &a, used); err != nil {
		t.Fatal(err)
	}

	expired := mustToken(t, s, nil, time.Minute)
	s.Clock.Advance(2 * time.Minute)

	// All three must be the same error: an attacker probing tokens learns
	// nothing about which case they hit.
	for name, hash := range map[string]string{
		"unknown": pulsereg.HashToken("never-existed"),
		"used":    used,
		"expired": expired,
	} {
		b := pulse.Agent{ID: "a-" + name, Name: "probe-" + name, TokenVersion: 1}
		if _, err := s.Tokens.Enroll(ctx(), &b, hash); !errors.Is(err, pulse.ErrInvalidBootstrap) {
			t.Errorf("%s: error = %v, want ErrInvalidBootstrap", name, err)
		}
	}
}

func testEnrollAtomic(t *testing.T, s Stores) {
	a := pulse.Agent{ID: "a1", Name: "probe-1", TokenVersion: 1}
	if _, err := s.Tokens.Enroll(ctx(), &a, pulsereg.HashToken("bogus")); err == nil {
		t.Fatal("want an error for an unknown token")
	}
	// A failed claim must leave nothing behind: half an agent row would be
	// found and reused by the next registration.
	if _, err := s.Agents.GetAgent(ctx(), "a1"); !errors.Is(err, pulse.ErrNotFound) {
		t.Errorf("a failed enrollment left an agent row behind (err = %v)", err)
	}
}

// --- config tests ---

func testConfigVersion(t *testing.T, s Stores) {
	mustAgent(t, s, pulse.Agent{ID: "a1", Name: "probe-1"})
	four := 4
	cfg := pulse.AgentConfig{AgentID: "a1", Concurrency: &four, UpdatedBy: "op"}
	if err := s.Configs.SaveAgentConfig(ctx(), &cfg); err != nil {
		t.Fatalf("SaveAgentConfig: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("first version = %d, want 1", cfg.Version)
	}

	eight := 8
	cfg2 := pulse.AgentConfig{AgentID: "a1", Concurrency: &eight}
	if err := s.Configs.SaveAgentConfig(ctx(), &cfg2); err != nil {
		t.Fatal(err)
	}
	// The version must advance on every save: it is what makes a push
	// verifiable rather than hopeful.
	if cfg2.Version != 2 {
		t.Errorf("second version = %d, want 2", cfg2.Version)
	}

	got, err := s.Configs.GetAgentConfig(ctx(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Concurrency == nil || *got.Concurrency != 8 {
		t.Errorf("concurrency = %v, want 8", got.Concurrency)
	}

	all, err := s.Configs.ListAgentConfigs(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all["a1"].Version != 2 {
		t.Errorf("ListAgentConfigs = %+v", all)
	}
}

func testConfigNilFields(t *testing.T, s Stores) {
	mustAgent(t, s, pulse.Agent{ID: "a1", Name: "probe-1"})
	four := 4
	timeout := 15 * time.Second
	level := "debug"
	full := pulse.AgentConfig{
		AgentID: "a1", Concurrency: &four, Timeout: &timeout, LogLevel: &level,
		Payload: []byte(`{"app":{"k":"v"}}`),
	}
	if err := s.Configs.SaveAgentConfig(ctx(), &full); err != nil {
		t.Fatal(err)
	}
	got, err := s.Configs.GetAgentConfig(ctx(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Timeout == nil || *got.Timeout != 15*time.Second {
		t.Errorf("timeout = %v, want 15s", got.Timeout)
	}
	if got.LogLevel == nil || *got.LogLevel != "debug" {
		t.Errorf("log level = %v", got.LogLevel)
	}
	// QPS was never set: it must come back nil, not zero. The difference is
	// "no opinion" versus "zero", and the agent acts on them differently.
	if got.QPS != nil {
		t.Errorf("QPS = %v, want nil for an unset field", *got.QPS)
	}
	if string(got.Payload) != `{"app":{"k":"v"}}` {
		t.Errorf("payload = %q, want it carried opaquely", got.Payload)
	}

	// Clearing a field stores NULL again.
	cleared := pulse.AgentConfig{AgentID: "a1", Concurrency: &four}
	if err := s.Configs.SaveAgentConfig(ctx(), &cleared); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Configs.GetAgentConfig(ctx(), "a1")
	if got.LogLevel != nil {
		t.Errorf("log level = %v after being cleared, want nil", *got.LogLevel)
	}
}

func testConfigNotFound(t *testing.T, s Stores) {
	mustAgent(t, s, pulse.Agent{ID: "a1", Name: "probe-1"})
	// "No override" is a normal state — the agent's local config governs — so
	// the control plane must be able to recognise it precisely.
	if _, err := s.Configs.GetAgentConfig(ctx(), "a1"); !errors.Is(err, pulse.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	four := 4
	cfg := pulse.AgentConfig{AgentID: "a1", Concurrency: &four}
	if err := s.Configs.SaveAgentConfig(ctx(), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.Configs.DeleteAgentConfig(ctx(), "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Configs.GetAgentConfig(ctx(), "a1"); !errors.Is(err, pulse.ErrNotFound) {
		t.Errorf("error after delete = %v, want ErrNotFound", err)
	}
}
