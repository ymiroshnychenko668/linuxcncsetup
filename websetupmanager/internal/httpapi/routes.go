package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/service"
)

func (s *Server) routeDomain(w http.ResponseWriter, r *http.Request, requestID string) bool {
	if s.service == nil {
		writeError(w, http.StatusServiceUnavailable, requestID, "SERVICE_UNAVAILABLE", "The setup service is unavailable.", nil, true)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	segments := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(segments) < 3 || segments[0] != "api" || segments[1] != "v1" {
		return false
	}
	for _, segment := range segments {
		if segment == "" {
			return false
		}
	}
	switch segments[2] {
	case "setups":
		return s.routeSetups(w, r, requestID, segments[3:])
	case "setup-imports":
		return s.routeImports(w, r, requestID, segments[3:])
	case "current-setup":
		return s.routeCurrent(w, r, requestID, segments[3:])
	case "recent-setups":
		return s.routeRecent(w, r, requestID, segments[3:])
	case "ui-state":
		return s.routeUIState(w, r, requestID, segments[3:])
	case "jobs":
		return s.routeJobs(w, r, requestID, segments[3:])
	default:
		return false
	}
}

func (s *Server) routeImports(w http.ResponseWriter, r *http.Request, requestID string, segments []string) bool {
	if len(segments) == 1 && segments[0] == "preflight" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, requestID, http.MethodPost)
			return true
		}
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		var body service.ImportPreflightInput
		if err := decodeJSON(w, r, &body, 1<<20); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		result, err := s.service.PreflightImport(r.Context(), body)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, result)
		}
		return true
	}
	if len(segments) == 0 {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, requestID, http.MethodPost)
			return true
		}
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		var body struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := decodeJSON(w, r, &body, 64<<10); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		session, err := s.service.StartImport(r.Context(), service.StartImportInput{
			Name: body.Name, Description: body.Description, IdempotencyKey: key,
		})
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			w.Header().Set("Location", "/api/v1/setup-imports/"+session.ID)
			writeJSON(w, http.StatusCreated, session)
		}
		return true
	}
	sessionID := segments[0]
	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			session, err := s.service.GetImport(r.Context(), sessionID)
			if err != nil {
				writeDomainError(w, requestID, err)
			} else {
				writeJSON(w, http.StatusOK, session)
			}
		case http.MethodDelete:
			if !requireMutation(s, w, r, requestID) {
				return true
			}
			key, err := requiredIdempotency(r)
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			session, err := s.service.CancelImport(r.Context(), sessionID, key)
			if err != nil {
				writeDomainError(w, requestID, err)
			} else {
				writeJSON(w, http.StatusOK, session)
			}
		default:
			methodNotAllowed(w, requestID, http.MethodGet, http.MethodDelete)
		}
		return true
	}
	if len(segments) == 2 && segments[1] == "artifacts" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, requestID, http.MethodPost)
			return true
		}
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		role := domain.ArtifactRole(r.URL.Query().Get("role"))
		name := r.URL.Query().Get("name")
		artifact, err := s.service.UploadImportArtifact(r.Context(), sessionID, service.UploadArtifactInput{
			Role: role, DisplayName: name, Content: r.Body, ExpectedSize: requestSize(r), IdempotencyKey: key,
		})
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusCreated, artifact)
		}
		return true
	}
	if len(segments) == 3 && segments[1] == "artifacts" {
		if r.Method != http.MethodDelete {
			methodNotAllowed(w, requestID, http.MethodDelete)
			return true
		}
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		session, err := s.service.ExcludeImportArtifact(r.Context(), sessionID, segments[2], key)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, session)
		}
		return true
	}
	if len(segments) == 2 && segments[1] == "commit" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, requestID, http.MethodPost)
			return true
		}
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		var body struct {
			ExpectedArtifactIDs []string `json:"expectedArtifactIds"`
			PrimaryArtifactID   string   `json:"primaryArtifactId"`
			SavePartialDraft    bool     `json:"savePartialDraft"`
		}
		if err := decodeJSON(w, r, &body, 128<<10); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		setup, err := s.service.CommitImport(r.Context(), sessionID, service.CommitImportInput{
			ExpectedArtifactIDs: body.ExpectedArtifactIDs, PrimaryArtifactID: body.PrimaryArtifactID,
			SavePartialDraft: body.SavePartialDraft, IdempotencyKey: key,
		})
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			w.Header().Set("Location", "/api/v1/setups/"+setup.ID)
			writeJSON(w, http.StatusCreated, setup)
		}
		return true
	}
	return false
}

