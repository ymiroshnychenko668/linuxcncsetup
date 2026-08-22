package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/service"
	"golang.org/x/net/html"
)

const sanitizedHTMLStyleHash = "sha256-w2JlanhbVje43OM1+Fh32en7h5Cas59JS7qV3hkvaUU="
const sanitizedHTMLCSP = "sandbox; default-src 'none'; connect-src 'none'; frame-ancestors 'self'; img-src data:; style-src '" + sanitizedHTMLStyleHash + "'"
const sanitizedHTMLDocumentCSP = "default-src 'none'; connect-src 'none'; img-src data:; style-src '" + sanitizedHTMLStyleHash + "'"
const sanitizedHTMLCompletionMarker = "<!--websetupmanager:sanitized-html-complete:v1-->"
const sanitizedHTMLCompletionSuffix = "</body></html>" + sanitizedHTMLCompletionMarker

// sanitizedHTMLBaseStyle is application-owned rather than copied from the
// uploaded document. It gives plain and legacy CAM setup sheets a predictable,
// readable print-like layout without allowing source styles to cover viewer
// controls or load external resources.
const sanitizedHTMLBaseStyle = `
:root {
	color-scheme: only light;
	font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
	color: #18211c;
	background: #e9eeeb;
}
*, *::before, *::after { box-sizing: border-box; }
html { background: #e9eeeb; }
body {
	max-width: 1120px;
	min-height: 100vh;
	margin: 0 auto;
	padding: clamp(1rem, 3vw, 2.5rem);
	background: #fff;
	font-size: 15px;
	line-height: 1.45;
	overflow-wrap: anywhere;
}
body > :first-child { margin-top: 0; }
h1, h2, h3, h4, h5, h6 {
	margin: 1.25em 0 0.55em;
	color: #101713;
	line-height: 1.2;
	break-after: avoid;
}
h1 { font-size: clamp(1.45rem, 3vw, 2rem); text-align: center; }
h2 { font-size: 1.35rem; }
h3 { font-size: 1.15rem; }
h4, h5, h6 { font-size: 1rem; }
p, ul, ol, dl, blockquote, pre { margin: 0.7rem 0; }
ul, ol { padding-left: 1.6rem; }
a { color: #17633e; text-decoration-thickness: 0.08em; text-underline-offset: 0.16em; }
hr { border: 0; border-top: 1px solid #c8d2cc; margin: 1.5rem 0; }
blockquote { padding-left: 1rem; border-left: 0.25rem solid #94aa9e; color: #45534b; }
pre, code, kbd {
	font-family: ui-monospace, SFMono-Regular, Consolas, "Liberation Mono", monospace;
}
pre {
	overflow: auto;
	padding: 0.8rem;
	border: 1px solid #d5ddd8;
	border-radius: 0.3rem;
	background: #f5f7f6;
	white-space: pre-wrap;
}
img { max-width: 100%; height: auto; }
figure { max-width: 100%; margin: 1rem auto; }
figcaption { margin-top: 0.4rem; color: #58665e; font-size: 0.9rem; }
table {
	max-width: 100%;
	margin: 0.8rem 0;
	border-spacing: 0;
	border-collapse: collapse;
}
body > table {
	width: min(100%, 18cm);
	margin-right: auto;
	margin-left: auto;
}
th, td {
	padding: 0.45rem 0.55rem;
	border: 1px solid #c7d1cb;
	text-align: left;
	vertical-align: top;
	overflow-wrap: normal;
	word-break: normal;
}
th { background: #e5ebe7; color: #17201b; font-weight: 700; }
caption { padding: 0.45rem; font-size: 1.05rem; font-weight: 700; }
table table { width: 100%; margin: 0; }
.jobhead { margin-bottom: 1rem; }
.jobhead td, table.info td, table.info th { border: 0; }
.jobhead td { padding: 0.2rem 0.35rem; font-size: 1rem; }
table.info td, table.info th { padding: 0.2rem 0.35rem; }
.job, .sheet { border: 1px solid #9faca5; }
.job td { padding: 0.45rem; }
.job > tbody > tr > td:first-child { min-width: 11rem; }
.odd > td { background: #f4f7f5; }
.space > td { height: 1px; padding: 0; border-bottom-color: #9faca5; font-size: 1px; }
.description { display: inline; color: #45554c; font-size: 0.82rem; font-variant: small-caps; letter-spacing: 0.02em; }
.value { display: inline; color: #4c5a52; }
.percentage { display: inline; font-size: 0.78rem; }
.model img, .preview img { display: block; border: 2px solid #28342d; }
.model img { width: min(100%, 12cm); }
.preview img { width: min(100%, 8cm); }
.image { text-align: right; }
.notes, .longtext { white-space: normal; }
.footer { color: #68766e; font-size: 0.85rem; text-align: center; }
details { margin: 0.7rem 0; }
summary { cursor: pointer; font-weight: 650; }
@media (max-width: 640px) {
	body { padding: 0.8rem; font-size: 14px; }
	th, td { padding: 0.35rem 0.4rem; }
}
@media print {
	:root, html { background: #fff; }
	body { max-width: none; min-height: 0; padding: 0; }
	h1, h2, h3, h4, h5, h6, tr, img { break-inside: avoid; }
}
`

