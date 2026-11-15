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

// IsComparableHash reports whether algo can be calculated and compared during
// streaming. QuickXor is intentionally included as OneDrive's native integrity signal.
func IsComparableHash(algo string) bool {
	switch strings.ToUpper(algo) {
	case "SHA1", "SHA256", "MD5", "SHA512", "DROPBOX", "HIDRIVE", "QUICKXOR":
		return true
	default:
		return false
	}
}

// BestSourceHash selects the strongest source-side integrity signal, preferring
// cryptographic hashes over ETags and indexed source hashes over worker-streamed hashes.
func BestSourceHash(sourceHash, workerHash string) string {
	if sourceHash != "" && !strings.HasPrefix(strings.ToUpper(sourceHash), "ETAG:") {
		return sourceHash
	}
	if workerHash != "" && !strings.HasPrefix(strings.ToUpper(workerHash), "ETAG:") {
		return workerHash
	}
	if sourceHash != "" {
		return sourceHash
	}
	if workerHash != "" {
		return workerHash
	}
	return ""
}

// HashComparison holds the normalized source and target checksum state
// produced by CompareTargetHash.
type HashComparison struct {
	HasSource   bool
	SourceAlgo  string
	SourceValue string
	TargetAlgo  string
	TargetValue string
	Comparable  bool
	Matched     bool
}

// CompareTargetHash selects the strongest source-side hash (promoting a streamed
// workerHash over sourceHash when it matches targetHash's native algorithm) and
// compares normalized cryptographic digests when both sides are comparable.
func CompareTargetHash(sourceHash, workerHash, targetHash string) HashComparison {
	targetAlgo, cleanTarget := ParseHashString(targetHash)
	srcHash := BestSourceHash(sourceHash, workerHash)
	if workerHash != "" {
		wAlgo, _ := ParseHashString(workerHash)
		sAlgo, _ := ParseHashString(srcHash)
		if wAlgo == targetAlgo && sAlgo != targetAlgo {
			srcHash = workerHash
		}
	}
	cmp := HashComparison{
		TargetAlgo:  targetAlgo,
		TargetValue: cleanTarget,
	}
	if srcHash == "" {
		return cmp
	}
	cmp.HasSource = true
	cmp.SourceAlgo, cmp.SourceValue = ParseHashString(srcHash)
	cmp.Comparable = IsComparableHash(cmp.SourceAlgo) && IsComparableHash(targetAlgo) && cmp.SourceAlgo == targetAlgo
	cmp.Matched = cmp.Comparable && cmp.SourceValue == cleanTarget
	return cmp
}
