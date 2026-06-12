// Package appapi implements the Nextcloud AppAPI shared-secret bearer scheme
// used by the ExApp build of cassini-operator.
//
// Inbound proxied requests carry AUTHORIZATION-APP-API: base64(userId:appSecret).
// AppAPI on the Nextcloud side computes that header against the same shared secret
// our container received on startup; matching values prove both that the request
// originated from Nextcloud and which user it is for.
//
// References:
//   - https://github.com/cloud-py-api/nc_py_api/blob/main/nc_py_api/_session.py
//   - https://github.com/nextcloud/app_api/blob/main/lib/Service/AppAPICommonService.php
package appapi

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"strings"
)

const (
	headerAuth      = "AUTHORIZATION-APP-API"
	headerExAppID   = "EX-APP-ID"
	headerExAppVer  = "EX-APP-VERSION"
	headerAAVersion = "AA-VERSION"
	headerRequestID = "AA-REQUEST-ID"
)

// Config holds the per-container ExApp credentials and metadata.
// All four fields are populated from env vars on startup by the operator.
type Config struct {
	AppID      string // EX-APP-ID we expect on inbound requests
	AppVersion string // EX-APP-VERSION we expect (empty = don't check)
	AppSecret  string // shared secret with the AppAPI proxy
	Logger     *log.Logger
}

// userIDContextKey is the type for the request-context value holding the
// authenticated Nextcloud user id (string). Empty for lifecycle callbacks
// where no user is involved.
type userIDContextKey struct{}

// authenticatedContextKey marks a request whose AppAPI headers Middleware
// verified, whether or not a user id was present (system/lifecycle requests
// carry an empty user id).
type authenticatedContextKey struct{}

// UserID returns the AppAPI-authenticated user id attached to the request
// context by Middleware, or "" when the request was not authenticated.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDContextKey{}).(string)
	return v
}

// Authenticated reports whether Middleware verified the request's AppAPI
// headers. It is false for requests served without an active middleware
// (e.g. a standalone deploy without APP_SECRET), which lets callers layer
// additional auth only on the unauthenticated path.
func Authenticated(ctx context.Context) bool {
	v, _ := ctx.Value(authenticatedContextKey{}).(bool)
	return v
}

// errInvalidAuth is the only error message we surface in 401 responses.
// We deliberately do not distinguish "missing header" vs "wrong secret" vs
// "wrong app id" to the caller.
var errInvalidAuth = errors.New("invalid AppAPI authentication")

// Middleware returns an http.Handler that verifies AUTHORIZATION-APP-API on
// every request. When verification fails it responds 401 and logs AA-REQUEST-ID
// only — never the header value, which contains the shared secret.
func (c Config) Middleware(next http.Handler) http.Handler {
	if strings.TrimSpace(c.AppSecret) == "" {
		// Disabled mode: pass requests through unchanged (local dev path).
		// Caller is responsible for not invoking us with an empty secret in prod;
		// see CASSINI_APPAPI_REQUIRED handling in run.go.
		return next
	}
	expectedSecret := []byte(c.AppSecret)
	expectedAppID := []byte(c.AppID)
	expectedAppVersion := []byte(c.AppVersion)
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := authenticate(r, expectedSecret, expectedAppID, expectedAppVersion)
		if !ok {
			// Log the request id (safe — it's a trace correlator, not a secret)
			// and the path. NEVER log the AUTHORIZATION-APP-API value.
			reqID := strings.TrimSpace(r.Header.Get(headerRequestID))
			if reqID == "" {
				reqID = "-"
			}
			logger.Printf("appapi auth: refused method=%s path=%s aa_request_id=%s", r.Method, r.URL.Path, reqID)
			http.Error(w, errInvalidAuth.Error(), http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), authenticatedContextKey{}, true)
		if userID != "" {
			ctx = context.WithValue(ctx, userIDContextKey{}, userID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate returns (userID, true) when the request carries a valid
// AppAPI header set. userID may be empty for system requests (lifecycle).
// All comparisons are constant-time.
func authenticate(r *http.Request, expectedSecret, expectedAppID, expectedAppVersion []byte) (string, bool) {
	raw := r.Header.Get(headerAuth)
	if raw == "" {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", false
	}
	// Expected payload: "userId:appSecret". userId may be empty.
	sep := -1
	for i := 0; i < len(decoded); i++ {
		if decoded[i] == ':' {
			sep = i
			break
		}
	}
	if sep < 0 {
		return "", false
	}
	userID := string(decoded[:sep])
	gotSecret := decoded[sep+1:]
	if subtle.ConstantTimeCompare(gotSecret, expectedSecret) != 1 {
		return "", false
	}
	gotAppID := []byte(strings.TrimSpace(r.Header.Get(headerExAppID)))
	if subtle.ConstantTimeCompare(gotAppID, expectedAppID) != 1 {
		return "", false
	}
	if len(expectedAppVersion) > 0 {
		gotAppVersion := []byte(strings.TrimSpace(r.Header.Get(headerExAppVer)))
		if subtle.ConstantTimeCompare(gotAppVersion, expectedAppVersion) != 1 {
			return "", false
		}
	}
	return userID, true
}