var allowedHTMLTags = map[string]bool{
	"a": true, "abbr": true, "article": true, "aside": true, "b": true,
	"blockquote": true, "br": true, "caption": true, "code": true, "col": true,
	"colgroup": true, "dd": true, "del": true, "details": true, "div": true,
	"dl": true, "dt": true, "em": true, "figcaption": true, "figure": true,
	"footer": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
	"h6": true, "header": true, "hr": true, "i": true, "img": true, "ins": true,
	"kbd": true, "li": true, "main": true, "mark": true, "ol": true, "p": true,
	"pre": true, "s": true, "section": true, "small": true, "span": true,
	"strong": true, "sub": true, "summary": true, "sup": true, "table": true,
	"tbody": true, "td": true, "tfoot": true, "th": true, "thead": true,
	"time": true, "tr": true, "u": true, "ul": true,
}

// The values deliberately repeat the canonical static names. The suppression
// stack therefore never retains attacker-controlled tag-name strings.
var dropHTMLSubtrees = map[string]string{
	"applet": "applet", "audio": "audio", "button": "button", "canvas": "canvas",
	"fieldset": "fieldset", "form": "form", "frameset": "frameset",
	"head": "head", "iframe": "iframe", "math": "math",
	"listing": "listing", "noembed": "noembed", "noframes": "noframes", "noscript": "noscript",
	"object": "object", "option": "option", "plaintext": "plaintext", "script": "script",
	"select": "select", "style": "style", "svg": "svg", "template": "template",
	"textarea": "textarea", "title": "title", "video": "video", "xmp": "xmp",
}

var blockedHTMLTags = map[string]bool{
	"area": true, "base": true, "embed": true, "frame": true, "input": true,
	"link": true, "meta": true, "param": true, "source": true, "track": true,
}

const maxSanitizedHTMLSuppressionDepth = 256

// Unlike ordinary HTML elements, foreign roots acknowledge a self-closing
// slash. A self-closing <svg/> or <math/> therefore has no subtree to suppress.
var selfClosingForeignHTMLTags = map[string]bool{"math": true, "svg": true}

