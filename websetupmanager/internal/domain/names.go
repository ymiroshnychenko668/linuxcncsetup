package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	// Setup names are display labels, not paths. The rune and byte limits keep
	// database, logs, and API responses bounded even for multi-byte input.
	MaxSetupNameRunes = 200
	MaxSetupNameBytes = 800

	// 255 bytes is the common NAME_MAX baseline for a single filesystem
	// component. Storage does not use this value as a physical object key.
	MaxArtifactNameRunes = 255
	MaxArtifactNameBytes = 255
)

var unicodeCaseFolder = cases.Fold()

// NormalizeSetupName validates a required display name and returns its NFC
// representation. A setup name is still treated as text and never as a path.
func NormalizeSetupName(name string) (string, error) {
	return normalizeName(name, MaxSetupNameRunes, MaxSetupNameBytes, false)
}

func ValidateSetupName(name string) error {
	_, err := NormalizeSetupName(name)
	return err
}

// NormalizeArtifactName accepts exactly one safe basename. It does not clean
// or repair paths because silently narrowing an attacker-supplied path can
// select a different object than the operator confirmed.
func NormalizeArtifactName(name string) (string, error) {
	return normalizeName(name, MaxArtifactNameRunes, MaxArtifactNameBytes, true)
}

func ValidateArtifactName(name string) error {
	_, err := NormalizeArtifactName(name)
	return err
}

// SetupNameKey produces a stable Unicode case-folded key for duplicate
// warnings. Setup display names themselves remain non-unique.
func SetupNameKey(name string) (string, error) {
	normalized, err := NormalizeSetupName(name)
	if err != nil {
		return "", err
	}
	return collisionKey(normalized), nil
}

// ArtifactNameKey produces the key used to reject semantically colliding
// artifact names inside one setup.
func ArtifactNameKey(name string) (string, error) {
	normalized, err := NormalizeArtifactName(name)
	if err != nil {
		return "", err
	}
	return collisionKey(normalized), nil
}

func collisionKey(normalized string) string {
	return norm.NFC.String(unicodeCaseFolder.String(norm.NFC.String(normalized)))
}

func normalizeName(name string, maxRunes, maxBytes int, basename bool) (string, error) {
	if !utf8.ValidString(name) {
		return "", invalidName("name is not valid UTF-8")
	}
	normalized := norm.NFC.String(name)
	if strings.TrimSpace(normalized) == "" {
		return "", invalidName("name must not be empty")
	}
	if len(normalized) > maxBytes || utf8.RuneCountInString(normalized) > maxRunes {
		return "", invalidName("name is too long")
	}
	for _, r := range normalized {
		if r == 0 || unicode.IsControl(r) {
			return "", invalidName("name contains a control character")
		}
	}
	last, _ := utf8.DecodeLastRuneInString(normalized)
	if last == '.' || unicode.IsSpace(last) {
		return "", invalidName("name must not end with a dot or whitespace")
	}
	if basename {
		if normalized == "." || normalized == ".." || strings.ContainsAny(normalized, `/\`) {
			return "", invalidName("artifact name must be a basename")
		}
		// Detect drive-relative and drive-absolute Windows volume paths even on
		// Linux, where filepath.VolumeName intentionally does not recognize them.
		if len(normalized) >= 2 && isASCIIAlpha(normalized[0]) && normalized[1] == ':' {
			return "", invalidName("artifact name must not be a volume path")
		}
	}
	return normalized, nil
}

func invalidName(message string) *Error {
	return NewError(CodeInvalidName, message)
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}
