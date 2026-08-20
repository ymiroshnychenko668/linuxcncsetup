package httpapi

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/service"
)

func (s *Server) routeContent(w http.ResponseWriter, r *http.Request, requestID string) bool {
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// api/v1/setups/{setup}/programs/{artifact}/content
	if len(segments) == 7 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "setups" &&
		segments[4] == "programs" && segments[6] == "content" {
		s.serveArtifactContent(w, r, requestID, segments[3], segments[5], domain.ArtifactRoleProgram)
		return true
	}
	// api/v1/setups/{setup}/setup-sheet/content
	if len(segments) == 6 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "setups" &&
		segments[4] == "setup-sheet" && segments[5] == "content" {
		s.serveSetupSheetContent(w, r, requestID, segments[3])
		return true
	}
	return false
}

func (s *Server) serveArtifactContent(w http.ResponseWriter, r *http.Request, requestID, setupID, artifactID string, requiredRole domain.ArtifactRole) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	if s.service == nil {
		writeError(w, http.StatusServiceUnavailable, requestID, "SERVICE_UNAVAILABLE", "The setup service is unavailable.", nil, true)
		return
	}
	metadata, err := s.service.InspectArtifactContent(r.Context(), setupID, artifactID)
	if err != nil {
		writeDomainError(w, requestID, err)
		return
	}
	setup, err := s.service.GetSetup(r.Context(), setupID)
	if err != nil {
		writeDomainError(w, requestID, err)
		return
	}
	roleOK := false
	for _, artifact := range setup.Artifacts {
		if artifact.ID == artifactID && artifact.Role == requiredRole {
			roleOK = true
			break
		}
	}
	if !roleOK {
		writeDomainError(w, requestID, domain.NewError(domain.CodeArtifactNotFound, "artifact was not found"))
		return
	}
	if !matchExpectedETag(r.Header.Get("If-Match"), metadata.ETag) {
		writeError(w, http.StatusPreconditionFailed, requestID, string(domain.CodeArtifactChanged), "Artifact version no longer matches.", nil, false)
		return
	}
	setContentHeaders(w.Header(), metadata)
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(metadata.ByteSize, 10))
		w.WriteHeader(http.StatusOK)
		return
	}
	selected, err := parseSingleRange(r.Header.Get("Range"), metadata.ByteSize, service.MaxContentRangeBytes)
	if err != nil {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(metadata.ByteSize, 10))
		writeError(w, http.StatusRequestedRangeNotSatisfiable, requestID, string(domain.CodeInvalidRange), "The requested byte range is not satisfiable.", nil, false)
		return
	}
	if metadata.ByteSize == 0 {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}
	// A request without Range is a normal streaming GET, not an implicit
	// preview block. Keep only one bounded block in memory while preserving the
	// version check on every read. Preview clients use explicit Range requests.
	if r.Header.Get("Range") == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(metadata.ByteSize, 10))
		for offset := int64(0); offset < metadata.ByteSize; {
			length := min(service.MaxContentRangeBytes, metadata.ByteSize-offset)
			block, readErr := s.service.ReadArtifactRange(
				r.Context(), setupID, artifactID, metadata.Version, offset, length,
			)
			if readErr != nil {
				// Once bytes were written the Content-Length makes a truncated
				// response detectable by the client; before that we can still
				// return the stable domain error envelope.
				if offset == 0 {
					w.Header().Del("Content-Length")
					writeDomainError(w, requestID, readErr)
				} else if recorder, ok := w.(interface{ setErrorCode(string) }); ok {
					recorder.setErrorCode("CONTENT_READ_FAILED")
				}
				return
			}
			if len(block.Data) == 0 {
				if recorder, ok := w.(interface{ setErrorCode(string) }); ok {
					recorder.setErrorCode("CONTENT_READ_FAILED")
				}
				return
			}
			written, writeErr := w.Write(block.Data)
			offset += int64(written)
			if writeErr != nil || written != len(block.Data) {
				return
			}
		}
		return
	}
	block, err := s.service.ReadArtifactRange(r.Context(), setupID, artifactID, metadata.Version, selected.start, selected.length)
	if err != nil {
		writeDomainError(w, requestID, err)
		return
	}
	partial := r.Header.Get("Range") != "" || selected.length != metadata.ByteSize
	if partial {
		w.Header().Set("Content-Range", contentRangeHeader(selected, metadata.ByteSize))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(block.Data)))
	status := http.StatusOK
	if partial {
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	_, _ = w.Write(block.Data)
}