func (s *Server) routeSetups(w http.ResponseWriter, r *http.Request, requestID string, segments []string) bool {
	if len(segments) == 1 && segments[0] == "name-check" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, requestID, http.MethodGet)
			return true
		}
		match, err := s.service.FindSetupNameMatch(r.Context(), r.URL.Query().Get("name"))
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"match": match})
		}
		return true
	}
	if len(segments) == 0 {
		switch r.Method {
		case http.MethodGet:
			statuses, err := parseStatuses(r.URL.Query()["status"])
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			limit, err := parseOptionalInt(r.URL.Query().Get("limit"))
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			hasSheet, err := parseOptionalBool(r.URL.Query().Get("hasSetupSheet"))
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			current, err := parseOptionalBool(r.URL.Query().Get("current"))
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			page, err := s.service.ListSetups(r.Context(), service.ListSetupsOptions{
				Query: r.URL.Query().Get("q"), Statuses: statuses,
				HasSetupSheet: hasSheet, Current: current, Sort: r.URL.Query().Get("sort"),
				Cursor: r.URL.Query().Get("cursor"), Limit: limit,
			})
			if err != nil {
				writeDomainError(w, requestID, err)
			} else {
				writeJSON(w, http.StatusOK, page)
			}
		case http.MethodPost:
			if !requireMutation(s, w, r, requestID) {
				return true
			}
			key, err := requiredIdempotency(r)
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			var body struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := decodeJSON(w, r, &body, 64<<10); err != nil {
				writeBadJSON(w, requestID)
				return true
			}
			setup, err := s.service.CreateSetup(r.Context(), service.CreateSetupInput{
				Name: body.Name, Description: body.Description, IdempotencyKey: key,
			})
			if err != nil {
				writeDomainError(w, requestID, err)
			} else {
				w.Header().Set("Location", "/api/v1/setups/"+setup.ID)
				writeJSON(w, http.StatusCreated, setup)
			}
		default:
			methodNotAllowed(w, requestID, http.MethodGet, http.MethodPost)
		}
		return true
	}
	setupID := segments[0]
	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			setup, err := s.service.GetSetup(r.Context(), setupID)
			if err != nil {
				writeDomainError(w, requestID, err)
			} else {
				writeJSON(w, http.StatusOK, setup)
			}
		case http.MethodPatch:
			if !requireMutation(s, w, r, requestID) {
				return true
			}
			key, err := requiredIdempotency(r)
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			var body struct {
				ExpectedRevision domain.Revision `json:"expectedRevision"`
				Name             string          `json:"name"`
				Description      string          `json:"description"`
			}
			if err := decodeJSON(w, r, &body, 64<<10); err != nil {
				writeBadJSON(w, requestID)
				return true
			}
			setup, err := s.service.UpdateSetup(r.Context(), setupID, service.UpdateSetupInput{
				ExpectedRevision: body.ExpectedRevision, Name: body.Name,
				Description: body.Description, IdempotencyKey: key,
			})
			if err != nil {
				writeDomainError(w, requestID, err)
			} else {
				writeJSON(w, http.StatusOK, setup)
			}
		case http.MethodDelete:
			if !requireMutation(s, w, r, requestID) {
				return true
			}
			key, err := requiredIdempotency(r)
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			var body struct {
				ExpectedRevision  domain.Revision `json:"expectedRevision"`
				ExactName         string          `json:"exactName"`
				ConfirmationToken string          `json:"confirmationToken"`
			}
			if err := decodeJSON(w, r, &body, 64<<10); err != nil {
				writeBadJSON(w, requestID)
				return true
			}
			err = s.service.PermanentDeleteSetup(r.Context(), setupID, service.PermanentDeleteInput{
				ExpectedRevision: body.ExpectedRevision, ExactName: body.ExactName,
				ConfirmationToken: body.ConfirmationToken, IdempotencyKey: key,
			})
			if err != nil {
				writeDomainError(w, requestID, err)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			methodNotAllowed(w, requestID, http.MethodGet, http.MethodPatch, http.MethodDelete)
		}
		return true
	}
	if len(segments) == 2 && segments[1] == "audit" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, requestID, http.MethodGet)
			return true
		}
		limit, err := parseOptionalInt(r.URL.Query().Get("limit"))
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		events, err := s.service.ListAuditEvents(r.Context(), setupID, limit)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"items": events})
		}
		return true
	}
	if len(segments) == 3 && segments[1] == "validations" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, requestID, http.MethodGet)
			return true
		}
		run, err := s.service.GetValidationRun(r.Context(), setupID, segments[2])
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, run)
		}
		return true
	}
	if len(segments) == 2 && segments[1] == "upload-jobs" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, requestID, http.MethodPost)
			return true
		}
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		var body struct {
			Operation        service.UploadJobOperation `json:"operation"`
			ExpectedRevision domain.Revision            `json:"expectedRevision"`
			ArtifactID       string                     `json:"artifactId"`
			ExpectedVersion  string                     `json:"expectedVersion"`
			Items            []service.UploadJobItem    `json:"items"`
		}
		if err := decodeJSON(w, r, &body, 1<<20); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		job, err := s.service.PrepareUploadJob(r.Context(), setupID, service.PrepareUploadJobInput{
			Operation: body.Operation, ExpectedRevision: body.ExpectedRevision,
			ArtifactID: body.ArtifactID, ExpectedVersion: body.ExpectedVersion,
			Items: body.Items, IdempotencyKey: key,
		})
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
			writeJSON(w, http.StatusCreated, job)
		}
		return true
	}
	if len(segments) >= 2 && segments[1] == "programs" {
		return s.routePrograms(w, r, requestID, setupID, segments[2:])
	}
	if len(segments) == 2 && segments[1] == "setup-sheet" {
		return s.routeSetupSheet(w, r, requestID, setupID)
	}
	if len(segments) == 2 {
		return s.routeSetupAction(w, r, requestID, setupID, segments[1])
	}
	return false
}

