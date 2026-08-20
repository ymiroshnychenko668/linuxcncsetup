package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"golang.org/x/text/cases"
)

const setupCursorVersion = 1

var setupSearchFolder = cases.Fold()

type setupCursor struct {
	Version    int    `json:"v"`
	FilterHash string `json:"f"`
	Primary    string `json:"p"`
	Secondary  string `json:"s,omitempty"`
	LastID     string `json:"i"`
}

// SetupNameMatch is the minimum public-safe identity required to warn an
// operator before creating another aggregate with the same display name.
// Setup names remain deliberately non-unique.
type SetupNameMatch struct {
	SetupID string `json:"setupId"`
	Name    string `json:"name"`
}

// FindSetupNameMatch applies the exact NFC/full-Unicode-case-fold key used by
// the domain. The lookup is library-scoped and is not bounded by a library UI
// page, so a duplicate warning cannot disappear beyond a pagination cursor.
func (s *Service) FindSetupNameMatch(ctx context.Context, name string) (*SetupNameMatch, error) {
	key, err := domain.SetupNameKey(name)
	if err != nil {
		return nil, err
	}
	var match SetupNameMatch
	err = s.db.QueryRowContext(ctx, `
		SELECT id, name
		  FROM setups
		 WHERE library_id = ? AND wsm_setup_name_key(name) = ?
		 ORDER BY created_at ASC, id ASC
		 LIMIT 1`, s.libraryID, key).Scan(&match.SetupID, &match.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, databaseError(err)
	}
	return &match, nil
}

