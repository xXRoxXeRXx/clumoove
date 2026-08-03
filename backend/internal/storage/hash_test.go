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
