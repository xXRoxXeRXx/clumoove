package storage

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

var (
	ErrInvalidByteRange   = errors.New("invalid byte range")
	ErrRangeNotSatisfied  = errors.New("range request not satisfied")
	ErrRangeHeaderMismatch = errors.New("range response did not match request")
)

// exactRangedReadCloser keeps ownership of the underlying provider stream while
// exposing exactly the requested byte window and failing with ErrUnexpectedEOF on truncation.
type exactRangedReadCloser struct {
	rc        io.ReadCloser
	remaining int64
}

func (r *exactRangedReadCloser) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.rc.Read(p)
	r.remaining -= int64(n)
	if err == io.EOF && r.remaining > 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func (r *exactRangedReadCloser) Close() error {
	return r.rc.Close()
}

// newRangedReadCloser wraps an io.ReadCloser with an exactRangedReadCloser for length
// bytes while ensuring Close() still closes the underlying stream.
func newRangedReadCloser(rc io.ReadCloser, length int64) io.ReadCloser {
	return &exactRangedReadCloser{
		rc:        rc,
		remaining: length,
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

func parseContentRange(cr string) (start, end int64, err error) {
	cr = strings.TrimSpace(cr)
	if !strings.HasPrefix(strings.ToLower(cr), "bytes ") {
		return 0, 0, fmt.Errorf("invalid range unit in Content-Range: %q", cr)
	}
	spec := strings.TrimSpace(cr[6:])
	parts := strings.SplitN(spec, "/", 2)
	rangePart := parts[0]
	dashIdx := strings.IndexByte(rangePart, '-')
	if dashIdx <= 0 || dashIdx == len(rangePart)-1 {
		return 0, 0, fmt.Errorf("invalid range span in Content-Range: %q", cr)
	}
	s, err := strconv.ParseInt(rangePart[:dashIdx], 10, 64)
	if err != nil || s < 0 {
		return 0, 0, fmt.Errorf("invalid range start in Content-Range: %q", cr)
	}
	e, err := strconv.ParseInt(rangePart[dashIdx+1:], 10, 64)
	if err != nil || e < s {
		return 0, 0, fmt.Errorf("invalid range end in Content-Range: %q", cr)
	}
	return s, e, nil
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

	cr := resp.Header.Get("Content-Range")
	if cr == "" {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: missing Content-Range header", ErrRangeHeaderMismatch)
	}
	crStart, crEnd, err := parseContentRange(cr)
	if err != nil || crStart != offset || crEnd != end {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: Content-Range %q does not match requested range %d-%d", ErrRangeHeaderMismatch, cr, offset, end)
	}

	if resp.ContentLength >= 0 && resp.ContentLength != length {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: Content-Length %d does not match requested length %d", ErrRangeHeaderMismatch, resp.ContentLength, length)
	}

	return newRangedReadCloser(resp.Body, length), nil
}
