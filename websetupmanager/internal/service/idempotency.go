package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

const maxIdempotencyOperationBytes = 128

// idempotencyClaim describes either ownership of a new request or the stable
// terminal result of an earlier request. Callers must not execute a mutation
// when Replayed is true; replayInto decodes the stored safe response.
type idempotencyClaim struct {
	Key            string
	Operation      string
	RequestHash    string
	Replayed       bool
	ResponseStatus int
	Result         json.RawMessage
	ErrorCode      domain.ErrorCode
	libraryID      string
}

// idempotencyRequestHash returns a stable digest for an operation and its
// content-independent request description. Streaming callers should include
// the staged object's SHA-256 and size in request after the stream completes;
// readers and artifact bytes must never be passed here.
func idempotencyRequestHash(operation string, request any) (string, error) {
	if err := validateIdempotencyOperation(operation); err != nil {
		return "", err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return "", domain.WrapError(domain.CodeInvalidContent, "request cannot be made idempotent", err)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(operation))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// claimIdempotency persists ownership before an operation that spans storage
// and database transactions. Terminal same-request results are replayed;
// concurrent or differently hashed uses of the key fail closed.
func (s *Service) claimIdempotency(
	ctx context.Context,
	key, operation, requestHash string,
) (claim idempotencyClaim, finalErr error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return claim, databaseError(err)
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()
	claim, err = s.claimIdempotencyTx(ctx, tx, key, operation, requestHash)
	if err != nil {
		return claim, err
	}
	if err := tx.Commit(); err != nil {
		return idempotencyClaim{}, databaseError(err)
	}
	return claim, nil
}

// claimIdempotencyTx is the atomic variant for a mutation wholly contained in
// the caller's transaction. The caller owns commit/rollback. If it records a
// failed terminal result, it should commit that result rather than roll back.
func (s *Service) claimIdempotencyTx(
	ctx context.Context,
	tx *sql.Tx,
	key, operation, requestHash string,
) (idempotencyClaim, error) {
	if tx == nil {
		return idempotencyClaim{}, databaseError(sql.ErrTxDone)
	}
	if err := validateIdempotencyKey(key); err != nil {
		return idempotencyClaim{}, err
	}
	if err := validateIdempotencyOperation(operation); err != nil {
		return idempotencyClaim{}, err
	}
	if !validSHA256(requestHash) {
		return idempotencyClaim{}, domain.NewError(domain.CodeInvalidContent, "idempotency request hash is invalid")
	}

	now := s.now().UTC()
	claim := idempotencyClaim{Key: key, Operation: operation, RequestHash: requestHash, libraryID: s.libraryID}
	existing, err := readIdempotencyClaim(ctx, tx, s.libraryID, key)
	if errors.Is(err, sql.ErrNoRows) {
		return claim, s.insertIdempotencyClaim(ctx, tx, claim, now)
	}
	if err != nil {
		return idempotencyClaim{}, databaseError(err)
	}

	// Only terminal rows can expire. An in-flight request keeps ownership even
	// after its nominal TTL; startup recovery turns abandoned rows into stable
	// conflicts without risking two concurrent mutations.
	if existing.terminal() && !now.Before(existing.expiresAt) {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM idempotency_requests
			 WHERE library_id = ? AND key = ? AND state <> 'in_progress'`, s.libraryID, key)
		if err != nil {
			return idempotencyClaim{}, databaseError(err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return idempotencyClaim{}, databaseError(fmt.Errorf("expired idempotency claim changed concurrently"))
		}
		return claim, s.insertIdempotencyClaim(ctx, tx, claim, now)
	}

	if existing.operation != operation || existing.requestHash != requestHash {
		return idempotencyClaim{}, idempotencyConflict("Idempotency-Key was already used for a different request", false)
	}
	if existing.state == idempotencyStateInProgress {
		return idempotencyClaim{}, idempotencyConflict("an operation with this Idempotency-Key is still in progress", true)
	}
	claim.Replayed = true
	claim.ResponseStatus = existing.responseStatus
	claim.Result = append(json.RawMessage(nil), existing.result...)
	claim.ErrorCode = existing.errorCode
	return claim, nil
}

func (s *Service) insertIdempotencyClaim(
	ctx context.Context,
	tx *sql.Tx,
	claim idempotencyClaim,
	now time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO idempotency_requests(
			library_id, key, operation, request_hash, state, result_json, created_at, expires_at
		) VALUES (?, ?, ?, ?, 'in_progress', '{}', ?, ?)`,
		s.libraryID, claim.Key, claim.Operation, claim.RequestHash,
		sqlTimestamp(now), sqlTimestamp(now.Add(s.idempotencyTTL)))
	if err != nil {
		return databaseError(err)
	}
	return nil
}

// replayInto reports whether this is a replay. Successful terminal JSON is
// decoded into destination. Failed/conflicted results recreate their stable
// error code without exposing the original internal cause.
func (c idempotencyClaim) replayInto(destination any) (bool, error) {
	if !c.Replayed {
		return false, nil
	}
	if c.ErrorCode != "" {
		return true, domain.NewError(c.ErrorCode, "the idempotent operation previously reached a terminal error")
	}
	if destination == nil || len(c.Result) == 0 || string(c.Result) == "null" {
		return true, nil
	}
	if err := json.Unmarshal(c.Result, destination); err != nil {
		return true, databaseError(fmt.Errorf("decode idempotent result: %w", err))
	}
	return true, nil
}

// finishIdempotency persists a safe stable terminal result for a standalone
// claim. operationErr is represented only by its public error code.
func (s *Service) finishIdempotency(
	ctx context.Context,
	claim idempotencyClaim,
	responseStatus int,
	result any,
	operationErr error,
) (finalErr error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return databaseError(err)
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()
	if err := finishIdempotencyTx(ctx, tx, claim, responseStatus, result, operationErr); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return databaseError(err)
	}
	return nil
}

