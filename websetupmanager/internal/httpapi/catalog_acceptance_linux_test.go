//go:build linux

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

func createCatalogFolderHTTP(t *testing.T, h *httpAcceptanceHarness, key, parentID, name string) domain.CatalogFolder {
	t.Helper()
	var parent any
	if parentID != "" {
		parent = parentID
	}
	response := h.jsonMutation(t, http.MethodPost, "/api/v1/catalog/folders", key, map[string]any{
		"parentFolderId": parent, "name": name,
	})
	requireHTTPStatus(t, response, http.StatusCreated)
	return decodeHTTPJSON[domain.CatalogFolder](t, response)
}

func createCatalogSetupHTTP(t *testing.T, h *httpAcceptanceHarness, key, folderID, name string) domain.CatalogSetup {
	t.Helper()
	var folder any
	if folderID != "" {
		folder = folderID
	}
	response := h.jsonMutation(t, http.MethodPost, "/api/v1/catalog/setups", key, map[string]any{
		"folderId": folder, "name": name, "description": "операторская заметка",
	})
	requireHTTPStatus(t, response, http.StatusCreated)
	return decodeHTTPJSON[domain.CatalogSetup](t, response)
}

func putCatalogFileHTTP(t *testing.T, h *httpAcceptanceHarness, setup domain.CatalogSetup, component, name, key string, content []byte) domain.CatalogSetup {
	t.Helper()
	target := "/api/v1/catalog/setups/" + setup.ID + "/" + component + "?expectedRevision=" + revisionString(setup.Revision)
	preconditionName, preconditionValue := "If-None-Match", "*"
	existing := setup.Program
	if component == "setup-sheet" {
		existing = setup.SetupSheet
	}
	if existing != nil {
		preconditionName, preconditionValue = "If-Match", `"`+existing.Version+`"`
	}
	response := h.request(http.MethodPut, target, bytes.NewReader(content), map[string]string{
		"Content-Type": "application/octet-stream", "Idempotency-Key": key,
		"X-File-Name": url.PathEscape(name), preconditionName: preconditionValue,
	})
	requireHTTPStatus(t, response, http.StatusOK)
	return decodeHTTPJSON[domain.CatalogSetup](t, response)
}

func revisionString(revision domain.Revision) string {
	return strconv.FormatInt(int64(revision), 10)
}

