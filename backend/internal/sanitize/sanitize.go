package sanitize

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var credURLRe = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s@?#]+@`)
var credQueryRe = regexp.MustCompile(`(?i)(\b(?:base_url|access_token|token|api_key|apikey|key|secret|password|passwd|pwd|client_secret|refresh_token|auth|signature)=)[^&\s]+`)

// SanitizeError redacts credentials from any URLs embedded in an error message.
// It strips user:pass userinfo and credential-bearing query values.
func SanitizeError(msg string) string {
	msg = credURLRe.ReplaceAllString(msg, "${1}***:***@")
	return credQueryRe.ReplaceAllString(msg, "${1}***")
}

type SanitizeResult struct {
	OriginalName  string
	SanitizedName string
	Changed       bool
	Reasons       []string
}

var reservedWindowsNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

var providerForbiddenChars = map[string][]rune{
	"smb":          {'\\', '/', ':', '*', '?', '"', '<', '>', '|'},
	"onedrive":     {'\\', '/', ':', '*', '?', '"', '<', '>', '|'},
	"dropbox":      {'/'},
	"google":       {'/'},
	"nextcloud":    {'/'},
	"opencloud":    {'/'},
	"magentacloud": {'/'},
	"webdav":       {'/'},
	"sftp":         {'/'},
	"ftp":          {'/'},
	"hidrive":      {'/'},
	"local":        {'/'},
	"seafile":      {'/'},
	"mega":         {'/'},
	"koofr":        {'\\', '/'},
}

var providerMaxLength = map[string]int{
	"smb":          255,
	"onedrive":     255,
	"dropbox":      255,
	"google":       255,
	"hidrive":      255,
	"nextcloud":    255,
	"magentacloud": 255,
	"webdav":       255,
	"sftp":         255,
	"seafile":      255,
	"mega":         255, // MEGA limits each path segment to 255 Unicode runes.
	"s3":           1024,
}

var providerMaxPathLength = map[string]int{
	"hidrive":  1020,
	"onedrive": 400,
}

func GetMaxPathLength(provider string) int {
	if ml, ok := providerMaxPathLength[provider]; ok {
		return ml
	}
	return 4096
}

// IsPathTooLong reports whether path exceeds targetProvider's maximum allowed
// path length. OneDrive specifies its 400-character limit in Unicode
// characters, while HiDrive's ext4-derived limit is measured in UTF-8 bytes.
func IsPathTooLong(path string, targetProvider string) bool {
	if targetProvider == "onedrive" {
		return utf8.RuneCountInString(path) > GetMaxPathLength(targetProvider)
	}
	return len(path) > GetMaxPathLength(targetProvider)
}

// Case-insensitive providers. Note: HiDrive runs on ext4 (Linux) and is
// case-sensitive, so it is intentionally omitted from caseInsensitiveProviders.
var caseInsensitiveProviders = map[string]bool{
	"dropbox":  true,
	"google":   true,
	"smb":      true,
	"onedrive": true,
	"koofr":    true,
}

func IsCaseInsensitive(provider string) bool {
	return caseInsensitiveProviders[provider]
}

func GetForbiddenChars(provider string) []rune {
	if chars, ok := providerForbiddenChars[provider]; ok {
		return chars
	}
	return nil
}

