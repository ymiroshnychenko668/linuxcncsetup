//go:build linux

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/service"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/storage"
	"golang.org/x/sys/unix"
)

type httpAcceptanceHarness struct {
	server     *Server
	service    *service.Service
	database   *database.DB
	roots      *storage.Roots
	store      *storage.Store
	catalog    *storage.CatalogStore
	libraryDir string
	programDir string
	rootDir    string
}

func newHTTPAcceptanceHarness(t *testing.T) *httpAcceptanceHarness {
	t.Helper()
	ctx := context.Background()
	// Keep this path short enough to create a Unix-domain socket at a
	// canonical content-addressed object name (sockaddr_un is limited to 108
	// bytes on Linux).
	rootDir, err := os.MkdirTemp("", "w-")
	if err != nil {
		t.Fatal(err)
	}
	libraryDir := filepath.Join(rootDir, "lib")
	stateDir := filepath.Join(rootDir, "state")
	programDir := filepath.Join(rootDir, "programs")
	if err := os.Mkdir(libraryDir, 0o750); err != nil {
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	if err := os.Mkdir(programDir, 0o750); err != nil {
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	roots, err := storage.NewRoots(libraryDir, stateDir, 0o640)
	if err != nil {
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	db, err := database.Open(ctx, stateDir)
	if err != nil {
		_ = roots.Close()
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	if err := db.EnsureLibrary(ctx, roots.LibraryID(), roots.LibraryFingerprint()); err != nil {
		_ = db.Close()
		_ = roots.Close()
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	store, err := storage.NewStore(roots, storage.StoreOptions{})
	if err != nil {
		_ = db.Close()
		_ = roots.Close()
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	catalog, err := storage.NewCatalogStore(programDir, store, 0o640)
	if err != nil {
		_ = db.Close()
		_ = roots.Close()
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	manager, err := service.New(service.Options{
		Database: db, Objects: store, Catalog: catalog, LibraryID: roots.LibraryID(),
		CatalogRootLabel: "Программы LinuxCNC", CatalogRootDisplay: "~/linuxcnc/nc_files",
		GCodeExtensions: []string{".ngc", ".nc"}, RecentLimit: 30,
	})
	if err != nil {
		_ = catalog.Close()
		_ = db.Close()
		_ = roots.Close()
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><main>Setup library</main>")},
	}
	server, err := NewWithService(Config{
		ListenAddress: "127.0.0.1:8080", LibraryID: roots.LibraryID(),
		LibraryAlias: "Acceptance", GCodeExtensions: []string{".ngc", ".nc"},
		EnableLegacyAPI: true,
	}, CheckFunc(db.Ping), CheckFunc(func(context.Context) error { return roots.Check() }), manager, static, nil)
	if err != nil {
		manager.Close()
		_ = catalog.Close()
		_ = db.Close()
		_ = roots.Close()
		_ = os.RemoveAll(rootDir)
		t.Fatal(err)
	}
	h := &httpAcceptanceHarness{
		server: server, service: manager, database: db, roots: roots, store: store, catalog: catalog,
		libraryDir: libraryDir, programDir: programDir, rootDir: rootDir,
	}
	t.Cleanup(func() {
		manager.Close()
		if err := catalog.Close(); err != nil {
			t.Errorf("close catalog: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Errorf("close acceptance database: %v", err)
		}
		if err := roots.Close(); err != nil {
			t.Errorf("close acceptance roots: %v", err)
		}
		if err := os.RemoveAll(rootDir); err != nil {
			t.Errorf("remove acceptance root: %v", err)
		}
	})
	return h
}

func (h *httpAcceptanceHarness) request(method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:8080"+target, body)
	req.Host = "127.0.0.1:8080"
	if isMutation(method) {
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		req.Header.Set("X-CSRF-Token", h.server.csrfToken)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, req)
	return response
}

func (h *httpAcceptanceHarness) jsonMutation(t *testing.T, method, target, key string, value any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return h.request(method, target, bytes.NewReader(payload), map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": key,
	})
}

func decodeHTTPJSON[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode status %d response %q: %v", response.Code, response.Body.String(), err)
	}
	return result
}

func requireHTTPStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, want, response.Body.String())
	}
}

func TestHTTPSetupNameCheckUsesBackendUnicodeSemantics(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	created := h.jsonMutation(t, http.MethodPost, "/api/v1/setups", "name-check-create", map[string]string{"name": "ς"})
	requireHTTPStatus(t, created, http.StatusCreated)
	want := decodeHTTPJSON[domain.Setup](t, created)

	response := h.request(http.MethodGet, "/api/v1/setups/name-check?name="+url.QueryEscape("Σ"), nil, nil)
	requireHTTPStatus(t, response, http.StatusOK)
	result := decodeHTTPJSON[struct {
		Match *service.SetupNameMatch `json:"match"`
	}](t, response)
	if result.Match == nil || result.Match.SetupID != want.ID || result.Match.Name != "ς" {
		t.Fatalf("name match = %+v", result.Match)
	}
}

func TestHTTPJobsCollectionRejectsUnsupportedMethods(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	response := h.request(http.MethodPost, "/api/v1/jobs", nil, nil)
	requireHTTPStatus(t, response, http.StatusMethodNotAllowed)
	if response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow = %q", response.Header().Get("Allow"))
	}
}

func (h *httpAcceptanceHarness) createSetupWithProgram(t *testing.T, suffix string, content []byte) *domain.Setup {
	t.Helper()
	createdResponse := h.jsonMutation(t, http.MethodPost, "/api/v1/setups", "create-"+suffix, map[string]string{
		"name": "Setup " + suffix,
	})
	requireHTTPStatus(t, createdResponse, http.StatusCreated)
	created := decodeHTTPJSON[domain.Setup](t, createdResponse)
	name := "main-" + suffix + ".ngc"
	prepare := h.jsonMutation(t, http.MethodPost, "/api/v1/setups/"+created.ID+"/upload-jobs", "prepare-upload-"+suffix, map[string]any{
		"operation": "addPrograms", "expectedRevision": created.Revision,
		"items": []map[string]any{{"displayName": name, "size": len(content)}},
	})
	requireHTTPStatus(t, prepare, http.StatusCreated)
	job := decodeHTTPJSON[domain.Job](t, prepare)
	payload, contentType := makeProgramMultipart(t, []string{name}, [][]byte{content})
	uploadResponse := h.request(http.MethodPost, "/api/v1/jobs/"+job.ID+"/upload", payload, map[string]string{
		"Idempotency-Key": "prepare-upload-" + suffix, "Content-Type": contentType,
	})
	requireHTTPStatus(t, uploadResponse, http.StatusOK)
	terminal := decodeHTTPJSON[domain.Job](t, uploadResponse)
	if terminal.State != domain.JobStateSucceeded {
		t.Fatalf("upload job = %+v", terminal)
	}
	setup, err := h.service.GetSetup(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return setup
}

func ptr[T any](value T) *T { return &value }

func makeProgramMultipart(t *testing.T, names []string, contents [][]byte) (*bytes.Buffer, string) {
	t.Helper()
	if len(names) != len(contents) {
		t.Fatal("multipart test input lengths differ")
	}
	manifest := programUploadManifest{Programs: make([]programUploadManifestItem, len(names))}
	for index := range names {
		size := int64(len(contents[index]))
		manifest.Programs[index] = programUploadManifestItem{DisplayName: names[index], Size: &size}
	}
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	manifestPart, err := writer.CreateFormField("manifest")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(manifestPart).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	for index, content := range contents {
		// This intentionally looks like a browser-supplied path. The endpoint
		// must ignore it and persist only the confirmed manifest basename.
		part, err := writer.CreateFormFile("program", `C:\fakepath\untrusted-`+strconv.Itoa(index)+`.ngc`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return payload, writer.FormDataContentType()
}

func TestHTTPAddMultipleProgramsUsesConfirmedManifestNamesAtomically(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	createdResponse := h.jsonMutation(t, http.MethodPost, "/api/v1/setups", "create-multipart", map[string]string{
		"name": "Multipart Setup",
	})
	requireHTTPStatus(t, createdResponse, http.StatusCreated)
	created := decodeHTTPJSON[domain.Setup](t, createdResponse)
	legacy := h.request(http.MethodPost,
		fmt.Sprintf("/api/v1/setups/%s/programs?expectedRevision=%d&name=legacy.ngc", created.ID, created.Revision),
		bytes.NewReader([]byte("G0 X0\n")), map[string]string{"Content-Type": "application/octet-stream", "Idempotency-Key": "legacy-direct-upload"})
	requireHTTPStatus(t, legacy, http.StatusConflict)
	legacyError := decodeHTTPJSON[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, legacy)
	if legacyError.Error.Code != string(domain.CodeUploadJobRequired) {
		t.Fatalf("legacy upload code = %q", legacyError.Error.Code)
	}
	payload, contentType := makeProgramMultipart(t,
		[]string{"roughing.ngc", "finishing.ngc"},
		[][]byte{[]byte("G90\nG0 X0\nM2\n"), []byte("G90\nG1 X1\nM2\n")},
	)
	prepare := h.jsonMutation(t, http.MethodPost, "/api/v1/setups/"+created.ID+"/upload-jobs", "prepare-multipart", map[string]any{
		"operation": "addPrograms", "expectedRevision": created.Revision,
		"items": []map[string]any{{"displayName": "roughing.ngc", "size": len("G90\nG0 X0\nM2\n")}, {"displayName": "finishing.ngc", "size": len("G90\nG1 X1\nM2\n")}},
	})
	requireHTTPStatus(t, prepare, http.StatusCreated)
	job := decodeHTTPJSON[domain.Job](t, prepare)
	activeResponse := h.request(http.MethodGet, "/api/v1/jobs?active=true&setupId="+created.ID+"&limit=1", nil, nil)
	requireHTTPStatus(t, activeResponse, http.StatusOK)
	activeJobs := decodeHTTPJSON[struct {
		Items []domain.Job `json:"items"`
	}](t, activeResponse)
	if len(activeJobs.Items) != 1 || activeJobs.Items[0].ID != job.ID {
		t.Fatalf("active jobs = %+v", activeJobs.Items)
	}
	response := h.request(http.MethodPost, "/api/v1/jobs/"+job.ID+"/upload",
		payload, map[string]string{"Content-Type": contentType, "Idempotency-Key": "prepare-multipart"})
	requireHTTPStatus(t, response, http.StatusOK)
	terminal := decodeHTTPJSON[domain.Job](t, response)
	if terminal.State != domain.JobStateSucceeded {
		t.Fatalf("multipart job = %+v", terminal)
	}
	replayPayload, replayType := makeProgramMultipart(t,
		[]string{"roughing.ngc", "finishing.ngc"},
		[][]byte{[]byte("G90\nG0 X0\nM2\n"), []byte("G90\nG1 X1\nM2\n")},
	)
	replayResponse := h.request(http.MethodPost, "/api/v1/jobs/"+job.ID+"/upload", replayPayload,
		map[string]string{"Content-Type": replayType, "Idempotency-Key": "prepare-multipart"})
	requireHTTPStatus(t, replayResponse, http.StatusOK)
	if replayJob := decodeHTTPJSON[domain.Job](t, replayResponse); replayJob.ID != job.ID || replayJob.State != domain.JobStateSucceeded {
		t.Fatalf("upload replay = %+v", replayJob)
	}
	alteredPayload, alteredType := makeProgramMultipart(t,
		[]string{"roughing.ngc", "finishing.ngc"},
		[][]byte{[]byte("G90\nG0 X9\nM2\n"), []byte("G90\nG1 X1\nM2\n")},
	)
	alteredResponse := h.request(http.MethodPost, "/api/v1/jobs/"+job.ID+"/upload", alteredPayload,
		map[string]string{"Content-Type": alteredType, "Idempotency-Key": "prepare-multipart"})
	requireHTTPStatus(t, alteredResponse, http.StatusConflict)
	alteredError := decodeHTTPJSON[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, alteredResponse)
	if alteredError.Error.Code != string(domain.CodeIdempotencyConflict) {
		t.Fatalf("altered replay code = %q", alteredError.Error.Code)
	}
	updated, err := h.service.GetSetup(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != created.Revision+1 || len(updated.Artifacts) != 2 {
		t.Fatalf("multipart setup = %+v", updated)
	}
	for index, want := range []string{"roughing.ngc", "finishing.ngc"} {
		if updated.Artifacts[index].DisplayName != want || strings.Contains(updated.Artifacts[index].DisplayName, "fakepath") {
			t.Fatalf("artifact %d name = %q, want %q", index, updated.Artifacts[index].DisplayName, want)
		}
	}
	selectedResponse := h.jsonMutation(t, http.MethodPatch,
		fmt.Sprintf("/api/v1/setups/%s/programs/%s", updated.ID, updated.Artifacts[0].ID),
		"select-primary-multipart", map[string]any{
			"expectedRevision": updated.Revision,
			"expectedVersion":  updated.Artifacts[0].Version,
			"primary":          true,
		})
	requireHTTPStatus(t, selectedResponse, http.StatusOK)
	selected := decodeHTTPJSON[domain.Setup](t, selectedResponse)
	if !selected.Artifacts[0].Primary || selected.Artifacts[1].Primary {
		t.Fatalf("unexpected primary selection: %+v", selected.Artifacts)
	}

	// Deleting the primary cannot silently choose another program.
	primary := selected.Artifacts[0]
	rejected := h.request(http.MethodDelete,
		fmt.Sprintf("/api/v1/setups/%s/programs/%s?expectedRevision=%d&expectedVersion=%s",
			selected.ID, primary.ID, selected.Revision, url.QueryEscape(primary.Version)),
		nil, map[string]string{"Idempotency-Key": "delete-primary-rejected"})
	if rejected.Code == http.StatusOK {
		t.Fatalf("ambiguous primary deletion unexpectedly succeeded: %s", rejected.Body.String())
	}
	accepted := h.request(http.MethodDelete,
		fmt.Sprintf("/api/v1/setups/%s/programs/%s?expectedRevision=%d&expectedVersion=%s&replacementPrimaryArtifactId=%s",
			selected.ID, primary.ID, selected.Revision, url.QueryEscape(primary.Version), selected.Artifacts[1].ID),
		nil, map[string]string{"Idempotency-Key": "delete-primary-explicit"})
	requireHTTPStatus(t, accepted, http.StatusOK)
	afterDelete := decodeHTTPJSON[domain.Setup](t, accepted)
	if len(afterDelete.Artifacts) != 1 || !afterDelete.Artifacts[0].Primary {
		t.Fatalf("explicit primary replacement = %+v", afterDelete.Artifacts)
	}
}

func TestHTTPImportPreflightReportsUnicodeFoldCollisionsBeforeStreaming(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	response := h.jsonMutation(t, http.MethodPost, "/api/v1/setup-imports/preflight", "unused-read-only-key", map[string]any{
		"items": []map[string]any{
			{"clientId": "ss-a", "role": "program", "displayName": "Straße.ngc"},
			{"clientId": "ss-b", "role": "program", "displayName": "STRASSE.NGC"},
			{"clientId": "sigma-a", "role": "program", "displayName": "part-Σ.ngc"},
			{"clientId": "sigma-b", "role": "program", "displayName": "part-ς.NGC"},
		},
	})
	requireHTTPStatus(t, response, http.StatusOK)
	result := decodeHTTPJSON[service.ImportPreflightResult](t, response)
	if len(result.Collisions) != 2 {
		t.Fatalf("preflight collisions = %+v", result.Collisions)
	}
	var sessions int
	if err := h.database.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM import_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("preflight created staging state: count=%d err=%v", sessions, err)
	}
}

func TestHTTPPreparedUploadJobCanBeCancelledWithoutPartialRevision(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	setup := h.createSetupWithProgram(t, "cancel-job", []byte("G90\nM2\n"))
	artifact := setup.Artifacts[0]
	for label, target := range map[string]string{
		"replace": fmt.Sprintf("/api/v1/setups/%s/programs/%s?expectedRevision=%d&expectedVersion=%s&name=%s", setup.ID, artifact.ID, setup.Revision, url.QueryEscape(artifact.Version), url.QueryEscape(artifact.DisplayName)),
		"sheet":   fmt.Sprintf("/api/v1/setups/%s/setup-sheet?expectedRevision=%d&name=sheet.pdf", setup.ID, setup.Revision),
	} {
		legacy := h.request(http.MethodPut, target, bytes.NewReader([]byte("legacy")), map[string]string{"Content-Type": "application/octet-stream", "Idempotency-Key": "legacy-" + label})
		requireHTTPStatus(t, legacy, http.StatusConflict)
		payload := decodeHTTPJSON[struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}](t, legacy)
		if payload.Error.Code != string(domain.CodeUploadJobRequired) {
			t.Fatalf("legacy %s code = %q", label, payload.Error.Code)
		}
	}
	content := []byte("G90\nG1 X999\nM2\n")
	prepare := h.jsonMutation(t, http.MethodPost, "/api/v1/setups/"+setup.ID+"/upload-jobs", "prepare-cancel-http", map[string]any{
		"operation": "replaceProgram", "expectedRevision": setup.Revision,
		"artifactId": artifact.ID, "expectedVersion": artifact.Version,
		"items": []map[string]any{{"displayName": artifact.DisplayName, "size": len(content)}},
	})
	requireHTTPStatus(t, prepare, http.StatusCreated)
	job := decodeHTTPJSON[domain.Job](t, prepare)
	reader, writer := io.Pipe()
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/jobs/"+job.ID+"/upload", reader)
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		req.Header.Set("X-CSRF-Token", h.server.csrfToken)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Idempotency-Key", "prepare-cancel-http")
		h.server.ServeHTTP(response, req)
	}()
	if _, err := writer.Write(content[:4]); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		active, err := h.service.GetJob(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if active.State == domain.JobStateRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not start: %+v", active)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancelled := h.request(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil, map[string]string{"Idempotency-Key": "cancel-http-upload"})
	requireHTTPStatus(t, cancelled, http.StatusOK)
	_ = writer.CloseWithError(context.Canceled)
	<-done
	terminal, err := h.service.GetJob(context.Background(), job.ID)
	if err != nil || terminal.State != domain.JobStateCancelled {
		t.Fatalf("terminal upload = %+v, %v; response=%s", terminal, err, response.Body.String())
	}
	unchanged, err := h.service.GetSetup(context.Background(), setup.ID)
	if err != nil || unchanged.Revision != setup.Revision || unchanged.Artifacts[0].Version != artifact.Version {
		t.Fatalf("partial replacement published: %+v, %v", unchanged, err)
	}
}

func TestHTTPAcceptanceCreateUploadValidateCurrentAndRangeETag(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	program := []byte("G90\nG0 X0 Y0\nM2\n")
	setup := h.createSetupWithProgram(t, "workflow", program)
	if setup.Status != domain.SetupStatusDraft || len(setup.Artifacts) != 1 || setup.Artifacts[0].SetupID != setup.ID {
		t.Fatalf("uploaded setup = %+v", setup)
	}
	artifact := setup.Artifacts[0]

	validateResponse := h.jsonMutation(t, http.MethodPost,
		"/api/v1/setups/"+setup.ID+"/validate", "validate-workflow",
		map[string]any{"expectedRevision": setup.Revision})
	requireHTTPStatus(t, validateResponse, http.StatusAccepted)
	job := decodeHTTPJSON[domain.Job](t, validateResponse)
	if job.Kind != domain.JobKindValidate || job.SetupID != setup.ID ||
		validateResponse.Header().Get("Location") != "/api/v1/jobs/"+job.ID {
		t.Fatalf("validation job = %+v, location=%q", job, validateResponse.Header().Get("Location"))
	}
	deadline := time.Now().Add(10 * time.Second)
	for !job.State.Terminal() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		jobResponse := h.request(http.MethodGet, "/api/v1/jobs/"+job.ID, nil, nil)
		requireHTTPStatus(t, jobResponse, http.StatusOK)
		job = decodeHTTPJSON[domain.Job](t, jobResponse)
	}
	if job.State != domain.JobStateSucceeded {
		t.Fatalf("validation terminal job = %+v", job)
	}

	setupResponse := h.request(http.MethodGet, "/api/v1/setups/"+setup.ID, nil, nil)
	requireHTTPStatus(t, setupResponse, http.StatusOK)
	ready := decodeHTTPJSON[domain.Setup](t, setupResponse)
	if ready.Status != domain.SetupStatusReady || ready.Revision != setup.Revision {
		t.Fatalf("validated setup = %+v", ready)
	}
	currentResponse := h.jsonMutation(t, http.MethodPut, "/api/v1/current-setup", "current-workflow", map[string]any{
		"setupId": setup.ID, "expectedRevision": ready.Revision, "confirmed": true,
	})
	requireHTTPStatus(t, currentResponse, http.StatusOK)
	current := decodeHTTPJSON[domain.CurrentSetup](t, currentResponse)
	if current.SetupID != setup.ID || current.RevisionSelected != ready.Revision {
		t.Fatalf("current setup = %+v", current)
	}

	contentPath := fmt.Sprintf("/api/v1/setups/%s/programs/%s/content", setup.ID, artifact.ID)
	head := h.request(http.MethodHead, contentPath, nil, nil)
	requireHTTPStatus(t, head, http.StatusOK)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(program)) ||
		head.Header().Get("Accept-Ranges") != "bytes" || head.Header().Get("ETag") == "" {
		t.Fatalf("HEAD headers=%v body=%q", head.Header(), head.Body.String())
	}
	etag := head.Header().Get("ETag")
	block := h.request(http.MethodGet, contentPath, nil, map[string]string{
		"Range": "bytes=0-3", "If-Match": etag,
	})
	requireHTTPStatus(t, block, http.StatusPartialContent)
	if block.Body.String() != "G90\n" || block.Header().Get("ETag") != etag ||
		block.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-3/%d", len(program)) {
		t.Fatalf("Range headers=%v body=%q", block.Header(), block.Body.String())
	}

	var currentAudit int
	if err := h.database.SQL().QueryRowContext(context.Background(), `
		SELECT count(*) FROM audit_events
		 WHERE setup_id = ? AND operation = 'selectCurrent' AND result = 'succeeded'`, setup.ID).Scan(&currentAudit); err != nil {
		t.Fatal(err)
	}
	if currentAudit != 1 {
		t.Fatalf("current-selection audit count = %d", currentAudit)
	}
	for _, leaked := range []string{h.libraryDir, "storage_key", "objects/"} {
		if strings.Contains(setupResponse.Body.String(), leaked) || strings.Contains(currentResponse.Body.String(), leaked) {
			t.Fatalf("public response leaked internal storage marker %q", leaked)
		}
	}
}

func TestHTTPContentWithoutRangeStreamsTheCompleteLargeProgram(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	program := bytes.Repeat([]byte("G1 X123 Y456\n"), int(service.MaxContentRangeBytes/13)+17)
	if int64(len(program)) <= service.MaxContentRangeBytes {
		t.Fatalf("fixture size = %d, want more than one service block", len(program))
	}
	setup := h.createSetupWithProgram(t, "stream-complete", program)
	artifact := setup.Artifacts[0]
	response := h.request(http.MethodGet,
		fmt.Sprintf("/api/v1/setups/%s/programs/%s/content", setup.ID, artifact.ID), nil, nil)
	requireHTTPStatus(t, response, http.StatusOK)
	if response.Header().Get("Content-Range") != "" ||
		response.Header().Get("Content-Length") != strconv.Itoa(len(program)) ||
		!bytes.Equal(response.Body.Bytes(), program) {
		t.Fatalf("streamed response length=%d headers=%v", response.Body.Len(), response.Header())
	}
}

func TestHTTPPathAttackMatrixCannotReadExternalSentinel(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	left := h.createSetupWithProgram(t, "left", []byte("G0 X1\n"))
	right := h.createSetupWithProgram(t, "right", []byte("G0 X2\n"))
	leftArtifact, rightArtifact := left.Artifacts[0], right.Artifacts[0]
	sentinelPath := filepath.Join(h.rootDir, "outside-sentinel")
	const sentinel = "EXTERNAL-SENTINEL-DO-NOT-READ"
	if err := os.WriteFile(sentinelPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	paths := map[string]string{
		"foreign artifact ID": fmt.Sprintf("/api/v1/setups/%s/programs/%s/content", left.ID, rightArtifact.ID),
		"foreign setup ID":    fmt.Sprintf("/api/v1/setups/%s/programs/%s/content", right.ID, leftArtifact.ID),
		"dot dot":             fmt.Sprintf("/api/v1/setups/../programs/%s/content", leftArtifact.ID),
		"single encoded":      fmt.Sprintf("/api/v1/setups/%%2e%%2e/programs/%s/content", leftArtifact.ID),
		"double encoded":      fmt.Sprintf("/api/v1/setups/%%252e%%252e/programs/%s/content", leftArtifact.ID),
		"absolute POSIX":      fmt.Sprintf("/api/v1/setups/%%2Fetc%%2Fpasswd/programs/%s/content", leftArtifact.ID),
		"UNC path":            fmt.Sprintf("/api/v1/setups/%%5C%%5Cserver%%5Cshare/programs/%s/content", leftArtifact.ID),
		"NUL":                 fmt.Sprintf("/api/v1/setups/%%00%s/programs/%s/content", left.ID, leftArtifact.ID),
		"artifact traversal":  fmt.Sprintf("/api/v1/setups/%s/programs/%%2e%%2e/content", left.ID),
	}
	for name, target := range paths {
		t.Run(name, func(t *testing.T) {
			response := h.request(http.MethodGet, target, nil, nil)
			if response.Code < 400 || response.Code >= 500 {
				t.Fatalf("attack status = %d, body=%s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			for _, forbidden := range []string{sentinel, sentinelPath, h.libraryDir, "storage_key", "objects/"} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("attack response leaked %q: %s", forbidden, body)
				}
			}
		})
	}
	contents, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != sentinel {
		t.Fatalf("sentinel changed to %q", contents)
	}
}

func TestHTTPRejectsSymlinkFIFOAndSocketWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "socket"} {
		t.Run(kind, func(t *testing.T) {
			h := newHTTPAcceptanceHarness(t)
			setup := h.createSetupWithProgram(t, kind, []byte("G0 X0\n"))
			artifact := setup.Artifacts[0]
			var storageKey string
			if err := h.database.SQL().QueryRowContext(context.Background(), `
				SELECT o.storage_key FROM storage_objects o
				JOIN setup_artifacts a ON a.storage_object_id = o.id
				WHERE a.id = ? AND a.setup_id = ?`, artifact.ID, setup.ID).Scan(&storageKey); err != nil {
				t.Fatal(err)
			}
			objectPath := filepath.Join(h.libraryDir, filepath.FromSlash(storageKey))
			if err := os.Remove(objectPath); err != nil {
				t.Fatal(err)
			}
			sentinelPath := filepath.Join(h.rootDir, "external-sentinel")
			const sentinel = "SPECIAL-FILE-SENTINEL"
			if err := os.WriteFile(sentinelPath, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			var socket net.Listener
			switch kind {
			case "symlink":
				if err := os.Symlink(sentinelPath, objectPath); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(objectPath, 0o600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: objectPath, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				socket = listener
				defer socket.Close()
			}

			target := fmt.Sprintf("/api/v1/setups/%s/programs/%s/content", setup.ID, artifact.ID)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- h.request(http.MethodGet, target, nil, nil) }()
			var response *httptest.ResponseRecorder
			select {
			case response = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("special-file request blocked the backend")
			}
			if response.Code != http.StatusConflict && response.Code != http.StatusServiceUnavailable {
				t.Fatalf("special-file status = %d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), sentinel) || strings.Contains(response.Body.String(), sentinelPath) ||
				strings.Contains(response.Body.String(), storageKey) {
				t.Fatalf("special-file response leaked protected data: %s", response.Body.String())
			}
			// The same attack must produce the same public status/error envelope.
			repeated := h.request(http.MethodGet, target, nil, nil)
			if repeated.Code != response.Code {
				t.Fatalf("unstable special-file status: first=%d repeated=%d", response.Code, repeated.Code)
			}
			var firstError, secondError struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &firstError); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(repeated.Body.Bytes(), &secondError); err != nil {
				t.Fatal(err)
			}
			if firstError.Error.Code == "" || firstError.Error.Code != secondError.Error.Code {
				t.Fatalf("unstable error codes: first=%q repeated=%q", firstError.Error.Code, secondError.Error.Code)
			}
			changed, err := h.service.GetSetup(context.Background(), setup.ID)
			if err != nil {
				t.Fatal(err)
			}
			if changed.Status != domain.SetupStatusAttention {
				t.Fatalf("setup status after %s = %s", kind, changed.Status)
			}
			contents, err := os.ReadFile(sentinelPath)
			if err != nil || string(contents) != sentinel {
				t.Fatalf("sentinel after %s = %q, %v", kind, contents, err)
			}
		})
	}
}

func TestHTTPSparseTenGiBHeadAndRangeUseBoundedMemory(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	const logicalSize int64 = 10 << 30
	setup, artifact, objectPath := h.registerSparseProgram(t, logicalSize)
	var stat unix.Stat_t
	if err := unix.Stat(objectPath, &stat); err != nil {
		t.Fatal(err)
	}
	if allocated := stat.Blocks * 512; allocated >= logicalSize/1024 {
		t.Fatalf("fixture is not sparse: logical=%d allocated=%d", logicalSize, allocated)
	}
	target := fmt.Sprintf("/api/v1/setups/%s/programs/%s/content", setup.ID, artifact.ID)
	head := h.request(http.MethodHead, target, nil, nil)
	requireHTTPStatus(t, head, http.StatusOK)
	if head.Header().Get("Content-Length") != strconv.FormatInt(logicalSize, 10) || head.Body.Len() != 0 {
		t.Fatalf("sparse HEAD headers=%v body bytes=%d", head.Header(), head.Body.Len())
	}
	etag := head.Header().Get("ETag")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	first := h.request(http.MethodGet, target, nil, map[string]string{
		"Range": "bytes=0-4095", "If-Match": etag,
	})
	requireHTTPStatus(t, first, http.StatusPartialContent)
	last := h.request(http.MethodGet, target, nil, map[string]string{
		"Range": fmt.Sprintf("bytes=%d-%d", logicalSize-3, logicalSize-1), "If-Match": etag,
	})
	requireHTTPStatus(t, last, http.StatusPartialContent)
	runtime.ReadMemStats(&after)
	if first.Body.Len() != 4096 || !bytes.Equal(first.Body.Bytes()[:4], []byte("G90\n")) || last.Body.String() != "M2\n" {
		t.Fatalf("sparse ranges: first length=%d prefix=%q last=%q", first.Body.Len(), first.Body.Bytes()[:min(4, first.Body.Len())], last.Body.String())
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("bounded ranges allocated %d bytes for a %d-byte object", allocated, logicalSize)
	}
}

func (h *httpAcceptanceHarness) registerSparseProgram(t *testing.T, size int64) (*domain.Setup, domain.Artifact, string) {
	t.Helper()
	ctx := context.Background()
	setup, err := h.service.CreateSetup(ctx, service.CreateSetupInput{Name: "Sparse 10 GiB", IdempotencyKey: "sparse-create"})
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("c", 64)
	key, err := h.store.ObjectKeyForSHA(sha)
	if err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(h.libraryDir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o750); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(objectPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	closeFile := true
	defer func() {
		if closeFile {
			_ = file.Close()
		}
	}()
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("G90\n"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("M2\n"), size-3); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	closeFile = false
	object, err := h.store.InspectObject(key, sha, "")
	if err != nil {
		t.Fatal(err)
	}
	objectID, err := domain.NewStorageObjectID()
	if err != nil {
		t.Fatal(err)
	}
	artifactID, err := domain.NewArtifactID()
	if err != nil {
		t.Fatal(err)
	}
	name := "huge.ngc"
	nameKey, err := domain.ArtifactNameKey(name)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := h.database.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO storage_objects(id, library_id, storage_key, media_type, byte_size, sha256)
		VALUES (?, ?, ?, 'text/x-gcode', ?, ?)`, objectID, h.roots.LibraryID(), key, size, sha); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO setup_artifacts(
			id, setup_id, role, display_name, normalized_name, storage_object_id,
			position, is_primary, identity_device, identity_inode, identity_size,
			identity_mtime_ns, identity_ctime_ns, object_version
		) VALUES (?, ?, 'program', ?, ?, ?, 0, 1, ?, ?, ?, ?, ?, ?)`,
		artifactID, setup.ID, name, nameKey, objectID, int64(object.Identity.Device), int64(object.Identity.Inode),
		object.Size, object.Identity.ModTimeNS, object.Identity.ChangeTimeNS, object.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE setups SET revision = revision + 1 WHERE id = ?`, setup.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	loaded, err := h.service.GetSetup(ctx, setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, loaded.Artifacts[0], objectPath
}

func TestHTTPPreviewRejectsSameSizeSameMtimeReplacement(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	original := []byte("G0 X111\nM2\n")
	replacement := []byte("G0 X999\nM2\n")
	if len(original) != len(replacement) {
		t.Fatal("fixture lengths differ")
	}
	setup := h.createSetupWithProgram(t, "identity", original)
	artifact := setup.Artifacts[0]
	var storageKey string
	if err := h.database.SQL().QueryRowContext(context.Background(), `
		SELECT o.storage_key FROM storage_objects o
		JOIN setup_artifacts a ON a.storage_object_id = o.id
		WHERE a.id = ?`, artifact.ID).Scan(&storageKey); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(h.libraryDir, filepath.FromSlash(storageKey))
	var before unix.Stat_t
	if err := unix.Stat(objectPath, &before); err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("/api/v1/setups/%s/programs/%s/content", setup.ID, artifact.ID)
	first := h.request(http.MethodGet, target, nil, map[string]string{"Range": "bytes=0-5"})
	requireHTTPStatus(t, first, http.StatusPartialContent)
	etag := first.Header().Get("ETag")

	temporary := objectPath + ".external-replacement"
	if err := os.WriteFile(temporary, replacement, 0o640); err != nil {
		t.Fatal(err)
	}
	times := []unix.Timespec{before.Atim, before.Mtim}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, temporary, times, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, objectPath); err != nil {
		t.Fatal(err)
	}
	var after unix.Stat_t
	if err := unix.Stat(objectPath, &after); err != nil {
		t.Fatal(err)
	}
	if before.Size != after.Size || before.Mtim != after.Mtim || before.Ino == after.Ino {
		t.Fatalf("replacement identity fixture invalid: before=%+v after=%+v", before, after)
	}

	second := h.request(http.MethodGet, target, nil, map[string]string{
		"Range": "bytes=6-11", "If-Match": etag,
	})
	if second.Code != http.StatusConflict {
		t.Fatalf("changed-object status=%d body=%s", second.Code, second.Body.String())
	}
	if strings.Contains(second.Body.String(), string(replacement)) || strings.Contains(second.Body.String(), storageKey) ||
		strings.Contains(second.Body.String(), objectPath) {
		t.Fatalf("changed-object response leaked content/path: %s", second.Body.String())
	}
	changed := decodeHTTPJSON[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, second)
	if changed.Error.Code != string(domain.CodeArtifactChanged) {
		t.Fatalf("changed-object code = %q", changed.Error.Code)
	}
	loaded, err := h.service.GetSetup(context.Background(), setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != domain.SetupStatusAttention || loaded.Artifacts[0].ID != artifact.ID {
		t.Fatalf("replacement was silently adopted: %+v", loaded)
	}
}

func TestHTTPHTMLSetupSheetIsSanitizedAndSandboxed(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	setup := h.createSetupWithProgram(t, "html", []byte("G0 X0\n"))
	malicious := []byte(`<!doctype html><html><head><meta http-equiv="refresh" content="0;url=https://evil.invalid"><script>fetch('/api/v1/setups')</script></head><body onload="steal()"><h1>Safe instructions</h1><form action="https://evil.invalid"><input value="secret"><p>hidden form text</p></form><iframe src="https://evil.invalid"></iframe><a href="javascript:steal()" onclick="steal()">blocked link</a><img src="https://evil.invalid/pixel"></body></html>`)
	prepare := h.jsonMutation(t, http.MethodPost, "/api/v1/setups/"+setup.ID+"/upload-jobs", "prepare-html-sheet", map[string]any{
		"operation": "putSetupSheet", "expectedRevision": setup.Revision,
		"items": []map[string]any{{"displayName": "sheet.html", "size": len(malicious)}},
	})
	requireHTTPStatus(t, prepare, http.StatusCreated)
	job := decodeHTTPJSON[domain.Job](t, prepare)
	upload := h.request(http.MethodPost, "/api/v1/jobs/"+job.ID+"/upload", bytes.NewReader(malicious), map[string]string{
		"Idempotency-Key": "prepare-html-sheet", "Content-Type": "application/octet-stream",
	})
	requireHTTPStatus(t, upload, http.StatusOK)
	terminal := decodeHTTPJSON[domain.Job](t, upload)
	if terminal.State != domain.JobStateSucceeded {
		t.Fatalf("sheet job = %+v", terminal)
	}
	withSheet, err := h.service.GetSetup(context.Background(), setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(withSheet.Artifacts) != 2 || withSheet.Artifacts[1].Role != domain.ArtifactRoleSetupSheet {
		t.Fatalf("setup sheet aggregate = %+v", withSheet)
	}

	viewer := h.request(http.MethodGet, "/api/v1/setups/"+setup.ID+"/setup-sheet/content", nil, nil)
	requireHTTPStatus(t, viewer, http.StatusOK)
	if viewer.Header().Get("Content-Security-Policy") != sanitizedHTMLCSP ||
		viewer.Header().Get("Content-Type") != "text/html; charset=utf-8" || viewer.Header().Get("ETag") == "" {
		t.Fatalf("HTML viewer headers = %v", viewer.Header())
	}
	output := strings.ToLower(viewer.Body.String())
	for _, forbidden := range []string{
		"<script", "fetch(", "<form", "<input", "hidden form text", "<iframe", "javascript:",
		"onclick", "onload", "evil.invalid", h.server.csrfToken,
	} {
		if strings.Contains(output, strings.ToLower(forbidden)) {
			t.Fatalf("HTML viewer contains %q: %s", forbidden, viewer.Body.String())
		}
	}
	if !strings.Contains(viewer.Body.String(), "Safe instructions") || !strings.Contains(viewer.Body.String(), "blocked link") {
		t.Fatalf("HTML viewer removed safe text: %s", viewer.Body.String())
	}

	stale := h.request(http.MethodGet, "/api/v1/setups/"+setup.ID+"/setup-sheet/content?version=stale", nil, nil)
	requireHTTPStatus(t, stale, http.StatusPreconditionFailed)
}

func TestHTTPHTMLSetupSheetStreamsBeyondFormerViewerLimit(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	setup := h.createSetupWithProgram(t, "large-html", []byte("G0 X0\n"))
	paragraph := []byte("<p>Safe setup instruction &amp; measurement.</p>")
	large := append([]byte("<!doctype html><html><body>"), bytes.Repeat(paragraph, (3<<20)/len(paragraph)+1)...)
	large = append(large, []byte("</body></html>")...)
	prepare := h.jsonMutation(t, http.MethodPost, "/api/v1/setups/"+setup.ID+"/upload-jobs", "prepare-large-html-sheet", map[string]any{
		"operation": "putSetupSheet", "expectedRevision": setup.Revision,
		"items": []map[string]any{{"displayName": "large-sheet.html", "size": len(large)}},
	})
	requireHTTPStatus(t, prepare, http.StatusCreated)
	job := decodeHTTPJSON[domain.Job](t, prepare)
	upload := h.request(http.MethodPost, "/api/v1/jobs/"+job.ID+"/upload", bytes.NewReader(large), map[string]string{
		"Idempotency-Key": "prepare-large-html-sheet", "Content-Type": "application/octet-stream",
	})
	requireHTTPStatus(t, upload, http.StatusOK)
	terminal := decodeHTTPJSON[domain.Job](t, upload)
	if terminal.State != domain.JobStateSucceeded {
		t.Fatalf("large HTML upload job = %+v", terminal)
	}

	withSheet, err := h.service.GetSetup(context.Background(), setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	var version string
	for _, artifact := range withSheet.Artifacts {
		if artifact.Role == domain.ArtifactRoleSetupSheet {
			version = artifact.Version
		}
	}
	if version == "" {
		t.Fatal("uploaded setup sheet was not published")
	}

	viewer := h.request(http.MethodGet, "/api/v1/setups/"+setup.ID+"/setup-sheet/content?version="+url.QueryEscape(version), nil, nil)
	requireHTTPStatus(t, viewer, http.StatusOK)
	if viewer.Body.Len() <= 2<<20 {
		t.Fatalf("streamed HTML viewer body = %d bytes", viewer.Body.Len())
	}
	if viewer.Header().Get("Content-Length") != "" {
		t.Fatalf("streamed HTML unexpectedly buffered with Content-Length %q", viewer.Header().Get("Content-Length"))
	}
	if !strings.Contains(viewer.Body.String()[:1024], "Safe setup instruction") {
		t.Fatal("streamed HTML viewer omitted safe content")
	}
}

func TestHTTPConcurrentStaleRevisionHasExactlyOneWinner(t *testing.T) {
	h := newHTTPAcceptanceHarness(t)
	created := h.jsonMutation(t, http.MethodPost, "/api/v1/setups", "race-create", map[string]string{"name": "Initial"})
	requireHTTPStatus(t, created, http.StatusCreated)
	setup := decodeHTTPJSON[domain.Setup](t, created)
	type outcome struct {
		name     string
		response *httptest.ResponseRecorder
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var ready sync.WaitGroup
	for index, name := range []string{"Window A", "Window B"} {
		index, name := index, name
		ready.Add(1)
		go func() {
			ready.Done()
			<-start
			response := h.jsonMutation(t, http.MethodPatch, "/api/v1/setups/"+setup.ID,
				"race-update-"+strconv.Itoa(index), map[string]any{
					"expectedRevision": setup.Revision, "name": name, "description": "preserve this input",
				})
			results <- outcome{name: name, response: response}
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	winners, conflicts := 0, 0
	winnerName := ""
	for _, result := range []outcome{first, second} {
		switch result.response.Code {
		case http.StatusOK:
			winners++
			winnerName = result.name
		case http.StatusConflict:
			conflicts++
			body := result.response.Body.String()
			if !strings.Contains(body, string(domain.CodeRevisionConflict)) || !strings.Contains(body, "revision") {
				t.Fatalf("conflict response = %s", body)
			}
		default:
			t.Fatalf("mutation status=%d body=%s", result.response.Code, result.response.Body.String())
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
	loaded, err := h.service.GetSetup(context.Background(), setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != setup.Revision+1 || loaded.Name != winnerName || loaded.Description != "preserve this input" {
		t.Fatalf("final setup = %+v, winning input=%q", loaded, winnerName)
	}
}

var _ fs.FS = fstest.MapFS{}