func TestHTTPCatalogCRUDDirectPlacementAndNullableMoves(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	empty := h.request(http.MethodGet, "/api/v1/catalog", nil, nil)
	requireHTTPStatus(t, empty, http.StatusOK)
	snapshot := decodeHTTPJSON[domain.CatalogTree](t, empty)
	if snapshot.Destination.RootLabel != "Программы LinuxCNC" || snapshot.Destination.RootDisplay != "~/linuxcnc/nc_files" ||
		len(snapshot.Folders) != 0 || len(snapshot.Setups) != 0 {
		t.Fatalf("empty catalog = %+v", snapshot)
	}
	if strings.Contains(empty.Body.String(), h.programDir) || strings.Contains(empty.Body.String(), h.rootDir) {
		t.Fatalf("catalog leaked a physical path: %s", empty.Body.String())
	}

	orders := createCatalogFolderHTTP(t, h, "folder-orders", "", "Заказы")
	year := createCatalogFolderHTTP(t, h, "folder-year", orders.ID, "2026")
	setup := createCatalogSetupHTTP(t, h, "setup-create", year.ID, "Деталь 42")
	if setup.Program != nil || setup.SetupSheet != nil || setup.Revision != 1 {
		t.Fatalf("new incomplete setup = %+v", setup)
	}

	// Omitting folderId must preserve the current destination exactly.
	patched := h.jsonMutation(t, http.MethodPatch, "/api/v1/catalog/setups/"+setup.ID, "setup-description", map[string]any{
		"expectedRevision": setup.Revision, "description": "новая заметка",
	})
	requireHTTPStatus(t, patched, http.StatusOK)
	setup = decodeHTTPJSON[domain.CatalogSetup](t, patched)
	if setup.FolderID != year.ID {
		t.Fatalf("omitted folderId moved setup: %+v", setup)
	}

	program := []byte("G21\nG0 X1\nM2\n")
	setup = putCatalogFileHTTP(t, h, setup, "program", "деталь 42.ngc", "program-put", program)
	if setup.Program == nil || setup.Program.RelativePath != "Заказы/2026/деталь 42.ngc" {
		t.Fatalf("program placement = %+v", setup.Program)
	}
	physical := filepath.Join(h.programDir, "Заказы", "2026", "деталь 42.ngc")
	actual, err := os.ReadFile(physical)
	if err != nil || !bytes.Equal(actual, program) {
		t.Fatalf("LinuxCNC program bytes = %q, err=%v", actual, err)
	}

	// Explicit null moves the setup (and its physical component) to the root.
	moved := h.jsonMutation(t, http.MethodPatch, "/api/v1/catalog/setups/"+setup.ID, "setup-root", map[string]any{
		"expectedRevision": setup.Revision, "folderId": nil,
	})
	requireHTTPStatus(t, moved, http.StatusOK)
	setup = decodeHTTPJSON[domain.CatalogSetup](t, moved)
	if setup.FolderID != "" || setup.Program == nil || setup.Program.RelativePath != "деталь 42.ngc" {
		t.Fatalf("root move = %+v", setup)
	}
	if _, err := os.Stat(filepath.Join(h.programDir, "деталь 42.ngc")); err != nil {
		t.Fatalf("moved program is absent: %v", err)
	}

	// Folder PATCH has the same absent-vs-null contract.
	renamed := h.jsonMutation(t, http.MethodPatch, "/api/v1/catalog/folders/"+year.ID, "folder-rename", map[string]any{
		"expectedRevision": year.Revision, "name": "2027",
	})
	requireHTTPStatus(t, renamed, http.StatusOK)
	year = decodeHTTPJSON[domain.CatalogFolder](t, renamed)
	if year.ParentFolderID != orders.ID {
		t.Fatalf("omitted parentFolderId moved folder: %+v", year)
	}
	toRoot := h.jsonMutation(t, http.MethodPatch, "/api/v1/catalog/folders/"+year.ID, "folder-root", map[string]any{
		"expectedRevision": year.Revision, "parentFolderId": nil,
	})
	requireHTTPStatus(t, toRoot, http.StatusOK)
	year = decodeHTTPJSON[domain.CatalogFolder](t, toRoot)
	if year.ParentFolderID != "" || year.RelativePath != "2027" {
		t.Fatalf("folder root move = %+v", year)
	}

	deletedProgram := h.request(http.MethodDelete,
		"/api/v1/catalog/setups/"+setup.ID+"/program?expectedRevision="+revisionString(setup.Revision), nil,
		map[string]string{"Idempotency-Key": "program-delete", "If-Match": `"` + setup.Program.Version + `"`})
	requireHTTPStatus(t, deletedProgram, http.StatusOK)
	setup = decodeHTTPJSON[domain.CatalogSetup](t, deletedProgram)
	if setup.Program != nil {
		t.Fatalf("program remains after delete: %+v", setup.Program)
	}
	if _, err := os.Stat(filepath.Join(h.programDir, "деталь 42.ngc")); !os.IsNotExist(err) {
		t.Fatalf("deleted program still exists: %v", err)
	}

	deletedSetup := h.request(http.MethodDelete,
		"/api/v1/catalog/setups/"+setup.ID+"?expectedRevision="+revisionString(setup.Revision), nil,
		map[string]string{"Idempotency-Key": "setup-delete"})
	requireHTTPStatus(t, deletedSetup, http.StatusNoContent)
}

