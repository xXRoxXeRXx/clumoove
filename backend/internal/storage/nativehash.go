package storage

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"hash"
)

// NativeHash declares the content hash a provider natively reports or verifies,
// next to the rest of its catalogue registration. A registration that declares
// none streams SHA1, the default for providers with no native content hash.
type NativeHash struct {
	// Algorithm is the upper-case algorithm name recorded with a streamed hash.
	Algorithm string
	// New returns a fresh hasher on every call.
	New func() hash.Hash
	// FixedOnSource makes a transfer hash the source stream with this
	// algorithm whenever the provider is the source, whatever algorithm the
	// indexed listing hash used. It is false for providers whose listed hash
	// cannot be recomputed by streaming the file (HiDrive's chash) or whose
	// listing hash stays authoritative on the source side (S3).
	FixedOnSource bool
}

var defaultNativeHash = NativeHash{Algorithm: "SHA1", New: sha1.New}

var (
	nativeHashMD5      = NativeHash{Algorithm: "MD5", New: md5.New, FixedOnSource: true}
	nativeHashSHA256   = NativeHash{Algorithm: "SHA256", New: sha256.New}
	nativeHashDropbox  = NativeHash{Algorithm: "DROPBOX", New: func() hash.Hash { return NewDropboxHasher() }, FixedOnSource: true}
	nativeHashQuickXor = NativeHash{Algorithm: "QUICKXOR", New: func() hash.Hash { return NewQuickXorHasher() }, FixedOnSource: true}
	nativeHashHiDrive  = NativeHash{Algorithm: "HIDRIVE", New: NewHiDriveHasher}
)

// NativeHashFor returns the native hash declared by providerType's catalogue
// registration, or the SHA1 default for an undeclared or unknown provider.
func NativeHashFor(providerType string) NativeHash {
	return defaultProviderCatalogue.nativeHash(providerType)
}

// IgnoresListedSourceHash reports whether a transfer must disregard the
// indexed listing hash of providerType when it is the source, because that
// hash is not a content digest of the transferred bytes.
func IgnoresListedSourceHash(providerType string) bool {
	return defaultProviderCatalogue.ignoresListedSourceHash(providerType)
}

func (c *providerCatalogue) nativeHash(providerType string) NativeHash {
	entry, ok := c.lookup(providerType)
	if !ok || entry.NativeHash == nil || entry.NativeHash.New == nil {
		return defaultNativeHash
	}
	return *entry.NativeHash
}

func (c *providerCatalogue) ignoresListedSourceHash(providerType string) bool {
	entry, ok := c.lookup(providerType)
	return ok && entry.IgnoresListedSourceHash
}
