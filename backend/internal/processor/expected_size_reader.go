package processor

import (
	"fmt"
	"io"
)

// expectedSizeReader makes a clean early EOF a transfer error. Providers that
// stream until EOF otherwise accept a truncated source as a successful upload.
// VerifyComplete must be called after the upload to also reject a source that
// grew after it was indexed.
type expectedSizeReader struct {
	r        io.Reader
	expected int64
	read     int64
	complete bool
}

func newExpectedSizeReader(r io.Reader, expected int64) *expectedSizeReader {
	return &expectedSizeReader{r: r, expected: expected}
}

func (r *expectedSizeReader) Read(p []byte) (int, error) {
	if r.read >= r.expected {
		return 0, io.EOF
	}
	remaining := r.expected - r.read
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.r.Read(p)
	r.read += int64(n)
	if r.read < r.expected && err == io.EOF {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

// VerifyComplete confirms that exactly expected bytes were consumed and that
// the underlying source ends there. It is idempotent after a successful
// confirmation; callers must treat an error as terminal and not retry this
// reader because a failed probe may have consumed a source byte.
func (r *expectedSizeReader) VerifyComplete() error {
	if r.read != r.expected {
		return fmt.Errorf("source stream size mismatch: read %d bytes, expected %d", r.read, r.expected)
	}
	if r.complete {
		return nil
	}

	var probe [1]byte
	n, err := r.r.Read(probe[:])
	if n > 0 {
		return fmt.Errorf("source stream size mismatch: read more than expected %d bytes", r.expected)
	}
	if err == io.EOF {
		r.complete = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to confirm source stream length: %w", err)
	}
	return fmt.Errorf("failed to confirm source stream length: reader returned no data")
}
