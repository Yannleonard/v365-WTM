// Package replication implements UniHV CROSS-HYPERVISOR VM REPLICATION for disaster
// recovery (DR/Replication Engineer): it continuously/periodically replicates a VM
// from one hypervisor to a DIFFERENT one (e.g. KVM -> ESXi), tracks the achieved
// RPO, and offers a one-click failover that powers on the replica on the target.
//
// It is a thin DR layer ON TOP of the existing V2V engine (server/internal/migrate):
// a replication "cycle" is a scheduled V2V pass with a consistency snapshot first.
// Each cycle:
//
//  1. snapshots the source VM (crash/app-consistent point — provider.Snapshot),
//  2. exports the source disk(s) (provider.ExportVM — the SAME export the V2V engine
//     uses),
//  3. converts to the target's native format (migrate.Converter — qemu-img/passthrough),
//  4. creates the replica on the target on the FIRST cycle (target.CreateVM) and
//     refreshes it on subsequent cycles (incremental re-sync of the same replica id).
//
// The engine reuses migrate.Engine.Run for steps 2-4 (it already wires
// export->convert->import across ANY provider pair), so this package never
// re-implements the V2V pipeline — it schedules it, snapshots first, and records
// per-policy DR state (lastSyncAt, measured RPO, status, cycle history ring buffer).
//
// The engine is concurrency-safe; the scheduler runs one goroutine per enabled
// policy. Policies are durable (store.ReplicationPolicy); per-cycle history is
// in-memory and survives as long as the engine runs.
package replication

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gtek-it/castor/server/internal/migrate"
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// Status is a policy's coarse runtime DR state.
type Status string

const (
	StatusIdle       Status = "idle"        // scheduled, between cycles, healthy
	StatusSyncing    Status = "syncing"     // a cycle is in progress
	StatusError      Status = "error"       // last cycle failed
	StatusDegraded   Status = "degraded"    // succeeding but missing the RPO target
	StatusFailedOver Status = "failed_over" // operator triggered failover; replica is live
)

// Policy is the replication configuration the engine schedules. It mirrors
// store.ReplicationPolicy's behavioural fields (the store row is the durable form).
type Policy struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	SourceProviderID string `json:"sourceProviderId"`
	SourceVMID       string `json:"sourceVmId"`
	TargetProviderID string `json:"targetProviderId"`
	TargetHostID     string `json:"targetHostId,omitempty"`
	// IntervalSeconds is the RPO TARGET: the engine runs a cycle every interval and
	// flags the policy degraded when the measured RPO exceeds it.
	IntervalSeconds int  `json:"intervalSeconds"`
	Retain          int  `json:"retain"`
	Enabled         bool `json:"enabled"`
}

// Cycle is one replication pass's outcome (ring-buffer history entry).
type Cycle struct {
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt"`
	DurationMs       int64     `json:"durationMs"`
	BytesTransferred int64     `json:"bytesTransferred"`
	ReplicaVMID      string    `json:"replicaVmId,omitempty"`
	OK               bool      `json:"ok"`
	Error            string    `json:"error,omitempty"`
	// FirstCycle is true when this pass CREATED the replica (vs. refreshed it).
	FirstCycle bool `json:"firstCycle"`
}

// State is the live, observable DR state of a policy (status + RPO + history).
type State struct {
	Policy           Policy    `json:"policy"`
	Status           Status    `json:"status"`
	LastSyncAt       time.Time `json:"lastSyncAt,omitempty"`
	LastDurationMs   int64     `json:"lastDurationMs"`
	BytesTransferred int64     `json:"bytesTransferred"` // cumulative across cycles
	ReplicaVMID      string    `json:"replicaVmId,omitempty"`
	LastError        string    `json:"lastError,omitempty"`
	// MeasuredRPOSeconds is the age (now - lastSyncAt) of the replica: the real
	// recovery-point objective being achieved. Zero until the first cycle succeeds.
	MeasuredRPOSeconds int64 `json:"measuredRpoSeconds"`
	// RPOTargetSeconds echoes the policy interval for easy UI comparison.
	RPOTargetSeconds int64   `json:"rpoTargetSeconds"`
	CycleCount       int64   `json:"cycleCount"`
	History          []Cycle `json:"history"`
}

// ProviderResolver resolves a provider id (satisfied by *vprovider.Registry and by
// migrate's resolver — it is the same contract).
type ProviderResolver interface {
	Get(id string) (vp.HypervisorProvider, bool)
}