// finishIdempotencyTx is the transaction-owned counterpart of
// finishIdempotency. Replayed claims are immutable and cannot be finished.
func finishIdempotencyTx(
	ctx context.Context,
	tx *sql.Tx,
	claim idempotencyClaim,
	responseStatus int,
	result any,
	operationErr error,
) error {
	if tx == nil {
		return databaseError(sql.ErrTxDone)
	}
	if claim.Replayed || claim.Key == "" || claim.Operation == "" || !validSHA256(claim.RequestHash) {
		return domain.NewError(domain.CodeInvalidContent, "idempotency claim is invalid")
	}
	if responseStatus == 0 && operationErr != nil {
		responseStatus = idempotencyErrorStatus(operationErr)
	}
	if responseStatus < 100 || responseStatus > 599 {
		return domain.NewError(domain.CodeInvalidContent, "idempotency response status is invalid")
	}
	payload := []byte("{}")
	if result != nil {
		var err error
		payload, err = json.Marshal(result)
		if err != nil {
			return domain.WrapError(domain.CodeInvalidContent, "idempotency result cannot be encoded", err)
		}
	}
	state := idempotencyStateCompleted
	var code domain.ErrorCode
	if operationErr != nil {
		state = idempotencyStateFailed
		if value, ok := domain.ErrorCodeOf(operationErr); ok {
			code = value
		} else {
			code = domain.CodeDatabaseUnavailable
		}
		if idempotencyErrorIsConflict(code) {
			state = idempotencyStateConflict
		}
	}
	resultSQL, err := tx.ExecContext(ctx, `
		UPDATE idempotency_requests
		   SET state = ?, response_status = ?, result_json = ?, error_code = NULLIF(?, '')
		 WHERE library_id = ? AND key = ? AND operation = ? AND request_hash = ?
		   AND state = 'in_progress'`,
		state, responseStatus, string(payload), code,
		claim.libraryID, claim.Key, claim.Operation, claim.RequestHash)
	if err != nil {
		return databaseError(err)
	}
	changed, err := resultSQL.RowsAffected()
	if err != nil || changed != 1 {
		return databaseError(fmt.Errorf("idempotency claim is no longer active"))
	}
	return nil
}

