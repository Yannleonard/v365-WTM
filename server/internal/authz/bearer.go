package authz

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// HashAPIToken returns the hex SHA-256 of a raw API token. The raw token lives
// only in the client's possession (shown once at create); we store + look up its
// hash so a DB leak never yields a usable bearer credential. Mirrors
// HashSessionID for sessions.
func HashAPIToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// bearerToken extracts the raw token from an "Authorization: Bearer <token>"
// header, or "" when absent/malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const pfx = "Bearer "
	if len(h) <= len(pfx) || !strings.EqualFold(h[:len(pfx)], pfx) {
		return ""
	}
	return strings.TrimSpace(h[len(pfx):])
}

// BearerOrSession authenticates a request by EITHER an API token (Authorization:
// Bearer <raw>) OR the session cookie, in that order. A bearer token is hashed,
// looked up (active + non-expired), and the owning user is loaded with the
// token's SCOPED permissions (the intersection of the token's allowed permission
// set with the user's actual role grants — a token can NEVER exceed its owner).
// CSRF is NOT enforced on the bearer path (a token is not a browser cookie, so it
// is immune to CSRF); requests without a bearer header fall through to the normal
// cookie-based session resolution. On any failure: 401.
func (d *Deps) BearerOrSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw := bearerToken(r); raw != "" {
			u, apiErr := d.resolveBearer(r, raw)
			if apiErr != nil {
				WriteError(w, r, apiErr)
				return
			}
			ctx := WithBearerAuth(WithUser(r.Context(), u))
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		// No bearer header: behave exactly like SessionAuth.
		u, apiErr := d.resolveUser(r)
		if apiErr != nil {
			WriteError(w, r, apiErr)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

// resolveBearer hashes + looks up an API token, loads the owning user, and builds
// a *User whose permission set is the token's scoped subset of the owner's grants.
func (d *Deps) resolveBearer(r *http.Request, raw string) (*User, *APIError) {
	ctx := r.Context()
	tok, err := d.Store.GetAPITokenByHash(ctx, HashAPIToken(raw))
	if err != nil {
		return nil, ErrUnauthenticated
	}
	su, err := d.Store.GetUserByID(ctx, tok.UserID)
	if err != nil || !su.IsActive {
		return nil, ErrUnauthenticated
	}
	roles, err := d.Store.EffectiveRolesForUser(ctx, su.ID)
	if err != nil {
		return nil, ErrInternal
	}
	// Build the owner's full scoped permission map, then restrict it to the token's
	// allowed set. This guarantees the token grants AT MOST what the owner holds.
	owner := buildUser(su, "", su.AuthSource, roles)
	scoped := restrictPerms(owner.perms, tok.Permissions)
	u := &User{
		User:  *su,
		AMR:   su.AuthSource,
		perms: scoped,
		roles: roles,
	}
	// Best-effort last-used touch (do not fail auth on a write error).
	_ = d.Store.TouchAPIToken(ctx, tok.ID)
	return u, nil
}

// restrictPerms returns a copy of the owner's scoped permission map keeping only
// the permissions the token is scoped to. A token permission grants a concrete
// owner permission when permSetGrants(tokenSet, ownerPerm) is true — i.e. an
// exact match or a token wildcard ("vm.*") that covers the owner's grant. A "*"
// token keeps everything the owner has (a full-access token, still bounded by the
// owner). The owner's own "*" / wildcards are preserved only when the token also
// covers them, so a scoped token over an admin owner is genuinely narrowed.
func restrictPerms(ownerPerms map[string]map[string]struct{}, tokenAllowed []string) map[string]map[string]struct{} {
	tokenSet := make(map[string]struct{}, len(tokenAllowed))
	for _, p := range tokenAllowed {
		tokenSet[p] = struct{}{}
	}
	// A "*" token inherits the owner's full map verbatim.
	if _, all := tokenSet["*"]; all {
		out := make(map[string]map[string]struct{}, len(ownerPerms))
		for scope, set := range ownerPerms {
			cp := make(map[string]struct{}, len(set))
			for p := range set {
				cp[p] = struct{}{}
			}
			out[scope] = cp
		}
		return out
	}
	out := make(map[string]map[string]struct{})
	for scope, set := range ownerPerms {
		kept := make(map[string]struct{})
		for ownerPerm := range set {
			// Keep an owner grant when the token's allowed set covers it. For a
			// wildcard owner grant (e.g. owner has "*"), instead ADD the token's
			// concrete permissions to this scope so the token still works while
			// staying bounded (the owner's "*" trivially permits them).
			if ownerPerm == "*" {
				for tp := range tokenSet {
					kept[tp] = struct{}{}
				}
				continue
			}
			if permSetGrants(tokenSet, ownerPerm) {
				kept[ownerPerm] = struct{}{}
			}
		}
		if len(kept) > 0 {
			out[scope] = kept
		}
	}
	return out
}