func (s *Server) routePrograms(w http.ResponseWriter, r *http.Request, requestID, setupID string, segments []string) bool {
	if len(segments) == 0 {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, requestID, http.MethodPost)
			return true
		}
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		writeDomainError(w, requestID, domain.NewError(domain.CodeUploadJobRequired, "prepare an upload job before sending program content"))
		return true
	}
	if len(segments) != 1 {
		return false
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		methodNotAllowed(w, requestID, http.MethodPut, http.MethodPatch, http.MethodDelete)
		return true
	}
	artifactID := segments[0]
	if !requireMutation(s, w, r, requestID) {
		return true
	}
	key, err := requiredIdempotency(r)
	if err != nil {
		writeDomainError(w, requestID, err)
		return true
	}
	switch r.Method {
	case http.MethodPut:
		writeDomainError(w, requestID, domain.NewError(domain.CodeUploadJobRequired, "prepare an upload job before replacing program content"))
		return true
	case http.MethodPatch:
		var body struct {
			ExpectedRevision domain.Revision `json:"expectedRevision"`
			ExpectedVersion  string          `json:"expectedVersion"`
			DisplayName      *string         `json:"displayName"`
			Primary          *bool           `json:"primary"`
		}
		if err := decodeJSON(w, r, &body, 32<<10); err != nil || (body.DisplayName == nil) == (body.Primary == nil) {
			writeBadJSON(w, requestID)
			return true
		}
		var setup *domain.Setup
		if body.DisplayName != nil {
			setup, err = s.service.RenameArtifact(r.Context(), setupID, artifactID, service.RenameArtifactInput{
				ExpectedRevision: body.ExpectedRevision, ExpectedVersion: body.ExpectedVersion,
				DisplayName: *body.DisplayName, IdempotencyKey: key,
			})
		} else if *body.Primary {
			setup, err = s.service.SetPrimaryProgram(r.Context(), setupID, artifactID, service.SetPrimaryInput{
				ExpectedRevision: body.ExpectedRevision, ExpectedVersion: body.ExpectedVersion, IdempotencyKey: key,
			})
		} else {
			err = domain.NewError(domain.CodeInvalidContent, "primary can only be set to true")
		}
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, setup)
		}
	case http.MethodDelete:
		revision, version, err := expectedMutationQuery(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		leavePrimary, err := parseOptionalBool(r.URL.Query().Get("leavePrimaryUnassigned"))
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		confirmLast, err := parseOptionalBool(r.URL.Query().Get("confirmDeleteLastProgram"))
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		setup, err := s.service.DeleteArtifact(r.Context(), setupID, artifactID, service.DeleteArtifactInput{
			ExpectedRevision: revision, ExpectedVersion: version,
			ReplacementPrimaryArtifactID: r.URL.Query().Get("replacementPrimaryArtifactId"),
			LeavePrimaryUnassigned:       leavePrimary != nil && *leavePrimary,
			ConfirmDeleteLastProgram:     confirmLast != nil && *confirmLast,
			IdempotencyKey:               key,
		})
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, setup)
		}
	default:
		methodNotAllowed(w, requestID, http.MethodPut, http.MethodPatch, http.MethodDelete)
	}
	return true
}