func (s *Server) serveSetupSheetContent(w http.ResponseWriter, r *http.Request, requestID, setupID string) {
	if s.service == nil {
		writeError(w, http.StatusServiceUnavailable, requestID, "SERVICE_UNAVAILABLE", "The setup service is unavailable.", nil, true)
		return
	}
	setup, err := s.service.GetSetup(r.Context(), setupID)
	if err != nil {
		writeDomainError(w, requestID, err)
		return
	}
	var sheet *domain.Artifact
	for index := range setup.Artifacts {
		if setup.Artifacts[index].Role == domain.ArtifactRoleSetupSheet {
			sheet = &setup.Artifacts[index]
			break
		}
	}
	if sheet == nil {
		writeDomainError(w, requestID, domain.NewError(domain.CodeArtifactNotFound, "setup sheet was not found"))
		return
	}
	if !strings.HasPrefix(strings.ToLower(sheet.MediaType), "text/html") {
		s.serveArtifactContent(w, r, requestID, setupID, sheet.ID, domain.ArtifactRoleSetupSheet)
		return
	}
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	metadata, err := s.service.InspectArtifactContent(r.Context(), setupID, sheet.ID)
	if err != nil {
		writeDomainError(w, requestID, err)
		return
	}
	requestedVersion := r.URL.Query().Get("version")
	if (requestedVersion != "" && requestedVersion != metadata.Version) ||
		!matchExpectedETag(r.Header.Get("If-Match"), metadata.ETag) {
		writeError(w, http.StatusPreconditionFailed, requestID, string(domain.CodeArtifactChanged), "Artifact version no longer matches.", nil, false)
		return
	}
	setSafeHTMLHeaders(w.Header(), metadata.ETag)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Prime one bounded block before committing response headers. Identity or
	// version failures therefore retain the normal structured error response;
	// the remaining document is sanitized directly to the client without a
	// whole-document source or output buffer.
	source := newArtifactContentReader(r.Context(), s.service, setupID, sheet.ID, metadata.Version, metadata.ByteSize)
	if err := source.prime(); err != nil {
		writeDomainError(w, requestID, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := sanitizeHTML(r.Context(), w, source); err != nil {
		code := "HTML_STREAM_FAILED"
		if domainCode, ok := domain.ErrorCodeOf(err); ok {
			code = string(domainCode)
		}
		if recorder, ok := w.(interface{ setErrorCode(string) }); ok {
			recorder.setErrorCode(code)
		}
	}
}

const htmlStreamBlockBytes int64 = 512 << 10

// artifactContentReader adapts the ID-based, version-checked Range service to
// a bounded streaming reader. It never receives or exposes a storage path.
type artifactContentReader struct {
	ctx        context.Context
	service    *service.Service
	setupID    string
	artifactID string
	version    string
	total      int64
	next       int64
	buffer     []byte
	done       bool
}

func newArtifactContentReader(ctx context.Context, application *service.Service, setupID, artifactID, version string, total int64) *artifactContentReader {
	return &artifactContentReader{
		ctx: ctx, service: application, setupID: setupID, artifactID: artifactID,
		version: version, total: total,
	}
}

func (reader *artifactContentReader) prime() error {
	if reader.total == 0 {
		reader.done = true
		return nil
	}
	return reader.fill()
}

func (reader *artifactContentReader) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if len(reader.buffer) == 0 {
		if reader.done {
			return 0, io.EOF
		}
		if err := reader.fill(); err != nil {
			return 0, err
		}
	}
	written := copy(destination, reader.buffer)
	reader.buffer = reader.buffer[written:]
	return written, nil
}

func (reader *artifactContentReader) fill() error {
	if reader.next >= reader.total {
		reader.done = true
		return io.EOF
	}
	length := min(htmlStreamBlockBytes, reader.total-reader.next)
	block, err := reader.service.ReadArtifactRange(
		reader.ctx, reader.setupID, reader.artifactID, reader.version, reader.next, length,
	)
	if err != nil {
		return err
	}
	if block.Version != reader.version || block.ByteSize != reader.total {
		return domain.NewError(domain.CodeArtifactChanged, "artifact version no longer matches")
	}
	if len(block.Data) == 0 {
		return domain.NewError(domain.CodeUploadIncomplete, "artifact could not be read completely")
	}
	reader.next += int64(len(block.Data))
	reader.buffer = block.Data
	return nil
}

func setSafeHTMLHeaders(header http.Header, etag string) {
	header.Set("Content-Security-Policy", sanitizedHTMLCSP)
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Disposition", "inline")
	header.Set("Cache-Control", "no-store")
	header.Set("ETag", etag)
}

func setContentHeaders(header http.Header, metadata *service.ContentMetadata) {
	header.Set("Accept-Ranges", "bytes")
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", metadata.MediaType)
	disposition := "inline"
	if strings.HasPrefix(strings.ToLower(metadata.MediaType), "application/pdf") {
		// The application renders PDF bytes through its canvas-only viewer. If
		// this endpoint is opened directly, force download rather than handing
		// an untrusted document to a browser PDF plugin.
		disposition = "attachment"
	}
	header.Set("Content-Disposition", disposition)
	header.Set("ETag", metadata.ETag)
}

func matchExpectedETag(ifMatch, actual string) bool {
	if ifMatch == "" {
		return true
	}
	for _, candidate := range strings.Split(ifMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == actual {
			return true
		}
	}
	return false
}
