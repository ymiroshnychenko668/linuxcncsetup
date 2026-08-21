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

func (s *Server) serveCatalogContent(w http.ResponseWriter, r *http.Request, requestID, setupID string, role domain.ArtifactRole) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		writeError(w, http.StatusMethodNotAllowed, requestID, "METHOD_NOT_ALLOWED", "The method is not allowed.", nil, false)
		return
	}
	metadata, err := s.service.InspectCatalogContent(r.Context(), setupID, role)
	if err != nil {
		writeDomainError(w, requestID, err)
		return
	}
	requestedVersion := r.URL.Query().Get("version")
	if (requestedVersion != "" && requestedVersion != metadata.Version) ||
		!matchExpectedETag(r.Header.Get("If-Match"), metadata.ETag) {
		writeError(w, http.StatusPreconditionFailed, requestID, string(domain.CodeArtifactChanged), "Catalog file version no longer matches.", nil, false)
		return
	}
	if role == domain.ArtifactRoleSetupSheet && strings.HasPrefix(strings.ToLower(metadata.MediaType), "text/html") {
		s.serveCatalogHTML(w, r, requestID, setupID, metadata)
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
	if r.Header.Get("Range") == "" {
		s.streamCatalogContent(w, r, requestID, setupID, role, metadata)
		return
	}
	block, err := s.service.ReadCatalogRange(r.Context(), setupID, role, metadata.Version, selected.start, selected.length)
	if err != nil {
		writeDomainError(w, requestID, err)
		return
	}
	if block.Version != metadata.Version || block.ByteSize != metadata.ByteSize {
		writeError(w, http.StatusConflict, requestID, string(domain.CodeArtifactChanged), "Catalog file version no longer matches.", nil, false)
		return
	}
	w.Header().Set("Content-Range", contentRangeHeader(selected, metadata.ByteSize))
	w.Header().Set("Content-Length", strconv.Itoa(len(block.Data)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(block.Data)
}

func (s *Server) streamCatalogContent(w http.ResponseWriter, r *http.Request, requestID, setupID string, role domain.ArtifactRole, metadata *service.ContentMetadata) {
	w.Header().Set("Content-Length", strconv.FormatInt(metadata.ByteSize, 10))
	for offset := int64(0); offset < metadata.ByteSize; {
		length := min(service.MaxContentRangeBytes, metadata.ByteSize-offset)
		block, err := s.service.ReadCatalogRange(r.Context(), setupID, role, metadata.Version, offset, length)
		if err != nil {
			if offset == 0 {
				w.Header().Del("Content-Length")
				writeDomainError(w, requestID, err)
			} else if recorder, ok := w.(interface{ setErrorCode(string) }); ok {
				recorder.setErrorCode("CONTENT_READ_FAILED")
			}
			return
		}
		if block.Version != metadata.Version || block.ByteSize != metadata.ByteSize || len(block.Data) == 0 {
			if offset == 0 {
				w.Header().Del("Content-Length")
				writeError(w, http.StatusConflict, requestID, string(domain.CodeArtifactChanged), "Catalog file version no longer matches.", nil, false)
			} else if recorder, ok := w.(interface{ setErrorCode(string) }); ok {
				recorder.setErrorCode(string(domain.CodeArtifactChanged))
			}
			return
		}
		written, writeErr := w.Write(block.Data)
		offset += int64(written)
		if writeErr != nil || written != len(block.Data) {
			return
		}
	}
}

func (s *Server) serveCatalogHTML(w http.ResponseWriter, r *http.Request, requestID, setupID string, metadata *service.ContentMetadata) {
	if r.Header.Get("Range") != "" {
		writeError(w, http.StatusBadRequest, requestID, string(domain.CodeInvalidRange), "Range is not supported for sanitized HTML.", nil, false)
		return
	}
	if r.Method == http.MethodHead {
		setSafeHTMLHeaders(w.Header(), metadata.ETag)
		w.WriteHeader(http.StatusOK)
		return
	}
	source := newCatalogContentReader(r.Context(), s.service, setupID, domain.ArtifactRoleSetupSheet, metadata.Version, metadata.ByteSize)
	if err := source.prime(); err != nil {
		writeDomainError(w, requestID, err)
		return
	}
	setSafeHTMLHeaders(w.Header(), metadata.ETag)
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

type catalogContentReader struct {
	ctx     context.Context
	service *service.Service
	setupID string
	role    domain.ArtifactRole
	version string
	total   int64
	next    int64
	buffer  []byte
	done    bool
}

func newCatalogContentReader(ctx context.Context, application *service.Service, setupID string, role domain.ArtifactRole, version string, total int64) *catalogContentReader {
	return &catalogContentReader{ctx: ctx, service: application, setupID: setupID, role: role, version: version, total: total}
}

func (reader *catalogContentReader) prime() error {
	if reader.total == 0 {
		reader.done = true
		return nil
	}
	return reader.fill()
}

func (reader *catalogContentReader) Read(destination []byte) (int, error) {
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

func (reader *catalogContentReader) fill() error {
	if reader.next >= reader.total {
		reader.done = true
		return io.EOF
	}
	length := min(htmlStreamBlockBytes, reader.total-reader.next)
	block, err := reader.service.ReadCatalogRange(reader.ctx, reader.setupID, reader.role, reader.version, reader.next, length)
	if err != nil {
		return err
	}
	if block.Version != reader.version || block.ByteSize != reader.total {
		return domain.NewError(domain.CodeArtifactChanged, "catalog file version no longer matches")
	}
	if len(block.Data) == 0 {
		return domain.NewError(domain.CodeUploadIncomplete, "catalog file could not be read completely")
	}
	reader.next += int64(len(block.Data))
	reader.buffer = block.Data
	return nil
}