const maxProgramManifestBytes int64 = 1 << 20

type programUploadManifest struct {
	Programs []programUploadManifestItem `json:"programs"`
}

type programUploadManifestItem struct {
	DisplayName string `json:"displayName"`
	Size        *int64 `json:"size"`
}

func decodeProgramManifest(part *multipart.Part) (programUploadManifest, error) {
	limited := &io.LimitedReader{R: part, N: maxProgramManifestBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var manifest programUploadManifest
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return manifest, errors.New("multipart manifest contains multiple JSON values")
		}
		return manifest, err
	}
	if limited.N == 0 || len(manifest.Programs) == 0 {
		return manifest, errors.New("multipart manifest is empty or too large")
	}
	for _, item := range manifest.Programs {
		if item.DisplayName == "" || item.Size == nil || *item.Size < 0 {
			return manifest, errors.New("multipart manifest entry is incomplete")
		}
	}
	return manifest, nil
}

func invalidMultipartUpload() error {
	return domain.NewError(domain.CodeInvalidContent, "multipart program upload is invalid")
}

func (s *Server) routeSetupSheet(w http.ResponseWriter, r *http.Request, requestID, setupID string) bool {
	if r.Method == http.MethodGet {
		setup, err := s.service.GetSetup(r.Context(), setupID)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		for _, artifact := range setup.Artifacts {
			if artifact.Role == domain.ArtifactRoleSetupSheet {
				writeJSON(w, http.StatusOK, artifact)
				return true
			}
		}
		writeDomainError(w, requestID, domain.NewError(domain.CodeArtifactNotFound, "setup sheet was not found"))
		return true
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		methodNotAllowed(w, requestID, http.MethodGet, http.MethodPut, http.MethodDelete)
		return true
	}
	if !requireMutation(s, w, r, requestID) {
		return true
	}
	key, err := requiredIdempotency(r)
	if err != nil {
		writeDomainError(w, requestID, err)
		return true
	}
	switch r.Method {
	case http.MethodPut:
		writeDomainError(w, requestID, domain.NewError(domain.CodeUploadJobRequired, "prepare an upload job before sending Setup Sheet content"))
		return true
	case http.MethodDelete:
		revision, version, err := expectedMutationQuery(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		setup, err := s.service.DeleteSetupSheet(r.Context(), setupID, service.DeleteArtifactInput{
			ExpectedRevision: revision, ExpectedVersion: version, IdempotencyKey: key,
		})
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, setup)
		}
	default:
		methodNotAllowed(w, requestID, http.MethodGet, http.MethodPut, http.MethodDelete)
	}
	return true
}