func TestHTTPCatalogRangeETagReplacementAndSanitizedHTML(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	setup := createCatalogSetupHTTP(t, h, "content-setup", "", "Просмотр")
	setup = putCatalogFileHTTP(t, h, setup, "program", "preview.ngc", "content-program", []byte("N10 G0 X0\nN20 G1 X5\nM2\n"))
	firstVersion := setup.Program.Version
	contentURL := "/api/v1/catalog/setups/" + setup.ID + "/program/content"
	staleReplace := h.request(http.MethodPut,
		"/api/v1/catalog/setups/"+setup.ID+"/program?expectedRevision="+revisionString(setup.Revision),
		strings.NewReader("G0 X999\nM2\n"), map[string]string{
			"Idempotency-Key": "content-stale-replace", "X-File-Name": "preview.ngc",
			"If-Match": `"` + strings.Repeat("0", 64) + `"`,
		})
	requireHTTPStatus(t, staleReplace, http.StatusConflict)
	if !strings.Contains(staleReplace.Body.String(), string(domain.CodeArtifactChanged)) {
		t.Fatalf("stale replacement error = %s", staleReplace.Body.String())
	}
	unchanged, err := os.ReadFile(filepath.Join(h.programDir, "preview.ngc"))
	if err != nil || string(unchanged) != "N10 G0 X0\nN20 G1 X5\nM2\n" {
		t.Fatalf("stale replacement changed bytes: %q, %v", unchanged, err)
	}
	ranged := h.request(http.MethodGet, contentURL, nil, map[string]string{
		"Range": "bytes=4-10", "If-Match": `"` + firstVersion + `"`,
	})
	requireHTTPStatus(t, ranged, http.StatusPartialContent)
	if ranged.Body.String() != "G0 X0\nN" || ranged.Header().Get("Accept-Ranges") != "bytes" ||
		ranged.Header().Get("Content-Range") != "bytes 4-10/23" {
		t.Fatalf("range response headers/body = %q %+v", ranged.Body.String(), ranged.Header())
	}
	head := h.request(http.MethodHead, contentURL+"?version="+firstVersion, nil, nil)
	requireHTTPStatus(t, head, http.StatusOK)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != "23" || head.Header().Get("ETag") == "" {
		t.Fatalf("HEAD response = body=%q headers=%+v", head.Body.String(), head.Header())
	}

	setup = putCatalogFileHTTP(t, h, setup, "program", "preview.ngc", "content-replace", []byte("G21\nM2\n"))
	if setup.Program.Version == firstVersion {
		t.Fatal("replacement retained the old version")
	}
	stale := h.request(http.MethodGet, contentURL+"?version="+firstVersion, nil, nil)
	requireHTTPStatus(t, stale, http.StatusPreconditionFailed)

	malicious := []byte(`<!doctype html><html><head><script>steal()</script><meta http-equiv="refresh" content="0;url=https://evil.invalid"></head><body onload="steal()"><h1>Safe sheet</h1><a href="javascript:steal()">bad</a><iframe src="https://evil.invalid"></iframe></body></html>`)
	setup = putCatalogFileHTTP(t, h, setup, "setup-sheet", "sheet.html", "content-sheet", malicious)
	sheetURL := "/api/v1/catalog/setups/" + setup.ID + "/setup-sheet/content?version=" + setup.SetupSheet.Version
	sheet := h.request(http.MethodGet, sheetURL, nil, map[string]string{"If-Match": `"` + setup.SetupSheet.Version + `"`})
	requireHTTPStatus(t, sheet, http.StatusOK)
	if !strings.Contains(sheet.Body.String(), "Safe sheet") || strings.Contains(strings.ToLower(sheet.Body.String()), "script") ||
		strings.Contains(sheet.Body.String(), "evil.invalid") || sheet.Header().Get("Content-Security-Policy") != sanitizedHTMLCSP {
		t.Fatalf("unsafe HTML response: headers=%+v body=%s", sheet.Header(), sheet.Body.String())
	}

	pdfSetup := createCatalogSetupHTTP(t, h, "content-pdf-setup", "", "PDF")
	pdfSetup = putCatalogFileHTTP(t, h, pdfSetup, "setup-sheet", "sheet.pdf", "content-pdf", []byte("%PDF-1.7\n"))
	pdf := h.request(http.MethodGet, "/api/v1/catalog/setups/"+pdfSetup.ID+"/setup-sheet/content", nil, nil)
	requireHTTPStatus(t, pdf, http.StatusOK)
	if pdf.Header().Get("Content-Type") != "application/pdf" || pdf.Header().Get("Content-Disposition") != "attachment" ||
		pdf.Body.String() != "%PDF-1.7\n" {
		t.Fatalf("PDF response = headers=%+v body=%q", pdf.Header(), pdf.Body.String())
	}
}

