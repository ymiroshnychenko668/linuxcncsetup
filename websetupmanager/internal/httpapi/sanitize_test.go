package httpapi

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/service"
)

func TestSanitizeHTMLRemovesActiveAndNavigatingContent(t *testing.T) {
	source := `<!doctype html><html><head><meta http-equiv="refresh" content="0;url=https://evil.invalid"><script>fetch('/api/v1/setups')</script></head><body onload="steal()"><h1 style="color:red">Safe &amp; title</h1><form action="https://evil.invalid"><input value="secret"><p>hidden form text</p></form><iframe src="https://evil.invalid">frame</iframe><object data="file:///etc/passwd"></object><a href="javascript:steal()" onclick="steal()">blocked link</a><a href="#local">local</a><img src="https://evil.invalid/pixel"><img src="data:image/png;base64,AA==" onerror="steal()"></body></html>`
	var result bytes.Buffer
	if err := sanitizeHTML(context.Background(), &result, strings.NewReader(source)); err != nil {
		t.Fatal(err)
	}
	output := result.String()
	for _, forbidden := range []string{"script", "fetch(", "form", "input", "hidden form text", "iframe", "object", "file://", "javascript:", "onclick", "onload", "evil.invalid"} {
		if strings.Contains(strings.ToLower(output), strings.ToLower(forbidden)) {
			t.Fatalf("sanitized output contains %q: %s", forbidden, output)
		}
	}
	for _, wanted := range []string{"Safe &amp; title", "Content-Security-Policy", sanitizedHTMLDocumentCSP, "blocked link", `href="#local"`, "data:image/png;base64,AA=="} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("sanitized output lacks %q: %s", wanted, output)
		}
	}
}

func TestSanitizeHTMLHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var result bytes.Buffer
	if err := sanitizeHTML(ctx, &result, strings.NewReader("<p>content</p>")); err == nil {
		t.Fatal("cancelled sanitizer succeeded")
	}
}

func TestSanitizeHTMLRejectsAnOversizedSingleToken(t *testing.T) {
	source := `<p title="` + strings.Repeat("&", service.MaxHTMLSetupSheetTokenBytes+1) + `">content</p>`
	if err := sanitizeHTML(context.Background(), &bytes.Buffer{}, strings.NewReader(source)); err == nil {
		t.Fatal("escaping-heavy single token was accepted")
	}
}

func TestSanitizeHTMLStreamsDocumentsLargerThanFormerViewerLimit(t *testing.T) {
	paragraph := "<p>Safe setup instruction &amp; measurement.</p>"
	source := strings.Repeat(paragraph, (3<<20)/len(paragraph)+1)
	writer := &countingWriter{}
	if err := sanitizeHTML(context.Background(), writer, strings.NewReader(source)); err != nil {
		t.Fatal(err)
	}
	if writer.written <= 2<<20 {
		t.Fatalf("streamed sanitized output = %d bytes", writer.written)
	}
}

type countingWriter struct{ written int64 }

func (writer *countingWriter) Write(value []byte) (int, error) {
	writer.written += int64(len(value))
	return len(value), nil
}
