package storage

import "strings"

// ParseHashString normalizes hashes obtained from any storage provider.
func ParseHashString(hashStr string) (string, string) {
	if strings.HasPrefix(hashStr, "\"") || strings.HasSuffix(hashStr, "\"") {
		if len(hashStr) < 2 || hashStr[0] != '"' || hashStr[len(hashStr)-1] != '"' {
			return "UNKNOWN", hashStr
		}
		hashStr = hashStr[1 : len(hashStr)-1]
	}
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
		// Provider hashes are conventionally hex and case-insensitive, except
		// QuickXor, which Microsoft Graph returns as case-sensitive base64.
		hashValue := parts[1]
		if algo != "QUICKXOR" {
			hashValue = strings.ToLower(hashValue)
		}
		return algo, hashValue
	}

	return "UNKNOWN", hashStr
}
