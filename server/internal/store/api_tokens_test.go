package store

import (
	"context"
	"testing"
	"time"
)

// seedTokenUser creates a user the token rows can reference (FK).
func seedTokenUser(t *testing.T, st *Store) string {
	t.Helper()
	u := &User{ID: NewUUID(), Username: "tokuser", Email: "tok@example.com", PasswordHash: "x", IsActive: true}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u.ID
}

func TestAPITokenCreateAndLookup(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := seedTokenUser(t, st)

	tok := &APIToken{
		ID:          NewUUID(),
		Name:        "ci",
		UserID:      uid,
		TokenHash:   "deadbeefhash",
		Permissions: []string{"vm.read", "vm.power"},
	}
	if err := st.CreateAPIToken(ctx, tok); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	got, err := st.GetAPITokenByHash(ctx, "deadbeefhash")
	if err != nil {
		t.Fatalf("GetAPITokenByHash: %v", err)
	}
	if got.ID != tok.ID || got.UserID != uid {
		t.Fatalf("lookup mismatch: %+v", got)
	}
	if len(got.Permissions) != 2 || got.Permissions[0] != "vm.read" {
		t.Fatalf("permissions not round-tripped: %v", got.Permissions)
	}
}

func TestAPITokenExpiryFiltered(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := seedTokenUser(t, st)

	past := time.Now().Add(-time.Hour).Unix()
	tok := &APIToken{ID: NewUUID(), Name: "expired", UserID: uid, TokenHash: "expiredhash", ExpiresAt: &past}
	if err := st.CreateAPIToken(ctx, tok); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	// An expired token must NOT authenticate.
	if _, err := st.GetAPITokenByHash(ctx, "expiredhash"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for expired token, got %v", err)
	}

	// A future-dated token DOES authenticate.
	future := time.Now().Add(time.Hour).Unix()
	tok2 := &APIToken{ID: NewUUID(), Name: "live", UserID: uid, TokenHash: "livehash", ExpiresAt: &future}
	if err := st.CreateAPIToken(ctx, tok2); err != nil {
		t.Fatalf("CreateAPIToken live: %v", err)
	}
	if _, err := st.GetAPITokenByHash(ctx, "livehash"); err != nil {
		t.Fatalf("expected live token to resolve, got %v", err)
	}
}

func TestAPITokenRevoke(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := seedTokenUser(t, st)

	tok := &APIToken{ID: NewUUID(), Name: "revoke-me", UserID: uid, TokenHash: "revokehash"}
	if err := st.CreateAPIToken(ctx, tok); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	// Revoking under the WRONG user must fail (ownership enforcement).
	if err := st.RevokeAPIToken(ctx, tok.ID, "someone-else"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound revoking another user's token, got %v", err)
	}
	// Owner revoke succeeds, then the token no longer authenticates or lists.
	if err := st.RevokeAPIToken(ctx, tok.ID, uid); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := st.GetAPITokenByHash(ctx, "revokehash"); err != ErrNotFound {
		t.Fatalf("revoked token still resolves: %v", err)
	}
	list, err := st.ListAPITokensForUser(ctx, uid)
	if err != nil {
		t.Fatalf("ListAPITokensForUser: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("revoked token still listed: %d", len(list))
	}
}

func TestAPITokenTouch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := seedTokenUser(t, st)

	tok := &APIToken{ID: NewUUID(), Name: "touch", UserID: uid, TokenHash: "touchhash"}
	if err := st.CreateAPIToken(ctx, tok); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := st.TouchAPIToken(ctx, tok.ID); err != nil {
		t.Fatalf("TouchAPIToken: %v", err)
	}
	got, err := st.GetAPIToken(ctx, tok.ID)
	if err != nil {
		t.Fatalf("GetAPIToken: %v", err)
	}
	if got.LastUsedAt == nil {
		t.Fatalf("LastUsedAt not set after touch")
	}
}
