package vprovider

import (
	"sync"
	"time"
)

// TaskState is the lifecycle state of an asynchronous provider task.
type TaskState string

const (
	TaskPending   TaskState = "pending"
	TaskRunning   TaskState = "running"
	TaskSucceeded TaskState = "succeeded"
	TaskFailed    TaskState = "failed"
	TaskCancelled TaskState = "cancelled"
)

// Task represents an asynchronous operation (create/power/migrate/...). Long
// hypervisor operations return a Task immediately; progress is polled or streamed
// via TaskUpdated events. Mirrors the prompt's `Task` return type.
type Task struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`     // "createVM"|"powerOp"|"migrate"|...
	ProviderID string    `json:"providerId"`
	EntityID   string    `json:"entityId,omitempty"` // target VM/host id
	State      TaskState `json:"state"`
	Progress   int       `json:"progress"` // 0..100
	Message    string    `json:"message,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

// Done reports whether the task reached a terminal state.
func (t *Task) Done() bool {
	switch t.State {
	case TaskSucceeded, TaskFailed, TaskCancelled:
		return true
	}
	return false
}

// TaskTracker is an optional in-memory helper providers can embed to manage
// task lifecycles uniformly. It is concurrency-safe.
type TaskTracker struct {
	mu    sync.RWMutex
	tasks map[string]*Task
}

// NewTaskTracker returns an empty tracker.
func NewTaskTracker() *TaskTracker {
	return &TaskTracker{tasks: make(map[string]*Task)}
}

// Start records a new running task and returns a copy.
func (tt *TaskTracker) Start(id, kind, providerID, entityID string, now time.Time) *Task {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	t := &Task{
		ID: id, Kind: kind, ProviderID: providerID, EntityID: entityID,
		State: TaskRunning, Progress: 0, StartedAt: now, UpdatedAt: now,
	}
	tt.tasks[id] = t
	cp := *t
	return &cp
}

// Update mutates a tracked task (progress/message) and returns a copy.
func (tt *TaskTracker) Update(id string, progress int, message string, now time.Time) *Task {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	t, ok := tt.tasks[id]
	if !ok {
		return nil
	}
	t.Progress = progress
	t.Message = message
	t.UpdatedAt = now
	cp := *t
	return &cp
}

// Finish marks a task terminal (succeeded/failed) and returns a copy.
func (tt *TaskTracker) Finish(id string, state TaskState, errMsg string, now time.Time) *Task {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	t, ok := tt.tasks[id]
	if !ok {
		return nil
	}
	t.State = state
	if state == TaskSucceeded {
		t.Progress = 100
	}
	t.Error = errMsg
	t.UpdatedAt = now
	t.FinishedAt = now
	cp := *t
	return &cp
}

// Get returns a copy of a tracked task.
func (tt *TaskTracker) Get(id string) (*Task, bool) {
	tt.mu.RLock()
	defer tt.mu.RUnlock()
	t, ok := tt.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}
