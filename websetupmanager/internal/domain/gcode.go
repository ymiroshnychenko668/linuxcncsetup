package domain

import (
	"bufio"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const gCodeReaderBufferSize = 64 << 10

var DefaultGCodeExtensions = []string{".gcode", ".nc", ".ngc", ".tap", ".cnc"}

type TextEncoding string

const (
	TextEncodingASCII   TextEncoding = "ascii"
	TextEncodingUTF8    TextEncoding = "utf-8"
	TextEncodingUTF8BOM TextEncoding = "utf-8-bom"
)

// GCodeContentInfo is computed in one bounded-memory streaming pass.
type GCodeContentInfo struct {
	Encoding TextEncoding `json:"encoding"`
	ByteSize int64        `json:"byteSize"`
	Empty    bool         `json:"empty"`
	HasBOM   bool         `json:"hasBom"`
}

// GCodeValidator validates the program role, configured extension, and text
// stream without buffering a complete artifact.
type GCodeValidator struct {
	extensions map[string]struct{}
}

// NewGCodeValidator builds an immutable extension policy. An empty list uses
// the documented production defaults.
func NewGCodeValidator(extensions []string) (*GCodeValidator, error) {
	if len(extensions) == 0 {
		extensions = DefaultGCodeExtensions
	}
	allowed := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		normalized := strings.ToLower(strings.TrimSpace(extension))
		if !validGCodeExtension(normalized) {
			return nil, NewError(CodeUnsupportedFileType, "G-code extension policy is invalid")
		}
		if _, duplicate := allowed[normalized]; duplicate {
			return nil, NewError(CodeUnsupportedFileType, "G-code extension policy contains duplicates")
		}
		allowed[normalized] = struct{}{}
	}
	return &GCodeValidator{extensions: allowed}, nil
}

func (v *GCodeValidator) ValidateRole(role ArtifactRole) error {
	if role != ArtifactRoleProgram {
		return NewError(CodeUnsupportedFileType, "artifact role is not a G-code program")
	}
	return nil
}

func (v *GCodeValidator) ValidateExtension(displayName string) error {
	if v == nil || len(v.extensions) == 0 {
		return NewError(CodeUnsupportedFileType, "G-code extension policy is unavailable")
	}
	normalized, err := NormalizeArtifactName(displayName)
	if err != nil {
		return err
	}
	extension := strings.ToLower(path.Ext(normalized))
	if _, ok := v.extensions[extension]; !ok {
		return NewError(CodeUnsupportedFileType, "file extension is not enabled for G-code")
	}
	return nil
}

// Validate performs all P0 G-code checks. The reader remains owned by the
// caller and is never closed by this method.
func (v *GCodeValidator) Validate(
	ctx context.Context,
	role ArtifactRole,
	displayName string,
	reader io.Reader,
) (GCodeContentInfo, error) {
	if err := v.ValidateRole(role); err != nil {
		return GCodeContentInfo{}, err
	}
	if err := v.ValidateExtension(displayName); err != nil {
		return GCodeContentInfo{}, err
	}
	return ValidateGCodeContent(ctx, reader)
}

// ValidateGCodeContent recognizes ASCII, UTF-8, and UTF-8 with a leading BOM.
// Invalid UTF-8, NUL, DEL, and non-whitespace control characters are treated
// as binary/unsupported content. Memory use is O(1) relative to the stream.
func ValidateGCodeContent(ctx context.Context, reader io.Reader) (GCodeContentInfo, error) {
	info := GCodeContentInfo{Encoding: TextEncodingASCII, Empty: true}
	if reader == nil {
		return info, NewError(CodeInvalidContent, "G-code content is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	buffered := bufio.NewReaderSize(reader, gCodeReaderBufferSize)
	firstRune := true
	hasNonASCII := false
	for {
		if err := ctx.Err(); err != nil {
			return info, &Error{
				Code: CodeJobCancelled, Message: "G-code validation was cancelled",
				Cause: err,
			}
		}
		r, size, err := buffered.ReadRune()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return info, &Error{
				Code: CodeUploadIncomplete, Message: "G-code content could not be read completely",
				Retryable: true, Cause: err,
			}
		}
		info.ByteSize += int64(size)
		if r == utf8.RuneError && size == 1 {
			return info, NewError(CodeInvalidContent, "G-code content is not valid UTF-8")
		}
		if firstRune && r == '\ufeff' {
			info.Encoding = TextEncodingUTF8BOM
			info.HasBOM = true
			firstRune = false
			continue
		}
		firstRune = false
		info.Empty = false
		if r == 0 {
			return info, NewError(CodeInvalidContent, "G-code content contains NUL bytes")
		}
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return info, NewError(CodeInvalidContent, "G-code content appears to be binary")
		}
		if r > unicode.MaxASCII {
			hasNonASCII = true
		}
	}
	if !info.HasBOM && hasNonASCII {
		info.Encoding = TextEncodingUTF8
	}
	return info, nil
}

func validGCodeExtension(extension string) bool {
	if len(extension) < 2 || extension[0] != '.' {
		return false
	}
	for i := 1; i < len(extension); i++ {
		character := extension[i]
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