// CreateSetup creates an empty, stable draft aggregate. Display names are
// labels and deliberately are not used as identifiers or required to be
// unique.
func (s *Service) CreateSetup(ctx context.Context, input CreateSetupInput) (*domain.Setup, error) {
	name, err := domain.NormalizeSetupName(input.Name)
	if err != nil {
		return nil, err
	}
	if err := validateDescription(input.Description); err != nil {
		return nil, err
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	hash, err := idempotencyRequestHash("createSetup", struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}{name, input.Description})
	if err != nil {
		return nil, databaseError(err)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback()
	claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "createSetup", hash)
	if err != nil {
		return nil, err
	}
	var replay domain.Setup
	if replayed, replayErr := claim.replayInto(&replay); replayed || replayErr != nil {
		if err := tx.Commit(); err != nil {
			return nil, databaseError(err)
		}
		if replayErr != nil {
			return nil, replayErr
		}
		return &replay, nil
	}
	if err := beginLifecycleMutation(ctx, tx); err != nil {
		return nil, err
	}

	setupID, err := domain.NewSetupID()
	if err != nil {
		return nil, finishLifecycleFailure(ctx, tx, claim, err)
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO setups(
			id, library_id, name, description, status, revision, source,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, 'draft', ?, 'created', ?, ?)`,
		setupID, s.libraryID, name, input.Description, domain.InitialRevision,
		sqlTimestamp(now), sqlTimestamp(now)); err != nil {
		return nil, finishLifecycleFailure(ctx, tx, claim, databaseError(err))
	}
	if err := s.appendAudit(ctx, tx, domain.AuditOperationCreate, setupID, "", "", 0,
		domain.InitialRevision, domain.AuditResultSucceeded, "", nil); err != nil {
		return nil, finishLifecycleFailure(ctx, tx, claim, err)
	}
	setup, err := s.loadSetup(ctx, tx, setupID, true)
	if err != nil {
		return nil, finishLifecycleFailure(ctx, tx, claim, err)
	}
	if err := finishIdempotencyTx(ctx, tx, claim, 201, setup, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseError(err)
	}
	return setup, nil
}

// GetSetup returns the aggregate card. The explicit recent-setups mutation is
// invoked by the UI only after the card or preview was successfully shown, so
// this read remains side-effect free and every public mutation is idempotent.
func (s *Service) GetSetup(ctx context.Context, setupID string) (*domain.Setup, error) {
	return s.loadSetup(ctx, s.db, setupID, true)
}

// ListSetups searches setup labels, descriptions, and program display names
// before applying an opaque, query-bound cursor. Empty status filters exclude
// archived setups, matching the primary library view.
func (s *Service) ListSetups(ctx context.Context, options ListSetupsOptions) (*SetupPage, error) {
	normalized, orderBy, err := normalizeListOptions(options)
	if err != nil {
		return nil, err
	}
	fingerprint, err := setupFilterHash(normalized)
	if err != nil {
		return nil, databaseError(err)
	}
	var cursor *setupCursor
	if normalized.Cursor != "" {
		decoded, err := decodeSetupCursor(normalized.Cursor)
		if err != nil || decoded.FilterHash != fingerprint || validateSetupCursorSort(decoded, normalized.Sort) != nil {
			return nil, domain.NewError(domain.CodeInvalidContent, "setup cursor is invalid for this query")
		}
		cursor = decoded
	}

	placeholders := make([]string, len(normalized.Statuses))
	arguments := make([]any, 0, len(normalized.Statuses)+12)
	arguments = append(arguments, s.libraryID)
	for index, status := range normalized.Statuses {
		placeholders[index] = "?"
		arguments = append(arguments, status)
	}
	conditions := []string{
		"s.library_id = ?",
		"s.status IN (" + strings.Join(placeholders, ",") + ")",
	}
	if normalized.Query != "" {
		needle := setupSearchFolder.String(normalized.Query)
		conditions = append(conditions, `(
			instr(wsm_casefold(s.name), ?) > 0 OR
			instr(wsm_casefold(s.description), ?) > 0 OR
			EXISTS(
				SELECT 1 FROM setup_artifacts searched
				 WHERE searched.setup_id = s.id AND searched.role = 'program'
				   AND instr(wsm_casefold(searched.display_name), ?) > 0
			)
		)`)
		arguments = append(arguments, needle, needle, needle)
	}
	if normalized.HasSetupSheet != nil {
		predicate := "EXISTS"
		if !*normalized.HasSetupSheet {
			predicate = "NOT EXISTS"
		}
		conditions = append(conditions, predicate+`(
			SELECT 1 FROM setup_artifacts sheet
			 WHERE sheet.setup_id = s.id AND sheet.role = 'setup_sheet'
		)`)
	}
	if normalized.Current != nil {
		predicate := "EXISTS"
		if !*normalized.Current {
			predicate = "NOT EXISTS"
		}
		conditions = append(conditions, predicate+`(
			SELECT 1 FROM current_setup selected
			 WHERE selected.library_id = s.library_id AND selected.setup_id = s.id
		)`)
	}
	if cursor != nil {
		predicate, cursorArguments := setupCursorPredicate(normalized.Sort, cursor)
		conditions = append(conditions, predicate)
		arguments = append(arguments, cursorArguments...)
	}
	query := `
		SELECT s.id, s.name, s.description, s.status, s.revision,
		       (SELECT count(*) FROM setup_artifacts a
		         WHERE a.setup_id = s.id AND a.role = 'program'),
		       EXISTS(SELECT 1 FROM setup_artifacts a
		               WHERE a.setup_id = s.id AND a.role = 'setup_sheet'),
		       EXISTS(SELECT 1 FROM current_setup c
		               WHERE c.library_id = s.library_id AND c.setup_id = s.id),
		       s.attention_reason, s.created_at, s.updated_at, r.last_opened_at
		  FROM setups s
		  LEFT JOIN recent_setups r
		    ON r.library_id = s.library_id AND r.setup_id = s.id
		 WHERE ` + strings.Join(conditions, " AND ") + `
		 ORDER BY ` + orderBy + `
		 LIMIT ?`
	arguments = append(arguments, normalized.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()

	page := &SetupPage{Items: make([]domain.SetupSummary, 0, normalized.Limit+1)}
	for rows.Next() {
		var item domain.SetupSummary
		var status, created, updated string
		var attention, opened sql.NullString
		var hasSheet, current int
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &status, &item.Revision,
			&item.ProgramCount, &hasSheet, &current, &attention, &created, &updated, &opened); err != nil {
			return nil, databaseError(err)
		}
		item.Status = domain.SetupStatus(status)
		item.HasSetupSheet = hasSheet != 0
		item.IsCurrent = current != 0
		if item.CreatedAt, err = parseTimestamp(created); err != nil {
			return nil, databaseError(err)
		}
		if item.UpdatedAt, err = parseTimestamp(updated); err != nil {
			return nil, databaseError(err)
		}
		if opened.Valid {
			value, parseErr := parseTimestamp(opened.String)
			if parseErr != nil {
				return nil, databaseError(parseErr)
			}
			item.LastOpenedAt = &value
		}
		item.NotReadyReasons = summaryNotReadyReasons(item, attention.String, s.requireSetupSheetForReady)
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}
	if len(page.Items) > normalized.Limit {
		page.Items = page.Items[:normalized.Limit]
		last := page.Items[len(page.Items)-1]
		next := setupCursorForSummary(last, normalized.Sort)
		next.Version = setupCursorVersion
		next.FilterHash = fingerprint
		page.NextCursor, err = encodeSetupCursor(next)
		if err != nil {
			return nil, databaseError(err)
		}
	}
	return page, nil
}

// UpdateSetup atomically updates aggregate metadata and advances the
// optimistic-concurrency revision. Any previously ready aggregate becomes a
// draft and must be validated again.
func (s *Service) UpdateSetup(ctx context.Context, setupID string, input UpdateSetupInput) (*domain.Setup, error) {
	if err := domain.ValidateID(setupID); err != nil {
		return nil, err
	}
	name, err := domain.NormalizeSetupName(input.Name)
	if err != nil {
		return nil, err
	}
	if err := validateDescription(input.Description); err != nil {
		return nil, err
	}
	if !input.ExpectedRevision.Valid() {
		return nil, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	if err := validateIdempotencyKey(input.IdempotencyKey); err != nil {
		return nil, err
	}
	hash, err := idempotencyRequestHash("updateSetup", struct {
		SetupID          string          `json:"setupId"`
		ExpectedRevision domain.Revision `json:"expectedRevision"`
		Name             string          `json:"name"`
		Description      string          `json:"description"`
	}{setupID, input.ExpectedRevision, name, input.Description})
	if err != nil {
		return nil, databaseError(err)
	}

	var result *domain.Setup
	err = s.withSetupLock(setupID, func() error {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return databaseError(err)
		}
		defer tx.Rollback()
		claim, err := s.claimIdempotencyTx(ctx, tx, input.IdempotencyKey, "updateSetup", hash)
		if err != nil {
			return err
		}
		var replay domain.Setup
		if replayed, replayErr := claim.replayInto(&replay); replayed || replayErr != nil {
			if err := tx.Commit(); err != nil {
				return databaseError(err)
			}
			if replayErr != nil {
				return replayErr
			}
			result = &replay
			return nil
		}
		if err := beginLifecycleMutation(ctx, tx); err != nil {
			return err
		}
		setup, err := s.loadSetup(ctx, tx, setupID, false)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		nextStatus, nextRevision, err := domain.NextMutation(setup.Status, setup.Revision, input.ExpectedRevision)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		now := sqlTimestamp(s.now())
		changed, err := tx.ExecContext(ctx, `
			UPDATE setups
			   SET name = ?, description = ?, status = ?, revision = ?,
			       ready_revision = NULL,
			       attention_reason = CASE WHEN ? = 'attention' THEN attention_reason ELSE NULL END,
			       updated_at = ?
			 WHERE library_id = ? AND id = ? AND revision = ?`,
			name, input.Description, nextStatus, nextRevision, nextStatus, now,
			s.libraryID, setupID, input.ExpectedRevision)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, databaseError(err))
		}
		rows, err := changed.RowsAffected()
		if err != nil || rows != 1 {
			return finishLifecycleFailure(ctx, tx, claim,
				domain.NewError(domain.CodeRevisionConflict, "setup revision has changed"))
		}
		result, err = s.loadSetup(ctx, tx, setupID, true)
		if err != nil {
			return finishLifecycleFailure(ctx, tx, claim, err)
		}
		if err := finishIdempotencyTx(ctx, tx, claim, 200, result, nil); err != nil {
			return err
		}
		return databaseError(tx.Commit())
	})
	return result, err
}

func finishLifecycleFailure(ctx context.Context, tx *sql.Tx, claim idempotencyClaim, operationErr error) error {
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT lifecycle_mutation"); err != nil {
		_ = tx.Rollback()
		return databaseError(err)
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT lifecycle_mutation"); err != nil {
		_ = tx.Rollback()
		return databaseError(err)
	}
	if finishErr := finishIdempotencyTx(ctx, tx, claim, 0, nil, operationErr); finishErr != nil {
		_ = tx.Rollback()
		return finishErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return databaseError(commitErr)
	}
	return operationErr
}

func beginLifecycleMutation(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT lifecycle_mutation"); err != nil {
		return databaseError(err)
	}
	return nil
}

func normalizeListOptions(options ListSetupsOptions) (ListSetupsOptions, string, error) {
	options.Query = strings.TrimSpace(options.Query)
	if len(options.Query) > 1024 {
		return options, "", domain.NewError(domain.CodeInvalidContent, "setup search query is too long")
	}
	if options.Limit == 0 {
		options.Limit = defaultSetupPageSize
	}
	if options.Limit < 1 || options.Limit > maximumSetupPageSize {
		return options, "", domain.NewError(domain.CodeInvalidContent, "setup page size is invalid")
	}
	if len(options.Statuses) == 0 {
		options.Statuses = []domain.SetupStatus{
			domain.SetupStatusDraft, domain.SetupStatusReady, domain.SetupStatusAttention,
		}
	} else {
		options.Statuses = append([]domain.SetupStatus(nil), options.Statuses...)
	}
	seen := make(map[domain.SetupStatus]struct{}, len(options.Statuses))
	for _, status := range options.Statuses {
		if !status.Valid() {
			return options, "", domain.NewError(domain.CodeInvalidContent, "setup status filter is invalid")
		}
		if _, duplicate := seen[status]; duplicate {
			return options, "", domain.NewError(domain.CodeInvalidContent, "setup status filter is duplicated")
		}
		seen[status] = struct{}{}
	}
	sort.Slice(options.Statuses, func(first, second int) bool { return options.Statuses[first] < options.Statuses[second] })

	var orderBy string
	switch options.Sort {
	case "", "updated", "updated_desc":
		options.Sort = "updated_desc"
		orderBy = "julianday(s.updated_at) DESC, s.id DESC"
	case "updated_asc":
		orderBy = "julianday(s.updated_at) ASC, s.id ASC"
	case "name", "name_asc":
		options.Sort = "name_asc"
		orderBy = "wsm_casefold(s.name) ASC, s.name ASC, s.id ASC"
	case "name_desc":
		orderBy = "wsm_casefold(s.name) DESC, s.name DESC, s.id DESC"
	case "recent", "recent_desc":
		options.Sort = "recent_desc"
		orderBy = "coalesce(julianday(r.last_opened_at), 0) DESC, julianday(s.updated_at) DESC, s.id DESC"
	default:
		return options, "", domain.NewError(domain.CodeInvalidContent, "setup sort is invalid")
	}
	return options, orderBy, nil
}

func setupFilterHash(options ListSetupsOptions) (string, error) {
	payload, err := json.Marshal(struct {
		Query         string               `json:"q"`
		Statuses      []domain.SetupStatus `json:"s"`
		HasSetupSheet *bool                `json:"h,omitempty"`
		Current       *bool                `json:"c,omitempty"`
		Sort          string               `json:"o"`
	}{setupSearchFolder.String(options.Query), options.Statuses, options.HasSetupSheet, options.Current, options.Sort})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func encodeSetupCursor(cursor setupCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeSetupCursor(value string) (*setupCursor, error) {
	if len(value) > 2048 {
		return nil, errors.New("cursor too long")
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	var cursor setupCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing cursor content")
	}
	if cursor.Version != setupCursorVersion || !validSHA256(cursor.FilterHash) ||
		domain.ValidateID(cursor.LastID) != nil || len(cursor.Primary) > 1024 || len(cursor.Secondary) > 1024 {
		return nil, errors.New("invalid cursor")
	}
	return &cursor, nil
}

func validateSetupCursorSort(cursor *setupCursor, sortOrder string) error {
	if cursor == nil {
		return errors.New("missing cursor")
	}
	switch sortOrder {
	case "updated_desc", "updated_asc":
		if cursor.Primary == "" || cursor.Secondary != "" {
			return errors.New("invalid update cursor")
		}
		_, err := parseTimestamp(cursor.Primary)
		return err
	case "name_asc", "name_desc":
		if cursor.Primary == "" || cursor.Secondary == "" {
			return errors.New("invalid name cursor")
		}
		name, err := domain.NormalizeSetupName(cursor.Secondary)
		if err != nil || name != cursor.Secondary || setupSearchFolder.String(name) != cursor.Primary {
			return errors.New("invalid name cursor")
		}
		return nil
	case "recent_desc":
		if cursor.Secondary == "" {
			return errors.New("invalid recent cursor")
		}
		if cursor.Primary != "" {
			if _, err := parseTimestamp(cursor.Primary); err != nil {
				return err
			}
		}
		_, err := parseTimestamp(cursor.Secondary)
		return err
	default:
		return errors.New("invalid cursor sort")
	}
}

func setupCursorPredicate(sortOrder string, cursor *setupCursor) (string, []any) {
	switch sortOrder {
	case "updated_asc":
		return `(julianday(s.updated_at) > julianday(?) OR
			(julianday(s.updated_at) = julianday(?) AND s.id > ?))`,
			[]any{cursor.Primary, cursor.Primary, cursor.LastID}
	case "name_asc":
		return `(wsm_casefold(s.name) > ? OR
			(wsm_casefold(s.name) = ? AND
			 (s.name > ? OR (s.name = ? AND s.id > ?))))`,
			[]any{cursor.Primary, cursor.Primary, cursor.Secondary, cursor.Secondary, cursor.LastID}
	case "name_desc":
		return `(wsm_casefold(s.name) < ? OR
			(wsm_casefold(s.name) = ? AND
			 (s.name < ? OR (s.name = ? AND s.id < ?))))`,
			[]any{cursor.Primary, cursor.Primary, cursor.Secondary, cursor.Secondary, cursor.LastID}
	case "recent_desc":
		return `(coalesce(julianday(r.last_opened_at), 0) < coalesce(julianday(?), 0) OR
			(coalesce(julianday(r.last_opened_at), 0) = coalesce(julianday(?), 0) AND
			 (julianday(s.updated_at) < julianday(?) OR
			  (julianday(s.updated_at) = julianday(?) AND s.id < ?))))`,
			[]any{cursor.Primary, cursor.Primary, cursor.Secondary, cursor.Secondary, cursor.LastID}
	default: // updated_desc
		return `(julianday(s.updated_at) < julianday(?) OR
			(julianday(s.updated_at) = julianday(?) AND s.id < ?))`,
			[]any{cursor.Primary, cursor.Primary, cursor.LastID}
	}
}

func setupCursorForSummary(summary domain.SetupSummary, sortOrder string) setupCursor {
	cursor := setupCursor{LastID: summary.ID}
	switch sortOrder {
	case "name_asc", "name_desc":
		cursor.Primary = setupSearchFolder.String(summary.Name)
		cursor.Secondary = summary.Name
	case "recent_desc":
		if summary.LastOpenedAt != nil {
			cursor.Primary = sqlTimestamp(*summary.LastOpenedAt)
		}
		cursor.Secondary = sqlTimestamp(summary.UpdatedAt)
	default:
		cursor.Primary = sqlTimestamp(summary.UpdatedAt)
	}
	return cursor
}

func summaryNotReadyReasons(summary domain.SetupSummary, attention string, requireSheet bool) []string {
	if summary.Status == domain.SetupStatusReady {
		return nil
	}
	if summary.Status == domain.SetupStatusArchived {
		return []string{"setup is archived"}
	}
	result := make([]string, 0, 3)
	if summary.ProgramCount == 0 {
		result = append(result, "add at least one G-code program")
	}
	if requireSheet && !summary.HasSetupSheet {
		result = append(result, "add a setup sheet")
	}
	if summary.Status == domain.SetupStatusAttention {
		if attention == "" {
			attention = "managed content needs attention"
		}
		result = append(result, attention)
	} else {
		result = append(result, "validate this revision")
	}
	return result
}