const (
	idempotencyStateInProgress = "in_progress"
	idempotencyStateCompleted  = "completed"
	idempotencyStateFailed     = "failed"
	idempotencyStateConflict   = "conflict"
)

type persistedIdempotencyClaim struct {
	operation      string
	requestHash    string
	state          string
	responseStatus int
	result         json.RawMessage
	errorCode      domain.ErrorCode
	expiresAt      time.Time
}

func (c persistedIdempotencyClaim) terminal() bool {
	return c.state == idempotencyStateCompleted || c.state == idempotencyStateFailed || c.state == idempotencyStateConflict
}

func readIdempotencyClaim(
	ctx context.Context,
	tx *sql.Tx,
	libraryID, key string,
) (persistedIdempotencyClaim, error) {
	var result persistedIdempotencyClaim
	var status sql.NullInt64
	var payload, expires string
	var code sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT operation, request_hash, state, response_status, result_json, error_code, expires_at
		  FROM idempotency_requests
		 WHERE library_id = ? AND key = ?`, libraryID, key).Scan(
		&result.operation, &result.requestHash, &result.state, &status, &payload, &code, &expires)
	if err != nil {
		return persistedIdempotencyClaim{}, err
	}
	if !result.terminal() && result.state != idempotencyStateInProgress {
		return persistedIdempotencyClaim{}, fmt.Errorf("invalid idempotency state")
	}
	if !json.Valid([]byte(payload)) {
		return persistedIdempotencyClaim{}, fmt.Errorf("invalid idempotency result")
	}
	parsed, err := parseTimestamp(expires)
	if err != nil {
		return persistedIdempotencyClaim{}, err
	}
	result.responseStatus = int(status.Int64)
	result.result = json.RawMessage(payload)
	result.errorCode = domain.ErrorCode(code.String)
	result.expiresAt = parsed
	return result, nil
}

func validateIdempotencyOperation(operation string) error {
	if operation == "" || len(operation) > maxIdempotencyOperationBytes || strings.TrimSpace(operation) != operation || strings.ContainsRune(operation, 0) {
		return domain.NewError(domain.CodeInvalidContent, "idempotency operation is invalid")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func idempotencyConflict(message string, retryable bool) error {
	return &domain.Error{Code: domain.CodeIdempotencyConflict, Message: message, Retryable: retryable}
}

func idempotencyErrorIsConflict(code domain.ErrorCode) bool {
	switch code {
	case domain.CodeIdempotencyConflict, domain.CodeRevisionConflict, domain.CodeArtifactChanged,
		domain.CodeCurrentSetupConflict, domain.CodeNameConflict:
		return true
	default:
		return false
	}
}

func idempotencyErrorStatus(err error) int {
	code, ok := domain.ErrorCodeOf(err)
	if !ok {
		return 500
	}
	switch code {
	case domain.CodeSetupNotFound, domain.CodeArtifactNotFound:
		return 404
	case domain.CodeRevisionConflict, domain.CodeArtifactChanged, domain.CodeCurrentSetupConflict,
		domain.CodeNameConflict, domain.CodeIdempotencyConflict, domain.CodeInvalidSetupState,
		domain.CodeSetupNotReady:
		return 409
	case domain.CodeFileTooLarge, domain.CodeImportTooLarge:
		return 413
	case domain.CodeInsufficientStorage:
		return 507
	case domain.CodeStorageUnavailable, domain.CodeDatabaseUnavailable:
		return 503
	default:
		return 400
	}
}
