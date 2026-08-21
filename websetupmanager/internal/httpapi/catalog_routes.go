package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/service"
)

// nullableString distinguishes an omitted PATCH field from an explicit null.
// A null catalog parent means the LinuxCNC program root.
type nullableString struct {
	present bool
	value   string
}

func (value *nullableString) UnmarshalJSON(data []byte) error {
	value.present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.value = ""
		return nil
	}
	return json.Unmarshal(data, &value.value)
}

func (value nullableString) pointer() *string {
	if !value.present {
		return nil
	}
	result := value.value
	return &result
}

func (s *Server) routeCatalog(w http.ResponseWriter, r *http.Request, requestID string) bool {
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) < 3 || segments[0] != "api" || segments[1] != "v1" || segments[2] != "catalog" {
		return false
	}
	for _, segment := range segments {
		if segment == "" {
			return false
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.service == nil {
		writeError(w, http.StatusServiceUnavailable, requestID, "SERVICE_UNAVAILABLE", "The catalog service is unavailable.", nil, true)
		return true
	}
	if len(segments) == 3 {
		if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
			writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
			return true
		}
		catalog, err := s.service.GetCatalogTree(r.Context())
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSONForRequest(w, r, http.StatusOK, catalog)
		}
		return true
	}
	switch segments[3] {
	case "folders":
		return s.routeCatalogFolders(w, r, requestID, segments[4:])
	case "setups":
		return s.routeCatalogSetups(w, r, requestID, segments[4:])
	default:
		return false
	}
}

func (s *Server) routeCatalogFolders(w http.ResponseWriter, r *http.Request, requestID string, segments []string) bool {
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
			ParentFolderID *string `json:"parentFolderId"`
			Name           string  `json:"name"`
		}
		if err := decodeJSON(w, r, &body, 64<<10); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		parent := ""
		if body.ParentFolderID != nil {
			parent = *body.ParentFolderID
		}
		folder, err := s.service.CreateCatalogFolder(r.Context(), service.CreateCatalogFolderInput{
			ParentFolderID: parent, Name: body.Name, IdempotencyKey: key,
		})
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			w.Header().Set("Location", "/api/v1/catalog/folders/"+folder.ID)
			writeJSON(w, http.StatusCreated, folder)
		}
		return true
	}
	if len(segments) != 1 {
		return false
	}
	folderID := segments[0]
	if !safeEntityID(folderID) {
		return false
	}
	switch r.Method {
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
			Name             nullableString  `json:"name"`
			ParentFolderID   nullableString  `json:"parentFolderId"`
		}
		if err := decodeJSON(w, r, &body, 64<<10); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		folder, err := s.service.UpdateCatalogFolder(r.Context(), folderID, service.UpdateCatalogFolderInput{
			ExpectedRevision: body.ExpectedRevision, Name: body.Name.pointer(),
			ParentFolderID: body.ParentFolderID.pointer(), IdempotencyKey: key,
		})
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, folder)
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
		revision, err := parseRevision(r.URL.Query().Get("expectedRevision"))
		if err == nil {
			err = s.service.DeleteCatalogFolder(r.Context(), folderID, revision, key)
		}
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		methodNotAllowed(w, requestID, http.MethodPatch, http.MethodDelete)
	}
	return true
}

func (s *Server) routeCatalogSetups(w http.ResponseWriter, r *http.Request, requestID string, segments []string) bool {
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
			FolderID    *string `json:"folderId"`
			Name        string  `json:"name"`
			Description string  `json:"description"`
		}
		if err := decodeJSON(w, r, &body, 64<<10); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		folder := ""
		if body.FolderID != nil {
			folder = *body.FolderID
		}
		setup, err := s.service.CreateCatalogSetup(r.Context(), service.CreateCatalogSetupInput{
			FolderID: folder, Name: body.Name, Description: body.Description, IdempotencyKey: key,
		})
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			w.Header().Set("Location", "/api/v1/catalog/setups/"+setup.ID)
			writeJSON(w, http.StatusCreated, setup)
		}
		return true
	}
	setupID := segments[0]
	if !safeEntityID(setupID) {
		return false
	}
	if len(segments) == 1 {
		return s.routeCatalogSetup(w, r, requestID, setupID)
	}
	if len(segments) == 2 {
		role, ok := catalogRole(segments[1])
		if !ok {
			return false
		}
		return s.routeCatalogComponent(w, r, requestID, setupID, role)
	}
	if len(segments) == 3 && segments[2] == "content" {
		role, ok := catalogRole(segments[1])
		if !ok {
			return false
		}
		s.serveCatalogContent(w, r, requestID, setupID, role)
		return true
	}
	return false
}

