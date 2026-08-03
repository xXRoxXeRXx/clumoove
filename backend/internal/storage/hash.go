package storage

import (
	"regexp"
	"strings"
)

var hexRegexp = regexp.MustCompile("^[0-9a-fA-F]+$")

// ParseHashString normalizes hashes obtained from any storage provider.
func ParseHashString(hashStr string) (string, string) {
	hashStr = strings.Trim(hashStr, "\"")
	parts := strings.SplitN(hashStr, ":", 2)
	if len(parts) == 2 {
		algo := strings.ToUpper(parts[0])
		switch algo {
		case "SHA-256", "SHA256":
			algo = "SHA256"
		case "SHA-1", "SHA1":
			algo = "SHA1"
		case "MD-5", "MD5":
			algo = "MD5"
		case "QUICKXOR", "QUICKXORHASH":
			algo = "QUICKXOR"
		}
		return algo, strings.ToLower(parts[1])
	}

	if hexRegexp.MatchString(hashStr) {
		switch len(hashStr) {
		case 32:
			return "MD5", strings.ToLower(hashStr)
		case 40:
			return "SHA1", strings.ToLower(hashStr)
		case 64:
			return "SHA256", strings.ToLower(hashStr)
		}
	}
	return "UNKNOWN", hashStr
}
