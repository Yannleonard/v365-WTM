package store

import (
	"context"
	"errors"
	"testing"
)

func TestStorageBackendCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Initially empty.
	list, err := st.ListStorageBackends(ctx)
	if err != nil {
		t.Fatalf("ListStorageBackends: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 backends, got %d", len(list))
	}

	b := &StorageBackend{
		ID:        NewUUID(),
		Name:      "nas-prod",
		Type:      "nfs",
		Endpoint:  "192.168.1.10",
		Target:    "/export/images",
		SecretEnc: nil, // NFS has no secret
		Enabled:   true,
	}
	if err := st.CreateStorageBackend(ctx, b); err != nil {
		t.Fatalf("CreateStorageBackend: %v", err)
	}
	if b.Status != "pending" {
		t.Fatalf("expected default status pending, got %q", b.Status)
	}
	if b.HasSecret() {
		t.Fatalf("nfs backend should have no secret")
	}

	// A cloud backend with a sealed secret.
	c := &StorageBackend{
		ID:        NewUUID(),
		Name:      "s3-backups",
		Type:      "s3",
		Target:    "my-bucket",
		Username:  "AKIDEXAMPLE",
		SecretEnc: []byte{0x01, 0x02, 0x03},
		Region:    "us-east-1",
		Enabled:   false,
	}
	if err := st.CreateStorageBackend(ctx, c); err != nil {
		t.Fatalf("CreateStorageBackend(s3): %v", err)
	}
	if !c.HasSecret() {
		t.Fatalf("s3 backend should report hasSecret")
	}

	// List returns both, ordered by name (nas-prod, s3-backups).
	list, err = st.ListStorageBackends(ctx)
	if err != nil {
		t.Fatalf("ListStorageBackends: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(list))
	}
	if list[0].Name != "nas-prod" || list[1].Name != "s3-backups" {
		t.Fatalf("unexpected order: %q, %q", list[0].Name, list[1].Name)
	}

	// Get round-trips the sealed secret bytes and all fields.
	got, err := st.GetStorageBackend(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetStorageBackend: %v", err)
	}
	if got.Region != "us-east-1" || got.Username != "AKIDEXAMPLE" || got.Target != "my-bucket" {
		t.Fatalf("field mismatch: %+v", got)
	}
	if len(got.SecretEnc) != 3 {
		t.Fatalf("secret bytes not round-tripped: %v", got.SecretEnc)
	}

	// Status update.
	if err := st.UpdateStorageBackendStatus(ctx, c.ID, "connected", ""); err != nil {
		t.Fatalf("UpdateStorageBackendStatus: %v", err)
	}
	got, _ = st.GetStorageBackend(ctx, c.ID)
	if got.Status != "connected" {
		t.Fatalf("expected connected, got %q", got.Status)
	}
	if got.LastSeenAt == 0 {
		t.Fatalf("expected last_seen_at set on connected")
	}

	// Error status records the message.
	if err := st.UpdateStorageBackendStatus(ctx, b.ID, "error", "mount failed"); err != nil {
		t.Fatalf("UpdateStorageBackendStatus(error): %v", err)
	}
	got, _ = st.GetStorageBackend(ctx, b.ID)
	if got.Status != "error" || got.LastError != "mount failed" {
		t.Fatalf("error status not recorded: %+v", got)
	}

	// Delete removes it; deleting again is ErrNotFound.
	if err := st.DeleteStorageBackend(ctx, c.ID); err != nil {
		t.Fatalf("DeleteStorageBackend: %v", err)
	}
	if err := st.DeleteStorageBackend(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on second delete, got %v", err)
	}
	if _, err := st.GetStorageBackend(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on get-after-delete, got %v", err)
	}

	list, _ = st.ListStorageBackends(ctx)
	if len(list) != 1 {
		t.Fatalf("expected 1 backend after delete, got %d", len(list))
	}
}
