package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
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
	for _, forbidden := range []string{"<script", "fetch(", "form", "input", "hidden form text", "iframe", "object", "file://", "javascript:", "onclick", "onload", "evil.invalid"} {
		if strings.Contains(strings.ToLower(output), strings.ToLower(forbidden)) {
			t.Fatalf("sanitized output contains %q: %s", forbidden, output)
		}
	}
	for _, wanted := range []string{"Safe &amp; title", "Content-Security-Policy", sanitizedHTMLDocumentCSP, "blocked link", `href="#local"`, "data:image/png;base64,AA=="} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("sanitized output lacks %q: %s", wanted, output)
		}
	}
	if !strings.HasSuffix(output, sanitizedHTMLCompletionSuffix) {
		t.Fatalf("sanitized output lacks completion marker: %s", output)
	}
}

func TestSanitizeHTMLDoesNotMarkFailedStreamComplete(t *testing.T) {
	reader := &errorAfterContentReader{content: []byte("<p>Partial safe content</p>")}
	var result bytes.Buffer
	if err := sanitizeHTML(context.Background(), &result, reader); err == nil {
		t.Fatal("failed source stream was accepted")
	}
	if strings.HasSuffix(result.String(), sanitizedHTMLCompletionMarker) {
		t.Fatalf("failed source stream was marked complete: %s", result.String())
	}
}

func TestSanitizeHTMLSelfClosingRawTextCannotForgeFailedStream(t *testing.T) {
	reader := &errorAfterContentReader{content: []byte("<p>Visible</p><xmp/>" + sanitizedHTMLCompletionMarker)}
	var result bytes.Buffer
	if err := sanitizeHTML(context.Background(), &result, reader); err == nil {
		t.Fatal("failed source stream was accepted")
	}
	if strings.Contains(result.String(), sanitizedHTMLCompletionMarker) {
		t.Fatalf("self-closing raw-text element forged completion marker: %s", result.String())
	}
}

func TestSanitizeHTMLCompletionMarkerCannotBeForged(t *testing.T) {
	source := `<p title="<!--websetupmanager:sanitized-html-complete:v1-->">Visible &lt;!--websetupmanager:sanitized-html-complete:v1--&gt;</p><!--websetupmanager:sanitized-html-complete:v1--><script><!--websetupmanager:sanitized-html-complete:v1--></script><style>/* <!--websetupmanager:sanitized-html-complete:v1--> */</style><textarea><!--websetupmanager:sanitized-html-complete:v1--></textarea><svg><![CDATA[<!--websetupmanager:sanitized-html-complete:v1-->]]></svg>`
	var result bytes.Buffer
	if err := sanitizeHTML(context.Background(), &result, strings.NewReader(source)); err != nil {
		t.Fatal(err)
	}
	output := result.String()
	if strings.Count(output, sanitizedHTMLCompletionMarker) != 1 || !strings.HasSuffix(output, sanitizedHTMLCompletionSuffix) {
		t.Fatalf("source forged completion marker: %s", output)
	}
}

func TestSanitizeHTMLDoesNotCompleteAfterFinalWriteFailure(t *testing.T) {
	var complete bytes.Buffer
	if err := sanitizeHTML(context.Background(), &complete, strings.NewReader("<p>Safe</p>")); err != nil {
		t.Fatal(err)
	}
	writer := &limitedErrorWriter{remaining: complete.Len() - 1}
	if err := sanitizeHTML(context.Background(), writer, strings.NewReader("<p>Safe</p>")); err == nil {
		t.Fatal("sanitizer accepted a failed final write")
	}
	if strings.HasSuffix(writer.String(), sanitizedHTMLCompletionSuffix) {
		t.Fatalf("failed final write produced completion suffix: %s", writer.String())
	}
}

func TestSanitizeHTMLDropsSourceHeadAndAddsReadableBaseStyle(t *testing.T) {
	source := `<!doctype html><html><head><title>Do not render this title</title><style>@import url('/api/v1/style-canary'); body { color: magenta; }</style></head><body><h1>Setup title</h1><table class="sheet"><tr><th>Operation</th><th>Tool</th></tr><tr><td><div class="description" style="display:inline;background:url('/api/v1/attribute-canary')">Type: </div><div class="value" style="cursor:url(https://evil.invalid/cursor),auto">Contour</div></td><td>T1</td></tr></table></body></html>`
	var result bytes.Buffer
	if err := sanitizeHTML(context.Background(), &result, strings.NewReader(source)); err != nil {
		t.Fatal(err)
	}
	output := result.String()
	for _, forbidden := range []string{"Do not render this title", "color: magenta", "style-canary", "attribute-canary", "evil.invalid"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("sanitized output contains source head content %q: %s", forbidden, output)
		}
	}
	for _, wanted := range []string{
		"Setup title",
		`<table class="sheet">`,
		"font-family: Inter",
		"body > table",
		"overflow-wrap: normal",
		"min-width: 11rem",
		".description { display: inline;",
		"@media print",
	} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("sanitized output lacks readable base style %q: %s", wanted, output)
		}
	}
	if strings.Contains(output, ` style=`) {
		t.Fatalf("sanitized output preserved source style attribute: %s", output)
	}
	if strings.Count(output, "<style>") != 1 || strings.Count(output, "</style>") != 1 {
		t.Fatalf("sanitized output must contain exactly one application-owned style: %s", output)
	}
}

