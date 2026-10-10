package api

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Error codes returned in {"error": {"code": …}}. Clients switch on these, not on messages.
const (
	CodeBadRequest    = "bad_request"
	CodeUnauthorized  = "unauthorized"
	CodeForbidden     = "forbidden"
	CodeNotFound      = "not_found"
	CodeConflict      = "conflict"
	CodeRateLimited   = "rate_limited"
	CodeInternal      = "internal"
	CodeInvalidOrigin = "invalid_origin"
	CodeMFARequired   = "mfa_required"
	CodeQuotaExceeded = "quota_exceeded"
	// CodeLocked: the environment's deploys are locked (Phase 15d).
	CodeLocked = "locked"
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// clientJSON is how responses and stream events are encoded: as
// encoding/json does, except that an empty (nil) list or map is [] or {},
// never null, so clients can always iterate it. A list whose nil means
// something else than empty is a store.NullList and stays null. JSON for
// clients is never inlined in HTML: "→" and ">" stay readable.
var clientJSON = jsonv2.JoinOptions(json.DefaultOptionsV1(), jsonv2.FormatNilSliceAsNull(false),
	jsonv2.FormatNilMapAsNull(false), jsontext.EscapeForHTML(false))

// marshalClient encodes v for a client (see clientJSON).
func marshalClient(v any) ([]byte, error) { return jsonv2.Marshal(v, clientJSON) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	// Encoded before anything is sent, so a value that cannot be encoded
	// is an error response, not an empty or cut-off body.
	b, err := marshalClient(v)
	if err != nil {
		status = http.StatusInternalServerError
		b, _ = marshalClient(map[string]apiError{"error": {Code: CodeInternal, Message: "encode response: " + err.Error()}})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}

const maxBodyBytes = 1 << 20

// decodeJSON reads a JSON body strictly: unknown fields and trailing data are errors.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "request body must contain a single JSON object")
		return false
	}
	return true
}