func TestHTTPCatalogMutationSecurityAndFilenameDecoding(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	setup := createCatalogSetupHTTP(t, h, "security-setup", "", "Безопасность")
	target := "/api/v1/catalog/setups/" + setup.ID + "/program?expectedRevision=" + revisionString(setup.Revision)

	for name, mutate := range map[string]func(*http.Request){
		"missing csrf":         func(r *http.Request) { r.Header.Del("X-CSRF-Token") },
		"missing idempotency":  func(r *http.Request) { r.Header.Del("Idempotency-Key") },
		"bad encoded filename": func(r *http.Request) { r.Header.Set("X-File-Name", "%ZZ") },
		"decoded traversal":    func(r *http.Request) { r.Header.Set("X-File-Name", "%2e%2e%2Fevil.ngc") },
		"double encoded traversal": func(r *http.Request) {
			r.Header.Set("X-File-Name", "%252e%252e%252Fevil.ngc")
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080"+target, strings.NewReader("M2\n"))
			request.Host = "127.0.0.1:8080"
			request.Header.Set("Origin", "http://127.0.0.1:8080")
			request.Header.Set("X-CSRF-Token", h.server.csrfToken)
			request.Header.Set("Idempotency-Key", "security-"+strings.ReplaceAll(name, " ", "-"))
			request.Header.Set("X-File-Name", "safe.ngc")
			request.Header.Set("If-None-Match", "*")
			mutate(request)
			response := httptest.NewRecorder()
			h.server.ServeHTTP(response, request)
			if response.Code < 400 || response.Code >= 500 {
				t.Fatalf("security rejection = %d %s", response.Code, response.Body.String())
			}
		})
	}

	duplicate := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080"+target, strings.NewReader("M2\n"))
	duplicate.Host = "127.0.0.1:8080"
	duplicate.Header.Set("Origin", "http://127.0.0.1:8080")
	duplicate.Header.Set("X-CSRF-Token", h.server.csrfToken)
	duplicate.Header.Set("Idempotency-Key", "duplicate-header")
	duplicate.Header.Set("If-None-Match", "*")
	duplicate.Header.Add("X-File-Name", "safe.ngc")
	duplicate.Header.Add("X-File-Name", "other.ngc")
	duplicateResponse := httptest.NewRecorder()
	h.server.ServeHTTP(duplicateResponse, duplicate)
	requireHTTPStatus(t, duplicateResponse, http.StatusBadRequest)

	badJSON := h.request(http.MethodPatch, "/api/v1/catalog/setups/"+setup.ID,
		strings.NewReader(`{"expectedRevision":1,"folderId":false}`), map[string]string{
			"Content-Type": "application/json", "Idempotency-Key": "bad-nullable",
		})
	requireHTTPStatus(t, badJSON, http.StatusBadRequest)

	missingPrecondition := h.request(http.MethodPut, target, strings.NewReader("M2\n"), map[string]string{
		"Idempotency-Key": "missing-file-precondition", "X-File-Name": "safe.ngc",
	})
	requireHTTPStatus(t, missingPrecondition, http.StatusPreconditionRequired)
	if !strings.Contains(missingPrecondition.Body.String(), string(domain.CodePreconditionRequired)) {
		t.Fatalf("missing precondition error = %s", missingPrecondition.Body.String())
	}
	ambiguousPrecondition := h.request(http.MethodPut, target, strings.NewReader("M2\n"), map[string]string{
		"Idempotency-Key": "ambiguous-file-precondition", "X-File-Name": "safe.ngc",
		"If-Match": `"` + strings.Repeat("a", 64) + `"`, "If-None-Match": "*",
	})
	requireHTTPStatus(t, ambiguousPrecondition, http.StatusPreconditionRequired)
	python := h.request(http.MethodPut, target, strings.NewReader("print('unsafe')\n"), map[string]string{
		"Idempotency-Key": "python-extension", "X-File-Name": "job.py", "If-None-Match": "*",
	})
	requireHTTPStatus(t, python, http.StatusUnsupportedMediaType)
	if _, err := os.Stat(filepath.Join(h.programDir, "job.py")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("executable upload reached PROGRAM_PREFIX: %v", err)
	}

	if entries, err := os.ReadDir(h.programDir); err != nil || len(entries) != 0 {
		t.Fatalf("rejected uploads changed program root: entries=%v err=%v", entries, err)
	}
}

