package storage

import (
	"strings"
)

// escapeDAVPath percent-encodes a path segment or multi-segment path for use in
// a WebDAV/Nextcloud request URL (resource paths and usernames embedded in the
// URL path, e.g. /files/<user>/...). Go's url.URL leaves RFC 3986 sub-delimiters
// such as '&' and '+' unescaped in paths, but SabreDAV/Nextcloud fail to locate
// resources whose names contain those characters unless they are percent-encoded
// (e.g. '&' -> '%26'). We encode every byte that is not an unreserved character
// or the '/' separator (so multi-segment paths stay intact; usernames must not
// contain '/'). This is safe because callers always pass already-decoded values,
// so re-encoding never double-encodes (a path containing a literal '%' is
// correctly encoded to "%25" and decoded back by the server).
func escapeDAVPath(p string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); i++ {
		c := p[i]
		if isUnreserved(c) || c == '/' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

// isUnreserved reports whether c is an RFC 3986 unreserved character
// (ALPHA / DIGIT / "-" / "." / "_" / "~").
func isUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '-', '.', '_', '~':
		return true
	}
	return false
}
