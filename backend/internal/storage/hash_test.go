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

func TestIsComparableHash(t *testing.T) {
	tests := []struct {
		algo string
		want bool
	}{
		{"SHA1", true},
		{"sha1", true},
		{"SHA256", true},
		{"MD5", true},
		{"SHA512", true},
		{"DROPBOX", true},
		{"HIDRIVE", true},
		{"QUICKXOR", true},
		{"ETAG", false},
		{"ETAG_MATCH", false},
		{"UNKNOWN", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.algo, func(t *testing.T) {
			if got := IsComparableHash(tc.algo); got != tc.want {
				t.Errorf("IsComparableHash(%q) = %v, want %v", tc.algo, got, tc.want)
			}
		})
	}
}

func TestBestSourceHash(t *testing.T) {
	cases := []struct {
		name       string
		sourceHash string
		workerHash string
		want       string
	}{
		{"prefer cryptographic source hash over worker hash", "SHA1:def987654321", "SHA1:abc123456789", "SHA1:def987654321"},
		{"prefer cryptographic source hash if worker hash is etag", "SHA256:fedcba9876543210", `ETAG:"etag123"`, "SHA256:fedcba9876543210"},
		{"fallback to cryptographic worker hash if source hash is empty", "", "MD5:0123456789abcdef", "MD5:0123456789abcdef"},
		{"prefer source etag if both are etags", `ETAG:"source-etag"`, `ETAG:"worker-etag"`, `ETAG:"source-etag"`},
		{"fallback to worker etag if source hash is empty", "", `ETAG:"worker-etag"`, `ETAG:"worker-etag"`},
		{"both empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BestSourceHash(c.sourceHash, c.workerHash); got != c.want {
				t.Errorf("BestSourceHash(%q, %q) = %q, want %q", c.sourceHash, c.workerHash, got, c.want)
			}
		})
	}
}

func TestCompareTargetHash(t *testing.T) {
	cases := []struct {
		name       string
		sourceHash string
		workerHash string
		targetHash string
		want       HashComparison
	}{
		{
			name:       "comparable match",
			sourceHash: "SHA1:ABCDEF",
			targetHash: "SHA-1:abcdef",
			want: HashComparison{
				HasSource: true, SourceAlgo: "SHA1", SourceValue: "abcdef",
				TargetAlgo: "SHA1", TargetValue: "abcdef",
				Comparable: true, Matched: true,
			},
		},
		{
			name:       "comparable mismatch",
			sourceHash: "SHA1:aaaaaa",
			targetHash: "SHA1:bbbbbb",
			want: HashComparison{
				HasSource: true, SourceAlgo: "SHA1", SourceValue: "aaaaaa",
				TargetAlgo: "SHA1", TargetValue: "bbbbbb",
				Comparable: true, Matched: false,
			},
		},
		{
			name:       "worker hash overrides differently-algorithmed source hash",
			sourceHash: "MD5:source-md5",
			workerHash: "SHA1:abcdef",
			targetHash: "SHA1:abcdef",
			want: HashComparison{
				HasSource: true, SourceAlgo: "SHA1", SourceValue: "abcdef",
				TargetAlgo: "SHA1", TargetValue: "abcdef",
				Comparable: true, Matched: true,
			},
		},
		{
			name:       "different algorithms without matching worker hash",
			sourceHash: "SHA1:source-sha1",
			targetHash: "MD5:target-md5",
			want: HashComparison{
				HasSource: true, SourceAlgo: "SHA1", SourceValue: "source-sha1",
				TargetAlgo: "MD5", TargetValue: "target-md5",
				Comparable: false, Matched: false,
			},
		},
		{
			name:       "no source hash",
			targetHash: "SHA1:target-sha1",
			want: HashComparison{
				HasSource: false,
				TargetAlgo: "SHA1", TargetValue: "target-sha1",
				Comparable: false, Matched: false,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CompareTargetHash(c.sourceHash, c.workerHash, c.targetHash); got != c.want {
				t.Fatalf("CompareTargetHash() = %+v, want %+v", got, c.want)
			}
		})
	}
}