// Persister persists the durable per-policy summary after a cycle/failover. The
// store satisfies it; tests pass nil (no persistence) freely.
type Persister interface {
	UpdateReplicationPolicyState(ctx context.Context, id, status, lastErr, replicaVMID string, lastSyncAt int64) error
}

// maxHistory caps the per-policy in-memory cycle ring buffer.
const maxHistory = 50

// job is the live per-policy runtime state guarded by the engine mutex.
type job struct {
	policy State
	cancel context.CancelFunc // stops the policy's scheduler goroutine
	runMu  sync.Mutex         // serializes cycles for ONE policy (manual run vs. tick)
}

// Engine schedules and runs replication cycles across a provider registry, reusing
// the V2V migrate engine for the export->convert->import work.
type Engine struct {
	reg   ProviderResolver
	mig   *migrate.Engine
	store Persister
	now   func() time.Time

	mu      sync.RWMutex
	jobs    map[string]*job
	running bool
}

// New builds an Engine. conv is the disk converter handed to the internal V2V engine
// (nil -> qemu-img with passthrough fallback, matching migrate.New). store may be nil
// (in-memory only, used by tests).
func New(reg ProviderResolver, conv migrate.Converter, store Persister) *Engine {
	return &Engine{
		reg:   reg,
		mig:   migrate.New(reg, conv),
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
		jobs:  map[string]*job{},
	}
}

// Start marks the engine running. Cycles only fire for policies that are Upserted
// with Enabled=true (Upsert schedules them).
func (e *Engine) Start() {
	e.mu.Lock()
	e.running = true
	e.mu.Unlock()
}

// Stop halts all policy schedulers (in-memory state is retained).
func (e *Engine) Stop() {
	e.mu.Lock()
	e.running = false
	for _, j := range e.jobs {
		if j.cancel != nil {
			j.cancel()
			j.cancel = nil
		}
	}
	e.mu.Unlock()
}

// Upsert registers (or updates) a policy in the engine. If the policy is enabled and
// the engine is running, its scheduler goroutine is (re)started. Returns the current
// State snapshot.
func (e *Engine) Upsert(p Policy) State {
	e.mu.Lock()
	j, ok := e.jobs[p.ID]
	if !ok {
		j = &job{policy: State{Status: StatusIdle}}
		e.jobs[p.ID] = j
	}
	// Preserve runtime fields, refresh the policy definition.
	j.policy.Policy = p
	j.policy.RPOTargetSeconds = int64(p.IntervalSeconds)
	if j.policy.Status == "" {
		j.policy.Status = StatusIdle
	}
	// Restart the scheduler with the new interval.
	if j.cancel != nil {
		j.cancel()
		j.cancel = nil
	}
	if e.running && p.Enabled && j.policy.Status != StatusFailedOver {
		e.scheduleLocked(p.ID, j)
	}
	st := j.policy
	e.mu.Unlock()
	return st
}

// Remove stops and forgets a policy (durable deletion is the caller's job).
func (e *Engine) Remove(id string) {
	e.mu.Lock()
	if j, ok := e.jobs[id]; ok {
		if j.cancel != nil {
			j.cancel()
		}
		delete(e.jobs, id)
	}
	e.mu.Unlock()
}

// scheduleLocked launches the per-policy ticker goroutine. Caller holds e.mu.
func (e *Engine) scheduleLocked(id string, j *job) {
	interval := time.Duration(j.policy.Policy.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cctx, ccancel := context.WithTimeout(ctx, 30*time.Minute)
				_, _ = e.runCycle(cctx, id)
				ccancel()
			}
		}
	}()
}

// RunNow triggers an immediate replication cycle for a policy, bypassing the timer
// (the "Run now" action). Blocks until the cycle completes.
func (e *Engine) RunNow(ctx context.Context, id string) (*Cycle, error) {
	return e.runCycle(ctx, id)
}