func TestSanitizeHTMLDropsStyleAndTitleSubtreesOutsideHead(t *testing.T) {
	source := `<p>Before</p><style>.secret { display: none }</style><title>Not document content</title><p>After</p>`
	var result bytes.Buffer
	if err := sanitizeHTML(context.Background(), &result, strings.NewReader(source)); err != nil {
		t.Fatal(err)
	}
	output := result.String()
	for _, forbidden := range []string{".secret", "Not document content"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("sanitized output contains dropped subtree %q: %s", forbidden, output)
		}
	}
	for _, wanted := range []string{"<p>Before</p>", "<p>After</p>"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("sanitized output lacks %q: %s", wanted, output)
		}
	}
}

func TestSanitizeHTMLHandlesImplicitHeadClose(t *testing.T) {
	for name, source := range map[string]string{
		"body start":                     `<html><head><title>Hidden</title><body><h1>Visible body</h1></body></html>`,
		"content start":                  `<html><head><title>Hidden</title><h1>Visible content</h1></html>`,
		"nested template":                `<html><head><template><p>Hidden template</p></template><body><h1>Visible body</h1></body></html>`,
		"mismatched end inside template": `<html><head><template></head><p>Hidden template</p></template><body><h1>Visible body</h1></body></html>`,
		"self-closing start":             `<html><head><meta charset="utf-8"><br/>Visible break</html>`,
	} {
		t.Run(name, func(t *testing.T) {
			var result bytes.Buffer
			if err := sanitizeHTML(context.Background(), &result, strings.NewReader(source)); err != nil {
				t.Fatal(err)
			}
			output := result.String()
			if strings.Contains(output, "Hidden") || !strings.Contains(output, "Visible") {
				t.Fatalf("implicit head close was not sanitized correctly: %s", output)
			}
		})
	}
}

func TestSanitizeHTMLBaseStyleHashMatchesCSP(t *testing.T) {
	digest := sha256.Sum256([]byte(sanitizedHTMLBaseStyle))
	got := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
	if got != sanitizedHTMLStyleHash {
		t.Fatalf("base style hash = %q, want %q", got, sanitizedHTMLStyleHash)
	}
	for name, policy := range map[string]string{
		"response": sanitizedHTMLCSP,
		"document": sanitizedHTMLDocumentCSP,
	} {
		if !strings.Contains(policy, "'"+sanitizedHTMLStyleHash+"'") {
			t.Fatalf("%s CSP does not allow the exact base style hash: %s", name, policy)
		}
	}
	if strings.Contains(sanitizedHTMLCSP, "'unsafe-inline'") || strings.Contains(sanitizedHTMLDocumentCSP, "'unsafe-inline'") {
		t.Fatal("sandboxed setup-sheet CSP must remain hash-only")
	}
	if !strings.Contains(appCSP, "style-src 'self' 'unsafe-inline'") {
		t.Fatal("trusted application shell CSP must permit React's dynamic geometry styles")
	}
	if strings.Contains(appCSP, sanitizedHTMLStyleHash) {
		t.Fatal("application CSP must not combine unsafe-inline with a style hash, which disables inline styles")
	}
}

func TestSanitizeHTMLDropsRawTextMutationVectors(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "noscript", source: `<p>Safe</p><noscript><img src="data:image/png;base64,AA=="><a href="#hidden">hidden</a></noscript><p>After</p>`},
		{name: "noembed", source: `<p>Safe</p><noembed><img src="data:image/png;base64,AA=="></noembed><p>After</p>`},
		{name: "noframes", source: `<p>Safe</p><noframes><a href="#hidden">hidden</a></noframes><p>After</p>`},
		{name: "xmp", source: `<p>Safe</p><xmp><img src="data:image/png;base64,AA=="></xmp><p>After</p>`},
		{name: "plaintext", source: `<p>Safe</p><plaintext><img src="data:image/png;base64,AA==">hidden`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var result bytes.Buffer
			if err := sanitizeHTML(context.Background(), &result, strings.NewReader(test.source)); err != nil {
				t.Fatal(err)
			}
			output := result.String()
			if !strings.Contains(output, "<p>Safe</p>") {
				t.Fatalf("sanitized output lost safe prefix: %s", output)
			}
			for _, forbidden := range []string{`src="data:image/png`, `href="#hidden"`, "hidden</a>"} {
				if strings.Contains(output, forbidden) {
					t.Fatalf("sanitized output contains raw-text mutation vector %q: %s", forbidden, output)
				}
			}
		})
	}
}