func SanitizeFilename(name string, targetProvider string) SanitizeResult {
	result := SanitizeResult{
		OriginalName:  name,
		SanitizedName: name,
	}

	if name == "" {
		result.SanitizedName = "unnamed_file"
		result.Changed = true
		result.Reasons = append(result.Reasons, "empty_name")
		return result
	}

	if targetProvider == "smb" || targetProvider == "onedrive" {
		sanitized := sanitizeWindowsReserved(result.SanitizedName)
		if sanitized != result.SanitizedName {
			result.SanitizedName = sanitized
			result.Changed = true
			result.Reasons = append(result.Reasons, "reserved_name")
		}
	}

	forbidden := GetForbiddenChars(targetProvider)
	if len(forbidden) > 0 {
		sanitized := replaceForbidden(result.SanitizedName, forbidden)
		if sanitized != result.SanitizedName {
			result.SanitizedName = sanitized
			result.Changed = true
			result.Reasons = append(result.Reasons, "forbidden_char")
		}
	}

	// Some Seafile deployments reject Unicode symbol characters (including
	// emoji) in file and directory names. Replace them before creating target
	// paths so a single unsupported source name does not fail a migration.
	if targetProvider == "seafile" {
		sanitized := replaceUnicodeSymbols(result.SanitizedName)
		if sanitized != result.SanitizedName {
			result.SanitizedName = sanitized
			result.Changed = true
			result.Reasons = append(result.Reasons, "unsupported_unicode_symbol")
		}
	}

	if targetProvider == "smb" || targetProvider == "onedrive" {
		sanitized := trimWindowsTrailing(result.SanitizedName)
		if sanitized != result.SanitizedName {
			result.SanitizedName = sanitized
			result.Changed = true
			result.Reasons = append(result.Reasons, "trailing_chars")
		}
	}

	maxLen := getMaxFilenameLength(targetProvider)
	if filenameLength(result.SanitizedName, targetProvider) > maxLen {
		result.SanitizedName = truncatePreserveExt(result.SanitizedName, maxLen, targetProvider)
		result.Changed = true
		result.Reasons = append(result.Reasons, "length_truncated")
	}

	if result.SanitizedName == "" {
		result.SanitizedName = "unnamed_file"
		result.Changed = true
		result.Reasons = append(result.Reasons, "empty_after_sanitize")
	}

	return result
}

func getMaxFilenameLength(provider string) int {
	if maxLength, ok := providerMaxLength[provider]; ok {
		return maxLength
	}
	return 255
}

func filenameLength(name, provider string) int {
	if provider == "hidrive" {
		return len(name)
	}
	return utf8.RuneCountInString(name)
}

func truncateFilename(name string, maxLen int, provider string) string {
	if maxLen <= 0 {
		return ""
	}
	if provider != "hidrive" {
		runes := []rune(name)
		if len(runes) > maxLen {
			runes = runes[:maxLen]
		}
		return string(runes)
	}
	if len(name) <= maxLen {
		return name
	}
	end := maxLen
	for end > 0 && end < len(name) && !utf8.RuneStart(name[end]) {
		end--
	}
	return name[:end]
}

func replaceForbidden(name string, forbidden []rune) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if isForbidden(r, forbidden) {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func replaceUnicodeSymbols(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	replacedSymbol := false
	for _, r := range name {
		if unicode.Is(unicode.So, r) {
			b.WriteRune('_')
			replacedSymbol = true
			continue
		}
		// Emoji variation selectors and zero-width joiners only have meaning
		// with a preceding symbol. Keeping them after that symbol is replaced
		// leaves invalid-looking filename fragments on some Seafile servers.
		if replacedSymbol && (unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Cf, r)) {
			continue
		}
		b.WriteRune(r)
		replacedSymbol = false
	}
	return b.String()
}

func isForbidden(r rune, forbidden []rune) bool {
	for _, f := range forbidden {
		if r == f {
			return true
		}
	}
	return false
}

func sanitizeWindowsReserved(name string) string {
	base := name
	if idx := strings.Index(name, "."); idx > 0 {
		base = name[:idx]
	}

	lowerBase := strings.ToLower(base)
	if reservedWindowsNames[lowerBase] {
		return "_" + name
	}

	for reserved := range reservedWindowsNames {
		if strings.HasPrefix(lowerBase, reserved) && len(lowerBase) > len(reserved) {
			next := rune(lowerBase[len(reserved)])
			if isForbidden(next, []rune{'\\', '/', ':', '*', '?', '"', '<', '>', '|'}) {
				return "_" + name
			}
		}
	}

	return name
}

func trimWindowsTrailing(name string) string {
	trimmed := strings.TrimRight(name, " .")
	if trimmed == "" {
		return name
	}
	return trimmed
}

func truncatePreserveExt(name string, maxLen int, provider string) string {
	if maxLen <= 0 {
		return ""
	}

	ext := ""
	base := name
	if idx := strings.LastIndex(name, "."); idx > 0 {
		ext = name[idx:]
		base = name[:idx]
	}

	extLen := filenameLength(ext, provider)
	if extLen >= maxLen {
		return truncateFilename(name, maxLen, provider)
	}

	availableBase := maxLen - extLen
	return truncateFilename(base, availableBase, provider) + ext
}
