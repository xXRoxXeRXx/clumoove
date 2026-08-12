package storage

import "testing"

func TestParseHashStringPreservesQuickXorBase64Case(t *testing.T) {
	algo, got := ParseHashString("QUICKXOR:Aa+/Zz==")
	if algo != "QUICKXOR" || got != "Aa+/Zz==" {
		t.Fatalf("ParseHashString() = %q, %q", algo, got)
	}
}

func TestParseHashStringNormalizesHexCase(t *testing.T) {
	algo, got := ParseHashString("SHA1:ABCDEF")
	if algo != "SHA1" || got != "abcdef" {
		t.Fatalf("ParseHashString() = %q, %q", algo, got)
	}
}

func TestParseHashString(t *testing.T) {
	tests := []struct {
		input, algorithm, value string
	}{
		{`"SHA-256:ABCDEF"`, "SHA256", "abcdef"},
		{"SHA-1:ABCDEF", "SHA1", "abcdef"},
		{"MD-5:ABCDEF", "MD5", "abcdef"},
		{"QUICKXORHASH:Aa+/Zz==", "QUICKXOR", "Aa+/Zz=="},
		{"HIDRIVE:ABCDEF", "HIDRIVE", "abcdef"},
		{"DROPBOX:ABCDEF", "DROPBOX", "abcdef"},
		{"0123456789abcdef0123456789abcdef", "UNKNOWN", "0123456789abcdef0123456789abcdef"},
		{`"SHA1:ABCDEF`, "UNKNOWN", `"SHA1:ABCDEF`},
		{"", "UNKNOWN", ""},
		{`""`, "UNKNOWN", ""},
		{"opaque-etag", "UNKNOWN", "opaque-etag"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			algorithm, value := ParseHashString(tt.input)
			if algorithm != tt.algorithm || value != tt.value {
				t.Fatalf("ParseHashString(%q) = %q, %q; want %q, %q", tt.input, algorithm, value, tt.algorithm, tt.value)
			}
		})
	}
}