// sanitizeHTML emits a new standalone document. It never forwards active
// elements, event handlers, navigation URLs, forms, embedded frames or object
// content from the source document.
func sanitizeHTML(ctx context.Context, destination io.Writer, source io.Reader) error {
	if ctx == nil || destination == nil || source == nil {
		return errors.New("invalid HTML sanitizer input")
	}
	if _, err := fmt.Fprintf(destination, "<!doctype html><html><head><meta charset=\"utf-8\"><meta http-equiv=\"Content-Security-Policy\" content=\"%s\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>Setup Sheet</title><style>%s</style></head><body>", sanitizedHTMLDocumentCSP, sanitizedHTMLBaseStyle); err != nil {
		return err
	}
	tokenizer := html.NewTokenizer(source)
	tokenizer.SetMaxBuf(service.MaxHTMLSetupSheetTokenBytes)
	suppressedTags := make([]string, 0, 4)
	pushSuppressedTag := func(tag string) error {
		if len(suppressedTags) >= maxSanitizedHTMLSuppressionDepth {
			return fmt.Errorf("parse setup sheet HTML: suppression nesting exceeds %d elements", maxSanitizedHTMLSuppressionDepth)
		}
		suppressedTags = append(suppressedTags, tag)
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			if err := tokenizer.Err(); err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("parse setup sheet HTML: %w", err)
			}
			_, err := io.WriteString(destination, sanitizedHTMLCompletionSuffix)
			return err
		case html.StartTagToken:
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if len(suppressedTags) > 0 {
				if len(suppressedTags) == 1 && suppressedTags[0] == "head" && (name == "body" || allowedHTMLTags[name]) {
					suppressedTags = suppressedTags[:0]
					if name == "body" {
						continue
					}
				} else {
					if tag, suppressed := dropHTMLSubtrees[name]; suppressed {
						if err := pushSuppressedTag(tag); err != nil {
							return err
						}
					}
					continue
				}
			}
			if blockedHTMLTags[name] {
				continue
			}
			if tag, suppressed := dropHTMLSubtrees[name]; suppressed {
				if err := pushSuppressedTag(tag); err != nil {
					return err
				}
				continue
			}
			if !allowedHTMLTags[name] {
				continue
			}
			token.Data, token.Attr = name, safeHTMLAttributes(name, token.Attr)
			if _, err := io.WriteString(destination, token.String()); err != nil {
				return err
			}
		case html.SelfClosingTagToken:
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if len(suppressedTags) > 0 {
				if len(suppressedTags) == 1 && suppressedTags[0] == "head" && (name == "body" || allowedHTMLTags[name]) {
					suppressedTags = suppressedTags[:0]
					if name == "body" {
						continue
					}
				} else {
					if tag, suppressed := dropHTMLSubtrees[name]; suppressed && !selfClosingForeignHTMLTags[name] {
						if err := pushSuppressedTag(tag); err != nil {
							return err
						}
					}
					continue
				}
			}
			if blockedHTMLTags[name] {
				continue
			}
			if tag, suppressed := dropHTMLSubtrees[name]; suppressed {
				if !selfClosingForeignHTMLTags[name] {
					if err := pushSuppressedTag(tag); err != nil {
						return err
					}
				}
				continue
			}
			if !allowedHTMLTags[name] {
				continue
			}
			token.Data, token.Attr = name, safeHTMLAttributes(name, token.Attr)
			if _, err := io.WriteString(destination, token.String()); err != nil {
				return err
			}
		case html.EndTagToken:
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if len(suppressedTags) > 0 {
				canonical, suppressed := dropHTMLSubtrees[name]
				for index := len(suppressedTags) - 1; suppressed && index >= 0; index-- {
					if suppressedTags[index] == canonical {
						suppressedTags = suppressedTags[:index]
						break
					}
					// Template contents have their own HTML parsing context. A
					// mismatched end tag inside one must not close an outer
					// suppressed head, form or other ancestor.
					if suppressedTags[index] == "template" {
						break
					}
				}
				continue
			}
			if allowedHTMLTags[name] {
				token.Data = name
				if _, err := io.WriteString(destination, token.String()); err != nil {
					return err
				}
			}
		case html.TextToken:
			if len(suppressedTags) == 0 {
				if _, err := destination.Write(tokenizer.Raw()); err != nil {
					return err
				}
			}
		}
	}
}

func safeHTMLAttributes(tag string, attributes []html.Attribute) []html.Attribute {
	result := make([]html.Attribute, 0, len(attributes))
	for _, attribute := range attributes {
		key := strings.ToLower(attribute.Key)
		if strings.HasPrefix(key, "on") || attribute.Namespace != "" {
			continue
		}
		switch key {
		case "class", "dir", "height", "id", "lang", "open", "scope", "title", "width":
			result = append(result, html.Attribute{Key: key, Val: attribute.Val})
		case "alt":
			if tag == "img" {
				result = append(result, html.Attribute{Key: key, Val: attribute.Val})
			}
		case "src":
			if tag == "img" && safeImageDataURL(attribute.Val) {
				result = append(result, html.Attribute{Key: key, Val: attribute.Val})
			}
		case "href":
			if tag == "a" && strings.HasPrefix(attribute.Val, "#") && !strings.ContainsAny(attribute.Val, "\r\n") {
				result = append(result, html.Attribute{Key: key, Val: attribute.Val})
			}
		case "colspan", "rowspan", "start":
			result = append(result, html.Attribute{Key: key, Val: attribute.Val})
		}
	}
	return result
}

func safeImageDataURL(value string) bool {
	lower := strings.ToLower(value)
	for _, prefix := range []string{"data:image/png;base64,", "data:image/jpeg;base64,", "data:image/gif;base64,", "data:image/webp;base64,"} {
		if strings.HasPrefix(lower, prefix) && !strings.ContainsAny(value, "\r\n") {
			return true
		}
	}
	return false
}