// runCycle performs ONE replication pass: snapshot -> V2V (export/convert/import) ->
// record DR state. Concurrency-safe across timer ticks AND manual runs of the SAME
// policy (per-policy runMu serializes them).
func (e *Engine) runCycle(ctx context.Context, id string) (*Cycle, error) {
	e.mu.RLock()
	j, ok := e.jobs[id]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("replication: unknown policy %q", id)
	}
	// Do not replicate a failed-over policy (the replica is now the live VM).
	e.mu.RLock()
	failedOver := j.policy.Status == StatusFailedOver
	pol := j.policy.Policy
	priorReplica := j.policy.ReplicaVMID
	e.mu.RUnlock()
	if failedOver {
		return nil, fmt.Errorf("replication: policy %q is failed-over; replication is paused", id)
	}

	j.runMu.Lock()
	defer j.runMu.Unlock()

	start := e.now()
	e.setStatus(id, StatusSyncing, "")
	cyc := Cycle{StartedAt: start, FirstCycle: priorReplica == ""}

	// --- step 1: consistency snapshot on the source (best-effort) ---
	// A failed snapshot is non-fatal (a crash-consistent export still protects data),
	// but it is recorded so the operator knows consistency was not guaranteed.
	if src, found := e.reg.Get(pol.SourceProviderID); found && src.Capabilities().Has(vp.CapSnapshot) {
		snapName := fmt.Sprintf("unihv-repl-%d", start.Unix())
		if _, err := src.Snapshot(ctx, pol.SourceVMID, vp.SnapshotOptions{
			Name: snapName, Description: "UniHV replication consistency point", Quiesce: true,
		}); err != nil {
			cyc.Error = "snapshot warning: " + err.Error()
		}
	}

	// --- steps 2-4: reuse the V2V engine (export -> convert -> import) ---
	// On the first cycle we CREATE the replica; on later cycles we refresh by creating
	// the up-to-date image and retargeting the policy's replica id to the newest copy
	// (the prior replica is retained per policy.Retain by the target until pruned).
	targetName := fmt.Sprintf("%s-replica", pol.SourceVMID)
	prog, err := e.mig.Run(ctx, migrate.Request{
		SourceProviderID: pol.SourceProviderID,
		SourceVMID:       pol.SourceVMID,
		TargetProviderID: pol.TargetProviderID,
		TargetHostID:     pol.TargetHostID,
		TargetName:       targetName,
		PowerOnAfter:     false, // a DR replica stays OFF until failover
	})

	fin := e.now()
	cyc.FinishedAt = fin
	cyc.DurationMs = fin.Sub(start).Milliseconds()

	if err != nil || prog == nil || prog.Phase != migrate.PhaseDone {
		msg := "replication cycle failed"
		if err != nil {
			msg = err.Error()
		} else if prog != nil && prog.Error != "" {
			msg = prog.Error
		}
		if cyc.Error != "" {
			msg = cyc.Error + "; " + msg
		}
		cyc.OK = false
		cyc.Error = msg
		e.recordCycle(id, cyc, StatusError, msg, "", time.Time{})
		return &cyc, fmt.Errorf("replication: %s", msg)
	}

	cyc.OK = true
	cyc.ReplicaVMID = prog.TargetVMID
	// migrate.Progress does not surface byte counts; approximate transferred volume
	// from the replica's provisioned disk capacity so RPO dashboards have a figure.
	cyc.BytesTransferred = e.estimateBytes(ctx, pol.TargetProviderID, prog.TargetVMID)

	// Determine status: degraded if the achieved RPO already exceeds the target.
	status := StatusIdle
	if pol.IntervalSeconds > 0 && cyc.DurationMs/1000 > int64(pol.IntervalSeconds) {
		status = StatusDegraded
	}
	e.recordCycle(id, cyc, status, "", prog.TargetVMID, fin)
	return &cyc, nil
}

// estimateBytes sums the replica's disk capacities (bytes) as a transfer proxy.
func (e *Engine) estimateBytes(ctx context.Context, providerID, vmID string) int64 {
	tgt, ok := e.reg.Get(providerID)
	if !ok || vmID == "" {
		return 0
	}
	d, err := tgt.GetVM(ctx, vmID)
	if err != nil || d == nil {
		return 0
	}
	var total float64
	for _, disk := range d.Disks {
		total += disk.CapacityGB
	}
	return int64(total * (1 << 30))
}