func (s *Server) routeSetupAction(w http.ResponseWriter, r *http.Request, requestID, setupID, action string) bool {
	switch action {
	case "archive", "restore", "validate", "duplicate", "delete-plan":
	default:
		return false
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, requestID, http.MethodPost)
		return true
	}
	if !requireMutation(s, w, r, requestID) {
		return true
	}
	var body struct {
		ExpectedRevision domain.Revision `json:"expectedRevision"`
		Name             string          `json:"name,omitempty"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeBadJSON(w, requestID)
		return true
	}
	key, err := requiredIdempotency(r)
	if err != nil {
		writeDomainError(w, requestID, err)
		return true
	}
	if action == "delete-plan" {
		plan, err := s.service.CreateDeletePlan(r.Context(), setupID, body.ExpectedRevision, key)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, plan)
		}
		return true
	}
	var setup *domain.Setup
	switch action {
	case "archive":
		setup, err = s.service.ArchiveSetup(r.Context(), setupID, service.ArchiveInput{ExpectedRevision: body.ExpectedRevision, IdempotencyKey: key})
	case "restore":
		job, restoreErr := s.service.RestoreSetupJob(r.Context(), setupID, service.ArchiveInput{
			ExpectedRevision: body.ExpectedRevision, IdempotencyKey: key,
		})
		if restoreErr != nil {
			writeDomainError(w, requestID, restoreErr)
		} else {
			w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
			writeJSON(w, http.StatusAccepted, job)
		}
		return true
	case "validate":
		job, validateErr := s.service.ValidateSetup(r.Context(), setupID, service.ValidateInput{
			ExpectedRevision: body.ExpectedRevision, IdempotencyKey: key,
		})
		if validateErr != nil {
			writeDomainError(w, requestID, validateErr)
		} else {
			w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
			writeJSON(w, http.StatusAccepted, job)
		}
		return true
	case "duplicate":
		job, duplicateErr := s.service.DuplicateSetup(r.Context(), setupID, service.DuplicateInput{
			ExpectedRevision: body.ExpectedRevision, Name: body.Name, IdempotencyKey: key,
		})
		if duplicateErr != nil {
			writeDomainError(w, requestID, duplicateErr)
		} else {
			w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
			writeJSON(w, http.StatusAccepted, job)
		}
		return true
	}
	if err != nil {
		writeDomainError(w, requestID, err)
	} else {
		writeJSON(w, http.StatusOK, setup)
	}
	return true
}

func (s *Server) routeCurrent(w http.ResponseWriter, r *http.Request, requestID string, segments []string) bool {
	if len(segments) != 0 {
		return false
	}
	switch r.Method {
	case http.MethodGet:
		current, err := s.service.GetCurrentSetup(r.Context())
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, current)
		}
	case http.MethodPut:
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		var body struct {
			SetupID                 string          `json:"setupId"`
			ExpectedRevision        domain.Revision `json:"expectedRevision"`
			ExpectedCurrentSetupID  string          `json:"expectedCurrentSetupId"`
			ExpectedCurrentRevision domain.Revision `json:"expectedCurrentRevision"`
			Confirmed               bool            `json:"confirmed"`
		}
		if err := decodeJSON(w, r, &body, 32<<10); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		if body.SetupID == "" {
			err = s.service.ClearCurrentSetup(r.Context(), service.ClearCurrentInput{
				ExpectedSetupID: body.ExpectedCurrentSetupID, ExpectedRevision: body.ExpectedCurrentRevision,
				Confirmed: body.Confirmed, IdempotencyKey: key,
			})
			if err == nil {
				w.WriteHeader(http.StatusNoContent)
			}
		} else {
			var current *domain.CurrentSetup
			current, err = s.service.SetCurrentSetup(r.Context(), service.SetCurrentInput{
				SetupID: body.SetupID, ExpectedRevision: body.ExpectedRevision,
				ExpectedPreviousSetupID: body.ExpectedCurrentSetupID, ExpectedPreviousRevision: body.ExpectedCurrentRevision,
				Confirmed: body.Confirmed, IdempotencyKey: key,
			})
			if err == nil {
				writeJSON(w, http.StatusOK, current)
			}
		}
		if err != nil {
			writeDomainError(w, requestID, err)
		}
	default:
		methodNotAllowed(w, requestID, http.MethodGet, http.MethodPut)
	}
	return true
}

func (s *Server) routeRecent(w http.ResponseWriter, r *http.Request, requestID string, segments []string) bool {
	if len(segments) == 1 {
		setupID := segments[0]
		switch r.Method {
		case http.MethodPut:
			if !requireMutation(s, w, r, requestID) {
				return true
			}
			key, err := requiredIdempotency(r)
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			var body struct {
				ArtifactID string `json:"artifactId"`
				Line       int64  `json:"line"`
			}
			if err := decodeJSON(w, r, &body, 32<<10); err != nil {
				writeBadJSON(w, requestID)
				return true
			}
			if err := s.service.TouchRecentSetup(r.Context(), setupID, body.ArtifactID, body.Line, key); err != nil {
				writeDomainError(w, requestID, err)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		case http.MethodDelete:
			if !requireMutation(s, w, r, requestID) {
				return true
			}
			key, err := requiredIdempotency(r)
			if err != nil {
				writeDomainError(w, requestID, err)
				return true
			}
			if err := s.service.DeleteRecentSetup(r.Context(), setupID, key); err != nil {
				writeDomainError(w, requestID, err)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			methodNotAllowed(w, requestID, http.MethodPut, http.MethodDelete)
		}
		return true
	}
	if len(segments) != 0 {
		return false
	}
	switch r.Method {
	case http.MethodGet:
		items, err := s.service.ListRecentSetups(r.Context())
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		}
	case http.MethodDelete:
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		var mutationErr error
		if setupID := r.URL.Query().Get("setupId"); setupID != "" {
			mutationErr = s.service.DeleteRecentSetup(r.Context(), setupID, key)
		} else {
			mutationErr = s.service.ClearRecentSetups(r.Context(), key)
		}
		if mutationErr != nil {
			writeDomainError(w, requestID, mutationErr)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		methodNotAllowed(w, requestID, http.MethodGet, http.MethodDelete)
	}
	return true
}

func (s *Server) routeUIState(w http.ResponseWriter, r *http.Request, requestID string, segments []string) bool {
	if len(segments) != 0 {
		return false
	}
	switch r.Method {
	case http.MethodGet:
		state, err := s.service.GetUIState(r.Context(), r.URL.Query().Get("clientId"))
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, state)
		}
	case http.MethodPut:
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		var state service.UIState
		if err := decodeJSON(w, r, &state, 128<<10); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		stored, err := s.service.PutUIState(r.Context(), state, key)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, stored)
		}
	default:
		methodNotAllowed(w, requestID, http.MethodGet, http.MethodPut)
	}
	return true
}

func (s *Server) routeJobs(w http.ResponseWriter, r *http.Request, requestID string, segments []string) bool {
	if len(segments) == 0 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, requestID, http.MethodGet)
			return true
		}
		active, err := parseOptionalBool(r.URL.Query().Get("active"))
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		if active != nil && *active {
			setupID := r.URL.Query().Get("setupId")
			if setupID == "" {
				writeDomainError(w, requestID, domain.NewError(domain.CodeInvalidID, "setupId is required for active jobs"))
				return true
			}
			items, err := s.service.ListActiveJobsForSetup(r.Context(), setupID)
			if err != nil {
				writeDomainError(w, requestID, err)
			} else {
				writeJSON(w, http.StatusOK, map[string]any{"items": items})
			}
			return true
		}
		limit, err := parseOptionalInt(r.URL.Query().Get("limit"))
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		items, err := s.service.ListJobs(r.Context(), limit)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		}
		return true
	}
	if len(segments) == 2 && segments[1] == "upload" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, requestID, http.MethodPost)
			return true
		}
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		input := service.RunUploadJobInput{Content: r.Body, IdempotencyKey: key}
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaType == "multipart/form-data" {
			reader, err := r.MultipartReader()
			if err != nil {
				writeDomainError(w, requestID, invalidMultipartUpload())
				return true
			}
			manifestPart, err := reader.NextPart()
			if err != nil || manifestPart.FormName() != "manifest" || manifestPart.FileName() != "" {
				if manifestPart != nil {
					_ = manifestPart.Close()
				}
				writeDomainError(w, requestID, invalidMultipartUpload())
				return true
			}
			manifest, err := decodeProgramManifest(manifestPart)
			closeErr := manifestPart.Close()
			if err != nil || closeErr != nil {
				writeDomainError(w, requestID, invalidMultipartUpload())
				return true
			}
			input.Content = nil
			input.Source = func(yield func(service.UploadArtifactInput) error) error {
				for _, item := range manifest.Programs {
					part, nextErr := reader.NextPart()
					if nextErr != nil || part.FormName() != "program" {
						if part != nil {
							_ = part.Close()
						}
						return invalidMultipartUpload()
					}
					yieldErr := yield(service.UploadArtifactInput{Content: part, DisplayName: item.DisplayName, ExpectedSize: *item.Size})
					partErr := part.Close()
					if yieldErr != nil {
						return yieldErr
					}
					if partErr != nil {
						return invalidMultipartUpload()
					}
				}
				extra, nextErr := reader.NextPart()
				if nextErr == nil {
					_ = extra.Close()
					return invalidMultipartUpload()
				}
				if !errors.Is(nextErr, io.EOF) {
					return invalidMultipartUpload()
				}
				return nil
			}
		}
		job, err := s.service.RunUploadJob(r.Context(), segments[0], input)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, job)
		}
		return true
	}
	if len(segments) != 1 {
		return false
	}
	switch r.Method {
	case http.MethodGet:
		job, err := s.service.GetJob(r.Context(), segments[0])
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, job)
		}
	case http.MethodDelete:
		if !requireMutation(s, w, r, requestID) {
			return true
		}
		key, err := requiredIdempotency(r)
		if err != nil {
			writeDomainError(w, requestID, err)
			return true
		}
		job, err := s.service.CancelJob(r.Context(), segments[0], key)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, job)
		}
	default:
		methodNotAllowed(w, requestID, http.MethodGet, http.MethodDelete)
	}
	return true
}

func requiredIdempotency(r *http.Request) (string, error) {
	key, err := idempotencyKey(r)
	if err != nil {
		return "", domain.NewError(domain.CodeInvalidContent, "Idempotency-Key is required")
	}
	return key, nil
}

func parseStatuses(values []string) ([]domain.SetupStatus, error) {
	result := make([]domain.SetupStatus, 0)
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if item == "" {
				continue
			}
			status := domain.SetupStatus(item)
			if !status.Valid() {
				return nil, domain.NewError(domain.CodeInvalidContent, "setup status filter is invalid")
			}
			result = append(result, status)
		}
	}
	return result, nil
}

func parseOptionalInt(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, domain.NewError(domain.CodeInvalidContent, "integer query value is invalid")
	}
	return parsed, nil
}

func parseOptionalBool(value string) (*bool, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return nil, domain.NewError(domain.CodeInvalidContent, "boolean query value is invalid")
	}
	return &parsed, nil
}

func parseRevision(value string) (domain.Revision, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	revision := domain.Revision(parsed)
	if err != nil || !revision.Valid() {
		return 0, domain.NewError(domain.CodeInvalidRevision, "expected revision is required")
	}
	return revision, nil
}

func expectedMutationQuery(r *http.Request) (domain.Revision, string, error) {
	revision, err := parseRevision(r.URL.Query().Get("expectedRevision"))
	if err != nil {
		return 0, "", err
	}
	return revision, r.URL.Query().Get("expectedVersion"), nil
}

func requestSize(r *http.Request) int64 {
	if r.ContentLength < 0 {
		return -1
	}
	return r.ContentLength
}

func writeBadJSON(w http.ResponseWriter, requestID string) {
	writeError(w, http.StatusBadRequest, requestID, string(domain.CodeInvalidContent), "The JSON request body is invalid.", nil, false)
}

func methodNotAllowed(w http.ResponseWriter, requestID string, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
}
