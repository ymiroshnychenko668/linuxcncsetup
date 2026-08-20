package service

import (
	"context"
	"strings"
	"testing"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

func TestHTMLSetupSheetRejectsOversizedTokenBeforePublication(t *testing.T) {
	source := "<!doctype html><html><body><p>" +
		strings.Repeat("x", MaxHTMLSetupSheetTokenBytes+1) +
		"</p></body></html>"
	err := validateSetupSheetContent(context.Background(), mediaTypeHTML, strings.NewReader(source))
	if !domain.IsErrorCode(err, domain.CodeInvalidContent) {
		t.Fatalf("oversized HTML token error = %v", err)
	}
}

func TestHTMLSetupSheetAllowsLargeDocumentWithBoundedTokens(t *testing.T) {
	paragraph := "<p>Safe setup instruction &amp; measurement.</p>"
	source := "<!doctype html><html><body>" +
		strings.Repeat(paragraph, (3<<20)/len(paragraph)+1) +
		"</body></html>"
	if err := validateSetupSheetContent(context.Background(), mediaTypeHTML, strings.NewReader(source)); err != nil {
		t.Fatal(err)
	}
}