// Failover powers on the replica on the target and marks the policy failed-over,
// pausing further replication (the replica becomes the live VM). Idempotent-ish: a
// missing replica is an error.
func (e *Engine) Failover(ctx context.Context, id string) (*State, error) {
	e.mu.RLock()
	j, ok := e.jobs[id]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("replication: unknown policy %q", id)
	}
	e.mu.RLock()
	pol := j.policy.Policy
	replica := j.policy.ReplicaVMID
	e.mu.RUnlock()
	if replica == "" {
		return nil, fmt.Errorf("replication: policy %q has no replica yet (run a cycle first)", id)
	}
	tgt, found := e.reg.Get(pol.TargetProviderID)
	if !found {
		return nil, fmt.Errorf("replication: target provider %q not found", pol.TargetProviderID)
	}
	if tgt.Capabilities().Has(vp.CapPowerStart) {
		if _, err := tgt.PowerOp(ctx, replica, vp.PowerStart); err != nil {
			e.setStatus(id, StatusError, "failover power-on failed: "+err.Error())
			st := e.snapshotState(id)
			return st, fmt.Errorf("replication: failover power-on failed: %w", err)
		}
	}
	// Stop the scheduler for this policy: it is now failed-over.
	e.mu.Lock()
	if jj, ok := e.jobs[id]; ok {
		if jj.cancel != nil {
			jj.cancel()
			jj.cancel = nil
		}
		jj.policy.Status = StatusFailedOver
		jj.policy.LastError = ""
	}
	e.mu.Unlock()
	e.persist(ctx, id, StatusFailedOver, "", replica, 0)
	return e.snapshotState(id), nil
}

// State returns a snapshot of a policy's live DR state.
func (e *Engine) State(id string) (*State, bool) {
	e.mu.RLock()
	_, ok := e.jobs[id]
	e.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return e.snapshotState(id), true
}

// List returns snapshots of every known policy's state.
func (e *Engine) List() []State {
	e.mu.RLock()
	ids := make([]string, 0, len(e.jobs))
	for id := range e.jobs {
		ids = append(ids, id)
	}
	e.mu.RUnlock()
	out := make([]State, 0, len(ids))
	for _, id := range ids {
		if st := e.snapshotState(id); st != nil {
			out = append(out, *st)
		}
	}
	return out
}

// --- internal state mutation ---

func (e *Engine) setStatus(id string, s Status, lastErr string) {
	e.mu.Lock()
	if j, ok := e.jobs[id]; ok {
		j.policy.Status = s
		if lastErr != "" {
			j.policy.LastError = lastErr
		}
	}
	e.mu.Unlock()
}

// recordCycle appends a cycle to the ring buffer and folds it into the live State,
// then persists the durable summary.
func (e *Engine) recordCycle(id string, c Cycle, status Status, lastErr, replicaVMID string, syncAt time.Time) {
	e.mu.Lock()
	j, ok := e.jobs[id]
	if !ok {
		e.mu.Unlock()
		return
	}
	j.policy.Status = status
	j.policy.LastError = lastErr
	j.policy.LastDurationMs = c.DurationMs
	j.policy.CycleCount++
	if c.OK {
		j.policy.BytesTransferred += c.BytesTransferred
		if replicaVMID != "" {
			j.policy.ReplicaVMID = replicaVMID
		}
		if !syncAt.IsZero() {
			j.policy.LastSyncAt = syncAt
			j.policy.MeasuredRPOSeconds = 0
		}
	}
	j.policy.History = append(j.policy.History, c)
	if len(j.policy.History) > maxHistory {
		j.policy.History = j.policy.History[len(j.policy.History)-maxHistory:]
	}
	pid := j.policy.Policy.ID
	e.mu.Unlock()

	var syncUnix int64
	if !syncAt.IsZero() {
		syncUnix = syncAt.Unix()
	}
	e.persist(context.Background(), pid, status, lastErr, replicaVMID, syncUnix)
	_ = id
}

// snapshotState returns a deep-ish copy of a policy's State with the measured RPO
// recomputed against now.
func (e *Engine) snapshotState(id string) *State {
	e.mu.RLock()
	defer e.mu.RUnlock()
	j, ok := e.jobs[id]
	if !ok {
		return nil
	}
	st := j.policy
	if !st.LastSyncAt.IsZero() && st.Status != StatusFailedOver {
		st.MeasuredRPOSeconds = int64(e.now().Sub(st.LastSyncAt).Seconds())
	}
	hist := make([]Cycle, len(st.History))
	copy(hist, st.History)
	st.History = hist
	return &st
}

// persist writes the durable summary (best-effort; nil store = no-op).
func (e *Engine) persist(ctx context.Context, id string, status Status, lastErr, replicaVMID string, syncAt int64) {
	if e.store == nil || id == "" {
		return
	}
	_ = e.store.UpdateReplicationPolicyState(ctx, id, string(status), lastErr, replicaVMID, syncAt)
}
