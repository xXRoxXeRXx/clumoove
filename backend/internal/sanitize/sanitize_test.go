package sanitize

import (
	"strings"
	"testing"
)

func TestSanitizeFilename_ForbiddenChars(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		provider string
		expected string
		reason   string
	}{
		{
			name:     "SMB colon",
			input:    "Bericht: 2026.pdf",
			provider: "smb",
			expected: "Bericht_ 2026.pdf",
			reason:   "forbidden_char",
		},
		{
			name:     "SMB multiple forbidden chars",
			input:    "file<>name|test.doc",
			provider: "smb",
			expected: "file__name_test.doc",
			reason:   "forbidden_char",
		},
		{
			name:     "SMB question mark and asterisk",
			input:    "what?is*this.txt",
			provider: "smb",
			expected: "what_is_this.txt",
			reason:   "forbidden_char",
		},
		{
			name:     "SMB double quote",
			input:    `say"hello".txt`,
			provider: "smb",
			expected: "say_hello_.txt",
			reason:   "forbidden_char",
		},
		{
			name:     "SMB backslash",
			input:    `path\file.txt`,
			provider: "smb",
			expected: "path_file.txt",
			reason:   "forbidden_char",
		},
		{
			name:     "Dropbox slash",
			input:    "dir/file.txt",
			provider: "dropbox",
			expected: "dir_file.txt",
			reason:   "forbidden_char",
		},
		{
			name:     "Google slash",
			input:    "a/b.txt",
			provider: "google",
			expected: "a_b.txt",
			reason:   "forbidden_char",
		},
		{
			name:     "Nextcloud no forbidden",
			input:    "normal.pdf",
			provider: "nextcloud",
			expected: "normal.pdf",
		},
		{
			name:     "WebDAV no forbidden",
			input:    "report:2026.pdf",
			provider: "webdav",
			expected: "report:2026.pdf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeFilename(tt.input, tt.provider)
			if result.SanitizedName != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result.SanitizedName)
			}
			if tt.reason != "" && !result.Changed {
				t.Error("expected Changed=true")
			}
			if tt.reason != "" && !containsReason(result.Reasons, tt.reason) {
				t.Errorf("expected reason %q, got %v", tt.reason, result.Reasons)
			}
			if tt.reason == "" && result.Changed {
				t.Errorf("expected no change, but got Changed=true with %v", result.Reasons)
			}
		})
	}
}

func TestSanitizeFilename_ReservedNames(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"aux.txt", "_aux.txt"},
		{"CON.log", "_CON.log"},
		{"prn.dat", "_prn.dat"},
		{"NUL", "_NUL"},
		{"com1.txt", "_com1.txt"},
		{"LPT9.doc", "_LPT9.doc"},
		{"normal.txt", "normal.txt"},
		{"auxiliary.txt", "auxiliary.txt"},
		{"con.foo", "_con.foo"},
		{"AUX.bar", "_AUX.bar"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := SanitizeFilename(tt.input, "smb")
			if result.SanitizedName != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result.SanitizedName)
			}
		})
	}
}

func TestSanitizeFilename_LengthTruncation(t *testing.T) {
	longName := strings.Repeat("a", 252) + ".pdf"
	result := SanitizeFilename(longName, "smb")
	if len(result.SanitizedName) > 255 {
		t.Errorf("expected length <= 255, got %d", len(result.SanitizedName))
	}
	if !strings.HasSuffix(result.SanitizedName, ".pdf") {
		t.Error("expected extension .pdf to be preserved")
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}
	if !containsReason(result.Reasons, "length_truncated") {
		t.Errorf("expected reason length_truncated, got %v", result.Reasons)
	}
}

func TestSanitizeFilename_S3LongName(t *testing.T) {
	longName := strings.Repeat("b", 1020) + ".txt"
	result := SanitizeFilename(longName, "s3")
	if result.Changed {
		t.Error("expected no change for S3 name within 1024 limit")
	}
}

func TestSanitizeFilename_EmptyName(t *testing.T) {
	result := SanitizeFilename("", "smb")
	if result.SanitizedName != "unnamed_file" {
		t.Errorf("expected unnamed_file, got %q", result.SanitizedName)
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}
}

func TestSanitizeFilename_TrailingSpacesAndDots(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"file.txt ", "file.txt"},
		{"file.txt.", "file.txt"},
		{"file.txt. ", "file.txt"},
		{"file .txt", "file .txt"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := SanitizeFilename(tt.input, "smb")
			if result.SanitizedName != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result.SanitizedName)
			}
		})
	}
}

func TestSanitizeFilename_MultipleIssues(t *testing.T) {
	result := SanitizeFilename("CON:file?.txt", "smb")
	if result.SanitizedName != "_CON_file_.txt" {
		t.Errorf("expected _CON_file_.txt, got %q", result.SanitizedName)
	}
	if !containsReason(result.Reasons, "forbidden_char") {
		t.Error("expected forbidden_char reason")
	}
	if !containsReason(result.Reasons, "reserved_name") {
		t.Error("expected reserved_name reason")
	}
}

func TestSanitizeFilename_NoChange(t *testing.T) {
	result := SanitizeFilename("normal.pdf", "nextcloud")
	if result.Changed {
		t.Error("expected no change")
	}
	if result.SanitizedName != "normal.pdf" {
		t.Errorf("expected normal.pdf, got %q", result.SanitizedName)
	}
}

func TestIsCaseInsensitive(t *testing.T) {
	if !IsCaseInsensitive("dropbox") {
		t.Error("dropbox should be case-insensitive")
	}
	if !IsCaseInsensitive("google") {
		t.Error("google should be case-insensitive")
	}
	if !IsCaseInsensitive("smb") {
		t.Error("smb should be case-insensitive")
	}
	if IsCaseInsensitive("nextcloud") {
		t.Error("nextcloud should be case-sensitive")
	}
	if IsCaseInsensitive("webdav") {
		t.Error("webdav should be case-sensitive")
	}
	if IsCaseInsensitive("sftp") {
		t.Error("sftp should be case-sensitive")
	}
}

func TestGetForbiddenChars(t *testing.T) {
	smbChars := GetForbiddenChars("smb")
	if len(smbChars) != 9 {
		t.Errorf("expected 9 forbidden chars for SMB, got %d", len(smbChars))
	}

	dropboxChars := GetForbiddenChars("dropbox")
	if len(dropboxChars) != 1 || dropboxChars[0] != '/' {
		t.Errorf("expected [/] for dropbox, got %v", dropboxChars)
	}

	s3Chars := GetForbiddenChars("s3")
	if s3Chars != nil {
		t.Errorf("expected nil for s3, got %v", s3Chars)
	}
}

func containsReason(reasons []string, reason string) bool {
	for _, r := range reasons {
		if r == reason {
			return true
		}
	}
	return false
}
