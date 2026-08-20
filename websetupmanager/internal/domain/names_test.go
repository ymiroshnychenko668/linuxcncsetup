package domain

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func TestNameNormalizationUsesNFC(t *testing.T) {
	decomposed := "Cafe\u0301"
	setup, err := NormalizeSetupName(decomposed)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := NormalizeArtifactName(decomposed + ".ngc")
	if err != nil {
		t.Fatal(err)
	}
	if setup != "Café" || artifact != "Café.ngc" {
		t.Fatalf("normalization = %q, %q", setup, artifact)
	}
	if !norm.NFC.IsNormalString(setup) || !norm.NFC.IsNormalString(artifact) {
		t.Fatal("normalized names are not NFC")
	}
}

func TestNameCollisionKeysUseUnicodeCaseFolding(t *testing.T) {
	pairs := [][2]string{
		{"Straße.NGC", "STRASSE.ngc"},
		{"Cafe\u0301.ngc", "CAFÉ.NGC"},
		{"ΟΣ.ngc", "ος.NGC"},
		{"Σ.ngc", "ς.NGC"},
	}
	for _, pair := range pairs {
		left, err := ArtifactNameKey(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		right, err := ArtifactNameKey(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if left != right {
			t.Errorf("collision keys differ for %q and %q: %q != %q", pair[0], pair[1], left, right)
		}
	}
	setupLeft, err := SetupNameKey("Straße")
	if err != nil {
		t.Fatal(err)
	}
	setupRight, err := SetupNameKey("STRASSE")
	if err != nil {
		t.Fatal(err)
	}
	if setupLeft != setupRight {
		t.Fatalf("setup collision keys differ: %q != %q", setupLeft, setupRight)
	}
}

func TestSetupNameValidationRejectsUnsafeText(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	for name, value := range map[string]string{
		"empty":               "",
		"only whitespace":     " \t",
		"NUL":                 "bad\x00name",
		"newline":             "bad\nname",
		"control":             "bad\u001bname",
		"trailing dot":        "name.",
		"trailing space":      "name ",
		"trailing unicode ws": "name\u00a0",
		"too many runes":      strings.Repeat("x", MaxSetupNameRunes+1),
		"invalid UTF-8":       invalidUTF8,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSetupName(value); !IsErrorCode(err, CodeInvalidName) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	for _, valid := range []string{"Setup 17", "Деталь № 5", "Part A/B", " leading"} {
		if err := ValidateSetupName(valid); err != nil {
			t.Errorf("valid setup name %q rejected: %v", valid, err)
		}
	}
}

func TestArtifactNameValidationRequiresOneSafeBasename(t *testing.T) {
	tooManyBytes := strings.Repeat("é", MaxArtifactNameBytes/2+1) + ".ngc"
	if utf8.RuneCountInString(tooManyBytes) > MaxArtifactNameRunes {
		t.Fatal("test input should exercise byte limit, not rune limit")
	}
	for name, value := range map[string]string{
		"dot":            ".",
		"dot dot":        "..",
		"absolute":       "/etc/passwd",
		"traversal":      "../outside.ngc",
		"nested":         "folder/file.ngc",
		"backslash":      `folder\file.ngc`,
		"UNC":            `\\server\share.ngc`,
		"drive absolute": `C:\file.ngc`,
		"drive relative": `z:file.ngc`,
		"NUL":            "bad\x00.ngc",
		"control":        "bad\u007f.ngc",
		"trailing dot":   "program.ngc.",
		"trailing space": "program.ngc ",
		"too many bytes": tooManyBytes,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateArtifactName(value); !IsErrorCode(err, CodeInvalidName) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	// Percent sequences are literal name text. The HTTP stack performs its one
	// URL decode; the domain must not decode a second time (SEC-PATH-003).
	for _, valid := range []string{
		"main.ngc", "Деталь 5.NC", "two..dots.tap", "%2e%2e.ngc", "..%2foutside.ngc",
	} {
		if err := ValidateArtifactName(valid); err != nil {
			t.Errorf("valid basename %q rejected: %v", valid, err)
		}
	}
}