func TestHTTPCatalogOnlyProductionGateAndSafeRoutes(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	server, err := NewWithService(Config{
		ListenAddress: "127.0.0.1:8080", LibraryID: h.roots.LibraryID(), LibraryAlias: "Catalog",
		GCodeExtensions: []string{".ngc", ".nc", ".tap"},
	}, CheckFunc(h.database.Ping), CheckFunc(func(ctx context.Context) error {
		if err := h.roots.Check(); err != nil {
			return err
		}
		return h.catalog.Check()
	}), h.service, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("catalog")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{
		"/api/v1/setups", "/api/v1/current-setup", "/api/v1/recent-setups", "/api/v1/jobs", "/api/v1/setup-imports",
	} {
		response := request(server, http.MethodGet, legacy)
		requireHTTPStatus(t, response, http.StatusNotFound)
	}
	catalog := request(server, http.MethodGet, "/api/v1/catalog")
	requireHTTPStatus(t, catalog, http.StatusOK)
	capabilities := request(server, http.MethodGet, "/api/v1/capabilities")
	requireHTTPStatus(t, capabilities, http.StatusOK)
	if !strings.Contains(capabilities.Body.String(), `"setupCatalog":true`) ||
		!strings.Contains(capabilities.Body.String(), `"validation":false`) ||
		strings.Contains(capabilities.Body.String(), h.programDir) {
		t.Fatalf("catalog capabilities = %s", capabilities.Body.String())
	}
	for _, attack := range []string{
		"/api/v1/catalog/setups/../program/content",
		"/api/v1/catalog/setups/%2e%2e/program/content",
		"/api/v1/catalog/setups/%252e%252e/program/content",
		"/api/v1/catalog/setups/%2Fetc%2Fpasswd/program/content",
		"/api/v1/catalog/setups/%5C%5Cserver%5Cshare/program/content",
	} {
		response := request(server, http.MethodGet, attack)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "passwd") {
			t.Fatalf("route attack %q = %d %s", attack, response.Code, response.Body.String())
		}
	}
}

func TestHTTPCatalogIdempotentCreateAndStableConflicts(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	payload := map[string]any{"folderId": nil, "name": "Повтор", "description": ""}
	first := h.jsonMutation(t, http.MethodPost, "/api/v1/catalog/setups", "same-create", payload)
	second := h.jsonMutation(t, http.MethodPost, "/api/v1/catalog/setups", "same-create", payload)
	requireHTTPStatus(t, first, http.StatusCreated)
	requireHTTPStatus(t, second, http.StatusCreated)
	a := decodeHTTPJSON[domain.CatalogSetup](t, first)
	b := decodeHTTPJSON[domain.CatalogSetup](t, second)
	if a.ID != b.ID || a.Revision != b.Revision {
		t.Fatalf("idempotent replay differs: %+v %+v", a, b)
	}
	changed := h.jsonMutation(t, http.MethodPost, "/api/v1/catalog/setups", "same-create", map[string]any{
		"folderId": nil, "name": "Другое", "description": "",
	})
	requireHTTPStatus(t, changed, http.StatusConflict)
	if !strings.Contains(changed.Body.String(), string(domain.CodeIdempotencyConflict)) {
		t.Fatalf("idempotency error = %s", changed.Body.String())
	}

	stale := h.jsonMutation(t, http.MethodPatch, "/api/v1/catalog/setups/"+a.ID, "stale-update", map[string]any{
		"expectedRevision": 99, "description": "stale",
	})
	requireHTTPStatus(t, stale, http.StatusConflict)
	if !strings.Contains(stale.Body.String(), string(domain.CodeRevisionConflict)) {
		t.Fatalf("revision error = %s", stale.Body.String())
	}
}

// Ensure helper additions above do not accidentally rely on whole-body reads
// in upload routes: an unknown length must retain the service's streaming -1
// size contract.
func TestHTTPCatalogUnknownLengthUpload(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	setup := createCatalogSetupHTTP(t, h, "unknown-size-setup", "", "Поток")
	request := httptest.NewRequest(http.MethodPut,
		"http://127.0.0.1:8080/api/v1/catalog/setups/"+setup.ID+"/program?expectedRevision="+revisionString(setup.Revision),
		io.NopCloser(strings.NewReader("G21\nM2\n")))
	request.ContentLength = -1
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	request.Header.Set("X-CSRF-Token", h.server.csrfToken)
	request.Header.Set("Idempotency-Key", "unknown-size-put")
	request.Header.Set("X-File-Name", "stream.ngc")
	request.Header.Set("If-None-Match", "*")
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, request)
	requireHTTPStatus(t, response, http.StatusOK)

	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["program"] == nil {
		t.Fatalf("stream upload response = %s, err=%v", response.Body.String(), err)
	}
}