func TestSanitizeHTMLDropsSelfClosingRawTextMutationVectors(t *testing.T) {
	for _, tag := range []string{"iframe", "noembed", "noframes", "noscript", "script", "style", "textarea", "title", "xmp"} {
		t.Run(tag, func(t *testing.T) {
			source := "<p>Before</p><" + tag + "/><img src=\"data:image/png;base64,AA==\">hidden</" + tag + "><p>After</p>"
			var result bytes.Buffer
			if err := sanitizeHTML(context.Background(), &result, strings.NewReader(source)); err != nil {
				t.Fatal(err)
			}
			output := result.String()
			if !strings.Contains(output, "<p>Before</p>") || !strings.Contains(output, "<p>After</p>") ||
				strings.Contains(output, `src="data:image/png`) || strings.Contains(output, "hidden") {
				t.Fatalf("self-closing %s subtree escaped suppression: %s", tag, output)
			}
		})
	}
}

func TestSanitizeHTMLKeepsContentAfterSelfClosingForeignRoots(t *testing.T) {
	for _, tag := range []string{"math", "svg"} {
		t.Run(tag, func(t *testing.T) {
			source := "<p>Before</p><" + tag + "/><p>After</p>"
			var result bytes.Buffer
			if err := sanitizeHTML(context.Background(), &result, strings.NewReader(source)); err != nil {
				t.Fatal(err)
			}
			output := result.String()
			if !strings.Contains(output, "<p>Before</p><p>After</p>") || strings.Contains(output, "<"+tag) {
				t.Fatalf("self-closing foreign root suppressed following content: %s", output)
			}
		})
	}
}

func TestSanitizeHTMLBoundsSuppressionNesting(t *testing.T) {
	source := strings.Repeat("<template>", maxSanitizedHTMLSuppressionDepth+1) + "hidden"
	var result bytes.Buffer
	if err := sanitizeHTML(context.Background(), &result, strings.NewReader(source)); err == nil {
		t.Fatal("excessive suppression nesting was accepted")
	}
	if strings.HasSuffix(result.String(), sanitizedHTMLCompletionSuffix) {
		t.Fatalf("excessive suppression nesting produced completion suffix: %s", result.String())
	}
}

func TestSanitizeHTMLDoesNotRetainArbitraryNestedTagNames(t *testing.T) {
	source := "<template>" + strings.Repeat("<custom-element>", maxSanitizedHTMLSuppressionDepth+1) +
		"hidden" + strings.Repeat("</custom-element>", maxSanitizedHTMLSuppressionDepth+1) +
		"</template><p>After</p>"
	var result bytes.Buffer
	if err := sanitizeHTML(context.Background(), &result, strings.NewReader(source)); err != nil {
		t.Fatal(err)
	}
	output := result.String()
	if strings.Contains(output, "hidden") || !strings.Contains(output, "<p>After</p>") ||
		!strings.HasSuffix(output, sanitizedHTMLCompletionSuffix) {
		t.Fatalf("arbitrary nested tag names affected bounded suppression: %s", output)
	}
}

func TestSanitizeHTMLHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var result bytes.Buffer
	if err := sanitizeHTML(ctx, &result, strings.NewReader("<p>content</p>")); err == nil {
		t.Fatal("cancelled sanitizer succeeded")
	}
	if strings.HasSuffix(result.String(), sanitizedHTMLCompletionSuffix) {
		t.Fatalf("cancelled sanitizer produced completion suffix: %s", result.String())
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

type errorAfterContentReader struct {
	content []byte
	read    bool
}

type limitedErrorWriter struct {
	buffer    bytes.Buffer
	remaining int
}

func (writer *limitedErrorWriter) Write(value []byte) (int, error) {
	if len(value) <= writer.remaining {
		writer.remaining -= len(value)
		return writer.buffer.Write(value)
	}
	written, _ := writer.buffer.Write(value[:writer.remaining])
	writer.remaining = 0
	return written, errors.New("injected destination write failure")
}

func (writer *limitedErrorWriter) String() string { return writer.buffer.String() }

func (reader *errorAfterContentReader) Read(destination []byte) (int, error) {
	if reader.read {
		return 0, errors.New("injected source read failure")
	}
	reader.read = true
	return copy(destination, reader.content), nil
}
