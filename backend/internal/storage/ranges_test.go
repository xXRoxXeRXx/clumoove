package storage

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestValidateByteRange(t *testing.T) {
	tests := []struct {
		offset, length int64
		wantEnd        int64
		wantErr        bool
	}{
		{0, 10, 9, false},
		{100, 50, 149, false},
		{-1, 10, 0, true},
		{0, 0, 0, true},
		{0, -5, 0, true},
		{1 << 62, 1 << 62, 0, true}, // overflow
	}

	for _, tt := range tests {
		end, err := ValidateByteRange(tt.offset, tt.length)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateByteRange(%d, %d) err = %v, wantErr %v", tt.offset, tt.length, err, tt.wantErr)
		}
		if !tt.wantErr && end != tt.wantEnd {
			t.Errorf("ValidateByteRange(%d, %d) = %d, wantEnd %d", tt.offset, tt.length, end, tt.wantEnd)
		}
	}
}
func TestValidateHTTPRangeResponse(t *testing.T) {
	payload := []byte("hello world")

	// Valid 206
	resp206 := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: int64(len(payload)),
		Header: http.Header{
			"Content-Range": []string{"bytes 0-10/100"},
		},
		Body: io.NopCloser(bytes.NewReader(payload)),
	}
	rc, err := ValidateHTTPRangeResponse(resp206, 0, 11)
	if err != nil {
		t.Fatalf("unexpected error for valid 206: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "hello world" {
		t.Fatalf("got %q, want %q", string(data), "hello world")
	}

	// Reject 200 OK
	resp200 := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: int64(len(payload)),
		Body:          io.NopCloser(bytes.NewReader(payload)),
	}
	if _, err := ValidateHTTPRangeResponse(resp200, 5, 5); err == nil {
		t.Fatal("expected error for 200 OK, got nil")
	}

	// Reject mismatched Content-Range
	respBadRange := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 5,
		Header: http.Header{
			"Content-Range": []string{"bytes 0-4/100"},
		},
		Body: io.NopCloser(bytes.NewReader(payload[:5])),
	}
	if _, err := ValidateHTTPRangeResponse(respBadRange, 5, 5); err == nil {
		t.Fatal("expected error for mismatched Content-Range, got nil")
	}

	// Reject mismatched Content-Length
	respBadLength := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 4,
		Header: http.Header{
			"Content-Range": []string{"bytes 5-9/100"},
		},
		Body: io.NopCloser(bytes.NewReader(payload[:4])),
	}
	if _, err := ValidateHTTPRangeResponse(respBadLength, 5, 5); err == nil {
		t.Fatal("expected error for mismatched Content-Length, got nil")
	}

	// Reject missing Content-Range
	respMissingCR := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 5,
		Body:          io.NopCloser(bytes.NewReader(payload[:5])),
	}
	if _, err := ValidateHTTPRangeResponse(respMissingCR, 0, 5); err == nil {
		t.Fatal("expected error for missing Content-Range, got nil")
	}

	// Valid 206 without total length in Content-Range
	resp206NoTotal := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: int64(len(payload)),
		Header: http.Header{
			"Content-Range": []string{"bytes 0-10"},
		},
		Body: io.NopCloser(bytes.NewReader(payload)),
	}
	rcNoTotal, err := ValidateHTTPRangeResponse(resp206NoTotal, 0, 11)
	if err != nil {
		t.Fatalf("unexpected error for valid 206 without total: %v", err)
	}
	rcNoTotal.Close()

	// Truncated body produces io.ErrUnexpectedEOF
	respTruncated := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 11,
		Header: http.Header{
			"Content-Range": []string{"bytes 0-10/100"},
		},
		Body: io.NopCloser(bytes.NewReader([]byte("short"))), // 5 bytes instead of 11
	}
	rcTruncated, err := ValidateHTTPRangeResponse(respTruncated, 0, 11)
	if err != nil {
		t.Fatalf("unexpected setup error: %v", err)
	}
	_, readErr := io.ReadAll(rcTruncated)
	rcTruncated.Close()
	if !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("expected ErrUnexpectedEOF on truncated read, got %v", readErr)
	}

	// Malformed Content-Range
	respMalformedCR := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 5,
		Header: http.Header{
			"Content-Range": []string{"invalid-range"},
		},
		Body: io.NopCloser(bytes.NewReader(payload[:5])),
	}
	if _, err := ValidateHTTPRangeResponse(respMalformedCR, 0, 5); err == nil {
		t.Fatal("expected error for malformed Content-Range, got nil")
	}
}
