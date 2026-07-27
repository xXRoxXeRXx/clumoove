package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONBodyRejectsOversizedValidPrefix(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"value":"ok"}`+strings.Repeat(" ", normalJSONBodyLimit)))
	rec := httptest.NewRecorder()
	var body struct {
		Value string `json:"value"`
	}

	if decodeJSONBody(rec, req, &body, normalJSONBodyLimit) {
		t.Fatal("expected oversized request body to be rejected")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var response struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ErrorCode != string(ErrInvalidBody) {
		t.Fatalf("error_code = %q, want %q", response.ErrorCode, ErrInvalidBody)
	}
}

func TestDecodeJSONBodyAcceptsValidBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"value":"ok"}`))
	rec := httptest.NewRecorder()
	var body struct {
		Value string `json:"value"`
	}

	if !decodeJSONBody(rec, req, &body, normalJSONBodyLimit) {
		t.Fatal("expected valid request body to be accepted")
	}
	if body.Value != "ok" {
		t.Fatalf("value = %q, want %q", body.Value, "ok")
	}
}

func TestDecodeJSONBodyRejectsEmptyAndTrailingBodies(t *testing.T) {
	for name, input := range map[string]string{
		"empty":      "",
		"two values": `{"value":"ok"}{"value":"second"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(input))
			rec := httptest.NewRecorder()
			var body struct {
				Value string `json:"value"`
			}

			if decodeJSONBody(rec, req, &body, normalJSONBodyLimit) {
				t.Fatal("expected invalid request body to be rejected")
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestDecodeJSONBodyUsesDedicatedAvatarLimit(t *testing.T) {
	input := `{"avatar":"` + strings.Repeat("a", normalJSONBodyLimit) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(input))
	rec := httptest.NewRecorder()
	var body struct {
		Avatar string `json:"avatar"`
	}

	if !decodeJSONBody(rec, req, &body, avatarJSONBodyLimit) {
		t.Fatal("expected avatar-sized body to be accepted by its dedicated limit")
	}
}
