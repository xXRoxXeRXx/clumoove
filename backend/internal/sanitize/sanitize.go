package sanitize

import (
	"strings"
	"unicode/utf8"
)

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
	"smb":      {'\\', '/', ':', '*', '?', '"', '<', '>', '|'},
	"dropbox":  {'/'},
	"google":   {'/'},
	"nextcloud": {'/'},
	"webdav":   {'/'},
	"sftp":     {'/'},
}

var providerMaxLength = map[string]int{
	"smb":       255,
	"dropbox":   255,
	"google":    255,
	"nextcloud": 255,
	"webdav":    255,
	"sftp":      255,
	"s3":        1024,
}

var caseInsensitiveProviders = map[string]bool{
	"dropbox": true,
	"google":  true,
	"smb":     true,
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

	if targetProvider == "smb" {
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

	if targetProvider == "smb" {
		sanitized := trimWindowsTrailing(result.SanitizedName)
		if sanitized != result.SanitizedName {
			result.SanitizedName = sanitized
			result.Changed = true
			result.Reasons = append(result.Reasons, "trailing_chars")
		}
	}

	maxLen := 255
	if ml, ok := providerMaxLength[targetProvider]; ok {
		maxLen = ml
	}
	if utf8.RuneCountInString(result.SanitizedName) > maxLen {
		result.SanitizedName = truncatePreserveExt(result.SanitizedName, maxLen)
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

func truncatePreserveExt(name string, maxLen int) string {
	ext := ""
	base := name
	if idx := strings.LastIndex(name, "."); idx > 0 {
		ext = name[idx:]
		base = name[:idx]
	}

	extLen := utf8.RuneCountInString(ext)
	availableBase := maxLen - extLen
	if availableBase < 1 {
		availableBase = 1
	}

	runes := []rune(base)
	if len(runes) > availableBase {
		runes = runes[:availableBase]
	}
	return string(runes) + ext
}
