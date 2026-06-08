package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/store"
)

// apiTokenRawBytes is the entropy of a raw API token (256 bits, base64url).
const apiTokenRawBytes = 32

// createTokenRequest is the POST /tokens body.
type createTokenRequest struct {
	Name string `json:"name"`
	// Scopes is the permission subset the token may exercise. It MUST be a subset
	// of the caller's own effective grants (a token can never exceed its owner) and
	// every entry must be a known permission (or hierarchical wildcard).
	Scopes []string `json:"scopes"`
	// ExpiresInDays optionally bounds the token's lifetime (0 = non-expiring).
	ExpiresInDays int `json:"expiresInDays,omitempty"`
}

// createTokenResponse returns the persisted record PLUS the raw token exactly
// once. The raw value is never retrievable again.
type createTokenResponse struct {
	*store.APIToken
	// Token is the raw bearer credential — surfaced ONCE at creation. Clients must
	// store it now; it is unrecoverable afterward.
	Token string `json:"token"`
}

// CreateAPIToken issues a new scoped API token for the calling user. The raw
// token is returned ONCE; only its SHA-256 hash is persisted. The requested
// scopes are validated against the permission catalog AND intersected with the
// caller's own grants so a token can never out-scope its owner.
func (s *Server) CreateAPIToken(w http.ResponseWriter, r *http.Request) {
	u := authz.UserFrom(r)
	if u == nil {
		authz.WriteError(w, r, authz.ErrUnauthenticated)
		return
	}
	var req createTokenRequest
	if err := decodeJSON(w, r, &req); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if req.Name == "" {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "Token name is required."))
		return
	}
	if len(req.Scopes) == 0 {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "At least one scope is required."))
		return
	}
	// Scopes must be known permissions (or valid wildcards).
	if err := validatePermissions(req.Scopes); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	// Subset guard: every requested scope must be one the caller actually holds at
	// global scope (a token is a personal credential; it cannot grant a permission
	// the owner lacks). "*" is only allowed for a global superuser.
	for _, scope := range req.Scopes {
		if !u.Can(scope, authz.Scope{Type: "global"}) {
			authz.WriteError(w, r, authz.Errorf(authz.ErrForbidden,
				"You cannot grant a token a permission you do not hold: "+scope))
			return
		}
	}

	raw, err := authz.RandomToken(apiTokenRawBytes)
	if err != nil {
		authz.WriteError(w, r, authz.ErrInternal)
		return
	}
	// Prefix makes the credential self-identifying in logs/secret scanners.
	raw = "uhv_" + raw

	rec := &store.APIToken{
		ID:          store.NewUUID(),
		Name:        req.Name,
		UserID:      u.ID,
		TokenHash:   authz.HashAPIToken(raw),
		Permissions: req.Scopes,
		CreatedAt:   time.Now().Unix(),
	}
	if req.ExpiresInDays > 0 {
		exp := time.Now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour).Unix()
		rec.ExpiresAt = &exp
	}
	if err := s.store.CreateAPIToken(r.Context(), rec); err != nil {
		authz.WriteError(w, r, authz.ErrInternal)
		return
	}
	created(w, &createTokenResponse{APIToken: rec, Token: raw})
}

// ListAPITokens returns the caller's active tokens (metadata only; never the raw
// value, which is not stored).
func (s *Server) ListAPITokens(w http.ResponseWriter, r *http.Request) {
	u := authz.UserFrom(r)
	if u == nil {
		authz.WriteError(w, r, authz.ErrUnauthenticated)
		return
	}
	toks, err := s.store.ListAPITokensForUser(r.Context(), u.ID)
	if err != nil {
		authz.WriteError(w, r, authz.ErrInternal)
		return
	}
	if toks == nil {
		toks = []*store.APIToken{}
	}
	ok(w, toks)
}

// DeleteAPIToken revokes one of the caller's tokens by id. Ownership is enforced
// in the store (a user can only revoke their OWN tokens).
func (s *Server) DeleteAPIToken(w http.ResponseWriter, r *http.Request) {
	u := authz.UserFrom(r)
	if u == nil {
		authz.WriteError(w, r, authz.ErrUnauthenticated)
		return
	}
	if err := s.store.RevokeAPIToken(r.Context(), chi.URLParam(r, "id"), u.ID); err != nil {
		authz.WriteError(w, r, authz.MapStoreError(err))
		return
	}
	noContent(w)
}