func (s *Server) routeCatalogSetup(w http.ResponseWriter, r *http.Request, requestID, setupID string) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		setup, err := s.service.GetCatalogSetup(r.Context(), setupID)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSONForRequest(w, r, http.StatusOK, setup)
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
			Name             nullableString  `json:"name"`
			Description      nullableString  `json:"description"`
			FolderID         nullableString  `json:"folderId"`
		}
		if err := decodeJSON(w, r, &body, 64<<10); err != nil {
			writeBadJSON(w, requestID)
			return true
		}
		setup, err := s.service.UpdateCatalogSetup(r.Context(), setupID, service.UpdateCatalogSetupInput{
			ExpectedRevision: body.ExpectedRevision, Name: body.Name.pointer(),
			Description: body.Description.pointer(), FolderID: body.FolderID.pointer(), IdempotencyKey: key,
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
		revision, err := parseRevision(r.URL.Query().Get("expectedRevision"))
		if err == nil {
			err = s.service.DeleteCatalogSetup(r.Context(), setupID, revision, key)
		}
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		methodNotAllowed(w, requestID, http.MethodGet, http.MethodHead, http.MethodPatch, http.MethodDelete)
	}
	return true
}

func (s *Server) routeCatalogComponent(w http.ResponseWriter, r *http.Request, requestID, setupID string, role domain.ArtifactRole) bool {
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		methodNotAllowed(w, requestID, http.MethodPut, http.MethodDelete)
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
	revision, err := parseRevision(r.URL.Query().Get("expectedRevision"))
	if err != nil {
		writeDomainError(w, requestID, err)
		return true
	}
	if r.Method == http.MethodDelete {
		expectedVersion, createOnly, preconditionErr := catalogMutationPrecondition(r)
		if preconditionErr != nil || createOnly {
			if preconditionErr == nil {
				preconditionErr = domain.NewError(domain.CodePreconditionRequired, "file deletion requires If-Match")
			}
			writeDomainError(w, requestID, preconditionErr)
			return true
		}
		setup, err := s.service.DeleteCatalogFile(r.Context(), setupID, role, revision, expectedVersion, key)
		if err != nil {
			writeDomainError(w, requestID, err)
		} else {
			writeJSON(w, http.StatusOK, setup)
		}
		return true
	}
	name, err := catalogUploadName(r)
	if err != nil {
		writeDomainError(w, requestID, err)
		return true
	}
	expectedVersion, createOnly, err := catalogMutationPrecondition(r)
	if err != nil {
		writeDomainError(w, requestID, err)
		return true
	}
	setup, err := s.service.PutCatalogFile(r.Context(), setupID, role, service.PutCatalogFileInput{
		ExpectedRevision: revision, ExpectedFileVersion: expectedVersion, CreateOnly: createOnly,
		DisplayName: name, Content: r.Body,
		ExpectedSize: requestSize(r), IdempotencyKey: key,
	})
	if err != nil {
		writeDomainError(w, requestID, err)
	} else {
		writeJSON(w, http.StatusOK, setup)
	}
	return true
}

func catalogMutationPrecondition(r *http.Request) (expectedVersion string, createOnly bool, err error) {
	ifMatch := r.Header.Values("If-Match")
	ifNoneMatch := r.Header.Values("If-None-Match")
	if len(ifMatch)+len(ifNoneMatch) != 1 || len(ifMatch) > 1 || len(ifNoneMatch) > 1 {
		return "", false, domain.NewError(domain.CodePreconditionRequired, "exactly one file precondition is required")
	}
	if len(ifNoneMatch) == 1 {
		if strings.TrimSpace(ifNoneMatch[0]) != "*" {
			return "", false, domain.NewError(domain.CodePreconditionRequired, "file creation requires If-None-Match: *")
		}
		return "", true, nil
	}
	value := strings.TrimSpace(ifMatch[0])
	if len(value) != 66 || value[0] != '"' || value[len(value)-1] != '"' || !lowerHex64(value[1:len(value)-1]) {
		return "", false, domain.NewError(domain.CodePreconditionRequired, "file replacement requires one exact quoted ETag")
	}
	return value[1 : len(value)-1], false, nil
}

func lowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			if value[index] < 'a' || value[index] > 'f' {
				return false
			}
		}
	}
	return true
}

func catalogRole(segment string) (domain.ArtifactRole, bool) {
	switch segment {
	case "program":
		return domain.ArtifactRoleProgram, true
	case "setup-sheet":
		return domain.ArtifactRoleSetupSheet, true
	default:
		return "", false
	}
}

func catalogUploadName(r *http.Request) (string, error) {
	values := r.Header.Values("X-File-Name")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", domain.NewError(domain.CodeInvalidName, "X-File-Name is required exactly once")
	}
	decoded, err := url.PathUnescape(values[0])
	if err != nil || strings.TrimSpace(decoded) == "" || containsEncodedPathSyntax(decoded) {
		return "", domain.NewError(domain.CodeInvalidName, "X-File-Name is invalid")
	}
	return decoded, nil
}

func containsEncodedPathSyntax(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "%2e") || strings.Contains(lower, "%2f") ||
		strings.Contains(lower, "%5c") || strings.Contains(lower, "%00")
}
