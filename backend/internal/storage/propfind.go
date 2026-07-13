package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// maxListAttempts is the number of times a single PROPFIND (directory listing)
// is retried when it fails with a transient timeout (context deadline exceeded)
// before giving up. The outer indexing context still bounds total time, so the
// retries cannot run forever.
const maxListAttempts = 3

// listingTimeout returns the per-request timeout for a single WebDAV/Nextcloud
// PROPFIND directory listing. Configurable via WEBDAV_LISTING_TIMEOUT_SECONDS
// (default 120s) so genuinely large folders are not killed prematurely.
func listingTimeout() time.Duration {
	if v := os.Getenv("WEBDAV_LISTING_TIMEOUT_SECONDS"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 2 * time.Minute
}

// cancelOnClose wraps a response body so that closing it also cancels the
// per-attempt context. This transfers ownership of the cancel func to the caller
// (which defers resp.Body.Close()) so the context is released exactly when the
// body is consumed, avoiding both a context leak and cancelling mid-read.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// doPropfind executes a PROPFIND request with a fresh per-attempt context timeout
// and retries transient deadline (timeout) errors with a short exponential backoff.
// Non-timeout errors (auth, bad status, network) are returned immediately. The
// provided req's context is ignored; each attempt uses its own derived context so
// a previous timeout does not poison the next attempt.
func doPropfind(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	backoff := 5 * time.Second
	for attempt := 0; attempt < maxListAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, listingTimeout())
		attemptReq := req.Clone(attemptCtx)

		resp, err := client.Do(attemptReq)
		if err != nil {
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				// Transient timeout: retry unless we've exhausted attempts or the
				// overall indexing context has been cancelled/shutdown.
				if attempt < maxListAttempts-1 && ctx.Err() == nil {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(backoff):
					}
					backoff *= 2
					continue
				}
				return nil, err
			}
			return nil, err
		}
		// Success: hand cancel ownership to the body so it fires on Body.Close().
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
	return nil, context.DeadlineExceeded
}
