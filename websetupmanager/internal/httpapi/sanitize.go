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

const sanitizedHTMLCSP = "sandbox; default-src 'none'; connect-src 'none'; frame-ancestors 'self'; img-src data:; style-src 'unsafe-inline'"
const sanitizedHTMLDocumentCSP = "default-src 'none'; connect-src 'none'; img-src data:; style-src 'unsafe-inline'"

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

var dropHTMLSubtrees = map[string]bool{
	"applet": true, "audio": true, "button": true, "canvas": true,
	"fieldset": true, "form": true, "frameset": true,
	"iframe": true, "math": true,
	"object": true, "option": true, "script": true, "select": true, "svg": true,
	"template": true, "textarea": true, "video": true,
}

var blockedHTMLTags = map[string]bool{
	"area": true, "base": true, "embed": true, "frame": true, "input": true,
	"link": true, "meta": true, "param": true, "source": true, "track": true,
}

// sanitizeHTML emits a new standalone document. It never forwards active
// elements, event handlers, navigation URLs, forms, embedded frames or object
// content from the source document.
func sanitizeHTML(ctx context.Context, destination io.Writer, source io.Reader) error {
	if ctx == nil || destination == nil || source == nil {
		return errors.New("invalid HTML sanitizer input")
	}
	if _, err := fmt.Fprintf(destination, "<!doctype html><html><head><meta charset=\"utf-8\"><meta http-equiv=\"Content-Security-Policy\" content=\"%s\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>Setup Sheet</title></head><body>", sanitizedHTMLDocumentCSP); err != nil {
		return err
	}
	tokenizer := html.NewTokenizer(source)
	tokenizer.SetMaxBuf(service.MaxHTMLSetupSheetTokenBytes)
	dropDepth := 0
	var dropTag string
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
			_, err := io.WriteString(destination, "</body></html>")
			return err
		case html.StartTagToken:
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if dropDepth > 0 {
				if name == dropTag {
					dropDepth++
				}
				continue
			}
			if blockedHTMLTags[name] {
				continue
			}
			if dropHTMLSubtrees[name] {
				dropTag, dropDepth = name, 1
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
			if dropDepth > 0 {
				continue
			}
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
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
			if dropDepth > 0 {
				if name == dropTag {
					dropDepth--
					if dropDepth == 0 {
						dropTag = ""
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
			if dropDepth == 0 {
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
		case "class", "dir", "height", "id", "lang", "open", "scope", "style", "title", "width":
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
