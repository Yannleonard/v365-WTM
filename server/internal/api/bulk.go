package api

import (
	"net/http"
	"time"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// bulkTarget identifies one VM in a bulk request: its owning provider + vm id.
type bulkTarget struct {
	ProviderID string `json:"providerId"`
	VMID       string `json:"vmId"`
}

// bulkRequest is the POST /vm/bulk body. Action is the verb (power|snapshot|
// delete); Op qualifies a power action (start|stop|reset|suspend|resume).
type bulkRequest struct {
	Action  string       `json:"action"`
	Op      string       `json:"op,omitempty"`
	Name    string       `json:"name,omitempty"` // optional snapshot name
	Force   bool         `json:"force,omitempty"`
	Targets []bulkTarget `json:"targets"`
}

// bulkResult is the per-target outcome.
type bulkResult struct {
	ProviderID string `json:"providerId"`
	VMID       string `json:"vmId"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	TaskID     string `json:"taskId,omitempty"`
}

// bulkResponse aggregates the fan-out outcome.
type bulkResponse struct {
	Action    string       `json:"action"`
	Op        string       `json:"op,omitempty"`
	Succeeded int          `json:"succeeded"`
	Failed    int          `json:"failed"`
	Results   []bulkResult `json:"results"`
}

// bulkPermForAction returns the SAME permission the single-VM action requires, so
// bulk cannot be used to bypass per-action RBAC. An unknown action yields "".
func bulkPermForAction(action string) string {
	switch action {
	case "power":
		return "vm.power"
	case "snapshot":
		return "vm.snapshot"
	case "delete":
		return "vm.delete"
	}
	return ""
}

// VMBulk fans a single action out across many VMs (possibly across providers),
// returning a per-target result. Each target is gated by the SAME permission the
// single action needs, evaluated at that target's provider scope — a caller
// lacking the permission for a given provider gets a per-target 403-style "denied"
// result rather than failing the whole batch. This is the multi-select bulk bar's
// backend (Power On/Off, Snapshot, Delete).
func (s *Server) VMBulk(w http.ResponseWriter, r *http.Request) {
	u := authz.UserFrom(r)
	if u == nil {
		authz.WriteError(w, r, authz.ErrUnauthenticated)
		return
	}
	var req bulkRequest
	if err := decodeJSON(w, r, &req); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	perm := bulkPermForAction(req.Action)
	if perm == "" {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "Unknown bulk action: "+req.Action))
		return
	}
	if len(req.Targets) == 0 {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "No targets specified."))
		return
	}
	if req.Action == "power" {
		if op := vprovider.PowerOp(req.Op); !op.Valid() {
			authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "Unknown power operation: "+req.Op))
			return
		}
	}

	resp := bulkResponse{Action: req.Action, Op: req.Op, Results: make([]bulkResult, 0, len(req.Targets))}
	for _, t := range req.Targets {
		res := s.runBulkTarget(r, u, perm, req, t)
		if res.OK {
			resp.Succeeded++
		} else {
			resp.Failed++
		}
		resp.Results = append(resp.Results, res)
	}
	ok(w, &resp)
}

// runBulkTarget executes one target's action, gating it by perm at the target's
// provider scope. It never panics on a missing provider — it records a per-target
// error instead so one bad target cannot abort the batch.
func (s *Server) runBulkTarget(r *http.Request, u *authz.User, perm string, req bulkRequest, t bulkTarget) bulkResult {
	res := bulkResult{ProviderID: t.ProviderID, VMID: t.VMID}

	// Per-target RBAC: the same permission the single action needs, scoped to the
	// target's provider (a global grant still matches). Mirrors scopeFromProvider.
	if !u.Can(perm, authz.Scope{Type: "host", ID: t.ProviderID}) {
		res.Error = "forbidden"
		return res
	}
	p, ok := s.vreg.Get(t.ProviderID)
	if !ok {
		res.Error = "unknown provider: " + t.ProviderID
		return res
	}

	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	var task *vprovider.Task
	var err error
	switch req.Action {
	case "power":
		task, err = p.PowerOp(ctx, t.VMID, vprovider.PowerOp(req.Op))
	case "snapshot":
		task, err = p.Snapshot(ctx, t.VMID, vprovider.SnapshotOptions{Name: req.Name})
	case "delete":
		task, err = p.DeleteVM(ctx, t.VMID, vprovider.DeleteOptions{Force: req.Force})
	}
	if err != nil {
		res.Error = vmProviderError(err).Error()
		return res
	}
	res.OK = true
	if task != nil {
		res.TaskID = task.ID
	}
	return res
}
