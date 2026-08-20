package domain

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewIDUsesCanonicalRandomHex(t *testing.T) {
	identifier, err := newIDFrom(bytes.NewReader([]byte{
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	}))
	if err != nil {
		t.Fatal(err)
	}
	const expected = "000102030405060708090a0b0c0d0e0f"
	if identifier != expected {
		t.Fatalf("NewID = %q, want %q", identifier, expected)
	}
	if err := ValidateID(identifier); err != nil {
		t.Fatalf("generated ID rejected: %v", err)
	}
	if _, err := newIDFrom(strings.NewReader("short")); err == nil {
		t.Fatal("short entropy read was accepted")
	}
}

func TestNewIDsAreUniqueAndEntityFactoriesAreCanonical(t *testing.T) {
	factories := []struct {
		name string
		new  func() (string, error)
	}{
		{"generic", NewID},
		{"setup", NewSetupID},
		{"artifact", NewArtifactID},
		{"storage object", NewStorageObjectID},
		{"validation", NewValidationRunID},
		{"job", NewJobID},
		{"import", NewImportID},
		{"audit", NewAuditEventID},
		{"operation", NewOperationID},
	}
	seen := make(map[string]struct{})
	for _, factory := range factories {
		for range 16 {
			identifier, err := factory.new()
			if err != nil {
				t.Fatalf("%s: %v", factory.name, err)
			}
			if !IsValidID(identifier) {
				t.Fatalf("%s returned invalid ID %q", factory.name, identifier)
			}
			if _, exists := seen[identifier]; exists {
				t.Fatalf("duplicate random ID %q", identifier)
			}
			seen[identifier] = struct{}{}
		}
	}
}

func TestValidateIDRejectsNonCanonicalRepresentations(t *testing.T) {
	valid := strings.Repeat("a", IDLength)
	for _, test := range []string{
		"",
		strings.Repeat("a", IDLength-1),
		strings.Repeat("a", IDLength+1),
		strings.Repeat("A", IDLength),
		strings.Repeat("g", IDLength),
		strings.Repeat("0", IDLength-1) + "/",
		strings.Repeat("0", IDLength-1) + "\x00",
		"../" + valid,
	} {
		t.Run(strings.ReplaceAll(test, "\x00", "NUL"), func(t *testing.T) {
			if IsValidID(test) {
				t.Fatalf("IsValidID(%q) = true", test)
			}
			if err := ValidateID(test); !IsErrorCode(err, CodeInvalidID) {
				t.Fatalf("ValidateID(%q) error = %v", test, err)
			}
		})
	}
}
