package domain

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestGCodeValidatorRoleAndExtensionPolicy(t *testing.T) {
	validator, err := NewGCodeValidator([]string{".NGC", " .tap "})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.ngc", "MAIN.NGC", "probe.TaP"} {
		if err := validator.ValidateExtension(name); err != nil {
			t.Errorf("extension for %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"main.txt", "main.ngc.exe", "main", "../main.ngc"} {
		if err := validator.ValidateExtension(name); err == nil {
			t.Errorf("extension/name %q accepted", name)
		}
	}
	if err := validator.ValidateRole(ArtifactRoleProgram); err != nil {
		t.Fatal(err)
	}
	if err := validator.ValidateRole(ArtifactRoleSetupSheet); !IsErrorCode(err, CodeUnsupportedFileType) {
		t.Fatalf("setup sheet role error = %v", err)
	}
	if _, err := NewGCodeValidator([]string{"ngc"}); !IsErrorCode(err, CodeUnsupportedFileType) {
		t.Fatalf("undotted extension error = %v", err)
	}
	if _, err := NewGCodeValidator([]string{".ngc", ".NGC"}); !IsErrorCode(err, CodeUnsupportedFileType) {
		t.Fatalf("duplicate extension error = %v", err)
	}
	defaults, err := NewGCodeValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.gcode", "a.nc", "a.ngc", "a.tap", "a.cnc"} {
		if err := defaults.ValidateExtension(name); err != nil {
			t.Errorf("default extension %q rejected: %v", name, err)
		}
	}
	var nilValidator *GCodeValidator
	if err := nilValidator.ValidateExtension("main.ngc"); !IsErrorCode(err, CodeUnsupportedFileType) {
		t.Fatalf("nil validator error = %v", err)
	}
}

func TestValidateGCodeContentRecognizesSupportedEncodings(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		encoding TextEncoding
		empty    bool
		bom      bool
	}{
		{"empty", "", TextEncodingASCII, true, false},
		{"ASCII", "G0 X0 Y0\r\n; comment\nM2\n", TextEncodingASCII, false, false},
		{"UTF-8", "G1 X1 (финиш)\n", TextEncodingUTF8, false, false},
		{"UTF-8 replacement rune", "G1 (�)\n", TextEncodingUTF8, false, false},
		{"BOM", "\xef\xbb\xbfG1 X1\n", TextEncodingUTF8BOM, false, true},
		{"BOM only", "\xef\xbb\xbf", TextEncodingUTF8BOM, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info, err := ValidateGCodeContent(context.Background(), strings.NewReader(test.content))
			if err != nil {
				t.Fatal(err)
			}
			if info.Encoding != test.encoding || info.Empty != test.empty || info.HasBOM != test.bom {
				t.Fatalf("info = %+v", info)
			}
			if info.ByteSize != int64(len(test.content)) {
				t.Fatalf("byteSize = %d, want %d", info.ByteSize, len(test.content))
			}
		})
	}
}

func TestValidateGCodeContentRejectsBinaryAndUnsupportedEncoding(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
	}{
		{"NUL", []byte("G1\x00X1")},
		{"escape", []byte("G1\x1bX1")},
		{"DEL", []byte("G1\x7fX1")},
		{"invalid UTF-8", []byte{0xff, 0xfe, 0xfd}},
		{"truncated UTF-8", []byte{0xe2, 0x82}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateGCodeContent(context.Background(), strings.NewReader(string(test.content)))
			if !IsErrorCode(err, CodeInvalidContent) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := ValidateGCodeContent(context.Background(), nil); !IsErrorCode(err, CodeInvalidContent) {
		t.Fatalf("nil reader error = %v", err)
	}
}

func TestGCodeValidationWorksAcrossSingleByteReads(t *testing.T) {
	content := "\xef\xbb\xbfG1 X1 (деталь)\r\nM2\n"
	reader := &oneByteReader{data: []byte(content)}
	info, err := ValidateGCodeContent(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	if info.Encoding != TextEncodingUTF8BOM || info.ByteSize != int64(len(content)) {
		t.Fatalf("info = %+v", info)
	}
}

func TestGCodeValidationCancellationAndReaderFailureAreCoded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ValidateGCodeContent(ctx, strings.NewReader("G1\n"))
	if !IsErrorCode(err, CodeJobCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}

	sentinel := errors.New("transport stopped")
	_, err = ValidateGCodeContent(context.Background(), &errorAfterReader{
		content: []byte("G1 X1\n"), err: sentinel,
	})
	if !IsErrorCode(err, CodeUploadIncomplete) || !errors.Is(err, sentinel) {
		t.Fatalf("reader error = %v", err)
	}
	var coded *Error
	if !errors.As(err, &coded) || !coded.Retryable {
		t.Fatalf("reader error is not retryable: %#v", coded)
	}
}

func TestGCodeValidatorCombinesRoleNameAndStreamingContent(t *testing.T) {
	validator, err := NewGCodeValidator([]string{".ngc"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := validator.Validate(nil, ArtifactRoleProgram, "main.NGC", strings.NewReader("G1 X1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Encoding != TextEncodingASCII || info.ByteSize != 6 {
		t.Fatalf("info = %+v", info)
	}
	if _, err := validator.Validate(context.Background(), ArtifactRoleSetupSheet, "main.ngc", strings.NewReader("G1")); !IsErrorCode(err, CodeUnsupportedFileType) {
		t.Fatalf("role error = %v", err)
	}
}

func TestGCodeValidationUsesBoundedReads(t *testing.T) {
	reader := &patternReader{remaining: 2 << 20}
	info, err := ValidateGCodeContent(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	if info.ByteSize != 2<<20 || info.Encoding != TextEncodingASCII {
		t.Fatalf("info = %+v", info)
	}
	if reader.maxRequest > gCodeReaderBufferSize {
		t.Fatalf("largest read was %d bytes, buffer is %d", reader.maxRequest, gCodeReaderBufferSize)
	}
}

type oneByteReader struct {
	data []byte
	read int
}

func (r *oneByteReader) Read(target []byte) (int, error) {
	if r.read == len(r.data) {
		return 0, io.EOF
	}
	target[0] = r.data[r.read]
	r.read++
	return 1, nil
}

type errorAfterReader struct {
	content []byte
	err     error
	done    bool
}

func (r *errorAfterReader) Read(target []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(target, r.content), nil
	}
	return 0, r.err
}

type patternReader struct {
	remaining  int64
	maxRequest int
}

func (r *patternReader) Read(target []byte) (int, error) {
	if len(target) > r.maxRequest {
		r.maxRequest = len(target)
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	count := len(target)
	if int64(count) > r.remaining {
		count = int(r.remaining)
	}
	for index := range count {
		target[index] = 'G'
	}
	r.remaining -= int64(count)
	return count, nil
}
