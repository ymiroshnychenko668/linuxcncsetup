package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

const defaultJSONLimit int64 = 1 << 20

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any, limit int64) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	if limit <= 0 {
		limit = defaultJSONLimit
	}
	reader := http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func requireMutation(s *Server, w http.ResponseWriter, r *http.Request, requestID string) bool {
	if s.authorizeMutation(r) {
		return true
	}
	if principal, ok := requestPrincipalFrom(r.Context()); ok && principal.kind == principalSession {
		code, message := s.sessionMutationRejection(r, principal.session)
		writeError(w, http.StatusForbidden, requestID, code, message, nil, false)
		return false
	}
	writeError(w, http.StatusForbidden, requestID, "REQUEST_FORBIDDEN", "The mutation was rejected by the same-origin security policy.", nil, false)
	return false
}

func idempotencyKey(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if value == "" || len(value) > 128 {
		return "", errors.New("Idempotency-Key is required")
	}
	return value, nil
}
