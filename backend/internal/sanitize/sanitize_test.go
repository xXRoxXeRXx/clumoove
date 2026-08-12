package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
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
			name:     "OneDrive Windows restrictions",
			input:    "CON: report?.txt ",
			provider: "onedrive",
			expected: "_CON_ report_.txt",
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
			name:     "MEGA slash",
			input:    "folder/file.txt",
			provider: "mega",
			expected: "folder_file.txt",
			reason:   "forbidden_char",
		},
		{
			name:     "Seafile emoji",
			input:    "🚀 Internal Projects & Campaign Planning",
			provider: "seafile",
			expected: "_ Internal Projects & Campaign Planning",
			reason:   "unsupported_unicode_symbol",
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
	if utf8.RuneCountInString(result.SanitizedName) > 255 {
		t.Errorf("expected length <= 255 runes, got %d", utf8.RuneCountInString(result.SanitizedName))
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

func TestSanitizeFilename_HiDriveLengthTruncation(t *testing.T) {
	longName := strings.Repeat("a", 252) + ".pdf" // 256 chars total
	result := SanitizeFilename(longName, "hidrive")
	if len(result.SanitizedName) > 255 {
		t.Errorf("expected HiDrive filename length <= 255 bytes, got %d", len(result.SanitizedName))
	}
	if !strings.HasSuffix(result.SanitizedName, ".pdf") {
		t.Error("expected extension .pdf to be preserved")
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}
	if GetMaxPathLength("hidrive") != 1020 {
		t.Errorf("expected HiDrive max path length to be 1020, got %d", GetMaxPathLength("hidrive"))
	}
	if !IsPathTooLong(strings.Repeat("a", 1021), "hidrive") {
		t.Error("expected IsPathTooLong=true for 1021 chars on hidrive")
	}
	if IsPathTooLong(strings.Repeat("a", 1020), "hidrive") {
		t.Error("expected IsPathTooLong=false for 1020 chars on hidrive")
	}
	unicodeName := SanitizeFilename(strings.Repeat("é", 255), "hidrive")
	if len(unicodeName.SanitizedName) > 255 {
		t.Errorf("expected HiDrive Unicode filename length <= 255 bytes, got %d", len(unicodeName.SanitizedName))
	}
	if !unicodeName.Changed || !containsReason(unicodeName.Reasons, "length_truncated") {
		t.Errorf("expected HiDrive Unicode name to be truncated, got %+v", unicodeName)
	}
}

func TestSanitizeFilename_LongExtensionIsBounded(t *testing.T) {
	longName := "a" + strings.Repeat(".x", 200)
	result := SanitizeFilename(longName, "smb")
	if got := utf8.RuneCountInString(result.SanitizedName); got > 255 {
		t.Errorf("expected length <= 255 runes, got %d", got)
	}
	if !result.Changed || !containsReason(result.Reasons, "length_truncated") {
		t.Errorf("expected long extension to be truncated, got %+v", result)
	}
}

func TestSanitizeFilename_MegaLengthTruncation(t *testing.T) {
	longName := strings.Repeat("a", 252) + ".pdf"
	result := SanitizeFilename(longName, "mega")
	if utf8.RuneCountInString(result.SanitizedName) > 255 {
		t.Errorf("expected MEGA filename length <= 255 runes, got %d", utf8.RuneCountInString(result.SanitizedName))
	}
	if !strings.HasSuffix(result.SanitizedName, ".pdf") {
		t.Error("expected extension .pdf to be preserved")
	}
	if !containsReason(result.Reasons, "length_truncated") {
		t.Errorf("expected reason length_truncated, got %v", result.Reasons)
	}
}

func TestOneDriveLimitsAndCaseSensitivity(t *testing.T) {
	if GetMaxPathLength("onedrive") != 400 {
		t.Errorf("OneDrive path length = %d, want 400", GetMaxPathLength("onedrive"))
	}
	if !IsPathTooLong(strings.Repeat("a", 401), "onedrive") {
		t.Error("expected 401-character OneDrive path to be too long")
	}
	if !IsCaseInsensitive("onedrive") {
		t.Error("onedrive should be case-insensitive")
	}
	if IsPathTooLong(strings.Repeat("é", 300), "onedrive") {
		t.Error("expected 300 Unicode characters to fit in OneDrive's 400-character limit")
	}
	if !IsPathTooLong(strings.Repeat("é", 401), "onedrive") {
		t.Error("expected 401 Unicode characters to exceed OneDrive's 400-character limit")
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

	megaChars := GetForbiddenChars("mega")
	if len(megaChars) != 1 || megaChars[0] != '/' {
		t.Errorf("expected [/] for mega, got %v", megaChars)
	}

	s3Chars := GetForbiddenChars("s3")
	if s3Chars != nil {
		t.Errorf("expected nil for s3, got %v", s3Chars)
	}

	for _, provider := range []string{"opencloud", "hidrive", "local", "ftp"} {
		chars := GetForbiddenChars(provider)
		if len(chars) != 1 || chars[0] != '/' {
			t.Errorf("expected [/] for %s, got %v", provider, chars)
		}
	}
}

func TestSanitizeFilename_SeafileEmojiSequence(t *testing.T) {
	result := SanitizeFilename("copyright\u00a9\ufe0f-family\U0001f468\u200d\U0001f469\u200d\U0001f467", "seafile")
	if result.SanitizedName != "copyright_-family___" {
		t.Errorf("expected symbols and their selectors to be replaced, got %q", result.SanitizedName)
	}
	if strings.ContainsRune(result.SanitizedName, '\ufe0f') || strings.ContainsRune(result.SanitizedName, '\u200d') {
		t.Errorf("expected no orphaned emoji selectors, got %q", result.SanitizedName)
	}
}

func TestSanitizeErrorRedactsCredentialsWithoutCorruptingOtherParameters(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://bearer-token@example.test/path", "https://***:***@example.test/path"},
		{"https://user%3Apass@example.test/path", "https://***:***@example.test/path"},
		{"https://files.example?owner=a@b", "https://files.example?owner=a@b"},
		{"request failed: https://example.test/?api_key=secret-value&mytoken=preserve", "request failed: https://example.test/?api_key=***&mytoken=preserve"},
		{"https://example.test/?client_secret=a&refresh_token=b&signature=c", "https://example.test/?client_secret=***&refresh_token=***&signature=***"},
	}
	for _, test := range tests {
		if got := SanitizeError(test.input); got != test.want {
			t.Errorf("SanitizeError(%q) = %q, want %q", test.input, got, test.want)
		}
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
