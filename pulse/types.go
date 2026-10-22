package pulse

import "time"

// Job is one unit of work. Payload is opaque: the framework moves it between
// the queue and an Executor without reading it.
type Job struct {
	ID string
	// GroupID is the batch this job belongs to, for the application's own
	// accounting. Empty for standalone work.
	GroupID string
	// SubjectID names what the job acts on, so results correlate without
	// decoding Payload.
	SubjectID string
	// Kind selects the executor. Empty is a valid kind.
	Kind string
	// AgentID is who the job is for. The application decides this at enqueue
	// time; the framework never reassigns it (requeueing leaves it in place,
	// so an application that wants re-selection must do that itself).
	AgentID string
	Status  string
	// Timeout bounds the whole job. Zero means the dispatcher's default.
	Timeout time.Duration
	// Attempt counts the times an agent has held this job with a chance to
	// work on it. A lease increments it; a requeue gives it back, because a
	// requeue means no work was possible (JobStore.Requeue).
	//
	// An EXPIRED lease does not give it back, and that asymmetry is the whole
	// point: a requeue says nothing happened, while an expiry says an agent
	// took the job and never answered — it may have done the work and died
	// before reporting. Charging the second and not the first is what keeps
	// MaxAttempts a budget for real attempts rather than one a busy fleet can
	// drain on its own.
	Attempt     int
	MaxAttempts int
	// RequeueCount is how often this job was handed back without running — a
	// full agent, not failed work. It never enters the MaxAttempts budget; it
	// exists only so a permanently saturated agent cannot spin a job forever.
	RequeueCount int
	LastError    string
	Payload      []byte

	LeaseExpiresAt time.Time
	CreatedAt      time.Time
	FinishedAt     time.Time
}

// Result is one finished job as reported by an agent.
type Result struct {
	JobID     string
	GroupID   string
	SubjectID string
	Kind      string
	// Status is one of the Status* constants.
	Status      string
	ErrorCode   string
	ErrorDetail string
	// ExecutedAt is when the job ran on the agent, not when the report
	// arrived; a reconnect can deliver results much later.
	ExecutedAt time.Time
	Duration   time.Duration
	Payload    []byte
}

// Agent is one enrolled agent.
type Agent struct {
	ID   string
	Name string
	// Labels is the effective set every selector reads: ReportedLabels with
	// LabelOverrides applied. Derived, computed on write — label matching sits
	// in the dispatch hot path, so it is not recomputed per read.
	Labels map[string]string
	// ReportedLabels is what the agent claimed at its last registration, kept
	// so a console can say who decided each value and so removing an override
	// can fall back to it.
	ReportedLabels map[string]string
	// LabelOverrides is the server's opinion. A nil value is a tombstone: the
	// key is removed from the effective set and stays gone across
	// registrations, which is the only way to express "delete a label the
	// agent keeps reporting".
	LabelOverrides map[string]*string
	// Capabilities are the job kinds this agent declared it can execute.
	// Stored and queryable; dispatch does not filter on them.
	Capabilities []string
	Version      string
	Status       string
	Description  string
	// ObservedIP is the address the connection came from — the only source of
	// peer address the framework trusts.
	ObservedIP string
	// TokenVersion is the revocation counter. Bumping it invalidates every
	// credential issued for this agent.
	TokenVersion int
	// LocalConfig is what the agent's own configuration says, reported at
	// registration. Display only: the server never acts on it.
	LocalConfig LocalConfig

	LastHeartbeatAt time.Time
	RegisteredAt    time.Time
	UpdatedAt       time.Time
}

// LocalConfig mirrors the proto LocalConfig: an agent's own settings, reported
// for display so "use the agent's local value" names a concrete number.
type LocalConfig struct {
	Concurrency int
	QPS         int
	Timeout     time.Duration
	LogLevel    string
	// Extra is whatever else the application wants visible in its console —
	// local-only switches, counts of local policy entries.
	Extra map[string]string
}

// AgentConfig is the server's pushed override for one agent.
//
// Pointer fields distinguish "no opinion" from a deliberate zero. A nil field
// means the agent keeps using its local value, which is why clearing an
// override needs no separate operation: it travels as absent and the agent
// falls back.
type AgentConfig struct {
	AgentID     string
	Concurrency *int
	QPS         *int
	Timeout     *time.Duration
	LogLevel    *string
	// Payload is application-defined configuration, opaque to the framework.
	// Not a pointer: "no extra configuration" is a complete statement with
	// nothing to fall back to.
	Payload []byte
	// Version advances on every save. The agent echoes the version it has
	// applied in its heartbeat.
	Version   int
	UpdatedAt time.Time
	UpdatedBy string
}

// BootstrapToken is a single-use enrollment credential. Only its hash is
// persisted, so the plaintext is unrecoverable once shown to the operator.
type BootstrapToken struct {
	ID        string
	TokenHash string
	// Labels are preset on the agent at enrollment — a claim made on the
	// agent's behalf, which server-side overrides can still replace.
	Labels      map[string]string
	Description string
	ExpiresAt   time.Time
	UsedAt      time.Time
	UsedBy      string
	CreatedAt   time.Time
	CreatedBy   string
}

// AgentRuntime is what the stream registry knows that no table does: it is
// derived from the live connection, so it empties the moment the stream goes
// away.
type AgentRuntime struct {
	// Online is whether a stream is attached right now.
	Online bool
	// AppliedConfigVersion is the config version the agent last reported
	// having applied. Meaningless when Online is false.
	AppliedConfigVersion int64
	// RunningJobs is what the agent last reported it had in flight.
	RunningJobs int32
	// Load is the agent's self-reported load average, for display.
	Load float64
}

// EffectiveLabels applies overrides to reported labels. A nil override value
// is a tombstone: the key is removed.
//
// This is the one piece of label logic the framework owns, because the stores
// must all compute the derived column identically.
func EffectiveLabels(reported map[string]string, overrides map[string]*string) map[string]string {
	out := make(map[string]string, len(reported))
	for k, v := range reported {
		out[k] = v
	}
	for k, v := range overrides {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = *v
	}
	return out
}
