package storage

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var (
	ErrInvalidByteRange   = errors.New("invalid byte range")
	ErrRangeNotSatisfied  = errors.New("range request not satisfied")
	ErrRangeHeaderMismatch = errors.New("range response did not match request")
)

// rangedReadCloser keeps ownership of the underlying provider stream while
// exposing exactly the requested byte window.
type rangedReadCloser struct {
	io.Reader
	io.Closer
}

// newRangedReadCloser wraps an io.ReadCloser with an io.LimitReader for length
// bytes while ensuring Close() still closes the underlying stream.
func newRangedReadCloser(rc io.ReadCloser, length int64) io.ReadCloser {
	return &rangedReadCloser{
		Reader: io.LimitReader(rc, length),
		Closer: rc,
	}
}

// ValidateByteRange validates offset and length for Range requests.
func ValidateByteRange(offset, length int64) (int64, error) {
	if offset < 0 || length <= 0 || offset > (1<<63-1)-length {
		return 0, ErrInvalidByteRange
	}
	return offset + length - 1, nil
}

// FormatByteRangeHeader returns the HTTP standard Range header value: "bytes={offset}-{end}".
func FormatByteRangeHeader(offset, length int64) (string, error) {
	end, err := ValidateByteRange(offset, length)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("bytes=%d-%d", offset, end), nil
}

// ValidateHTTPRangeResponse verifies that an HTTP response strictly satisfied
// the requested byte range with a 206 Partial Content status and matching headers.
// Any 200 OK response or mismatched Content-Range fails closed to prevent data corruption.
func ValidateHTTPRangeResponse(resp *http.Response, offset, length int64) (io.ReadCloser, error) {
	if resp == nil {
		return nil, errors.New("nil HTTP response")
	}
	if resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil, fmt.Errorf("%w: server returned 200 OK instead of 206 Partial Content", ErrRangeHeaderMismatch)
		}
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return nil, ErrRangeNotSatisfied
		}
		return nil, fmt.Errorf("%w: unexpected HTTP status %d", ErrRangeHeaderMismatch, resp.StatusCode)
	}

	end, err := ValidateByteRange(offset, length)
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}

	expectedPrefix := fmt.Sprintf("bytes %d-%d/", offset, end)
	expectedPrefixNoTotal := fmt.Sprintf("bytes %d-%d", offset, end)
	cr := resp.Header.Get("Content-Range")
	if cr == "" || (!strings.HasPrefix(cr, expectedPrefix) && cr != expectedPrefixNoTotal) {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: Content-Range %q does not match expected range %d-%d", ErrRangeHeaderMismatch, cr, offset, end)
	}

	if resp.ContentLength >= 0 && resp.ContentLength != length {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: Content-Length %d does not match requested length %d", ErrRangeHeaderMismatch, resp.ContentLength, length)
	}

	return newRangedReadCloser(resp.Body, length), nil
}
