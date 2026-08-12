package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/auth"
)

func TestRequireUserID(t *testing.T) {
	server := &APIServer{}
	t.Run("missing claims returns unauthorized", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if _, ok := server.requireUserID(rec, req); ok {
			t.Fatal("expected missing claims to be rejected")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("claims return user ID", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}))
		userID, ok := server.requireUserID(rec, req)
		if !ok || userID != "user-1" {
			t.Fatalf("requireUserID() = (%q, %v), want (user-1, true)", userID, ok)
		}
	})
}

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

func TestOAuthCallbackDoesNotReflectUntrustedOrigin(t *testing.T) {
	server := &APIServer{}
	maliciousOrigin := `</script><script>window.xss=true</script>`
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/callback?code=code&state=token:google:login:"+maliciousOrigin, nil)
	rec := httptest.NewRecorder()

	server.handleOAuthCallback(rec, req)

	if strings.Contains(rec.Body.String(), maliciousOrigin) {
		t.Fatal("OAuth callback reflected an untrusted origin")
	}
}

func TestSanitizeAuditToken(t *testing.T) {
	const maxTokenLen = 254
	// All C0 control bytes plus DEL; must stay in sync with sanitizeAuditToken.
	const allControls = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f" +
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f"

	cases := map[string]struct {
		in   string
		want string
	}{
		"empty":           {"", ""},
		"plain ipv4":      {"192.0.2.10", "192.0.2.10"},
		"plain ipv6":      {"2001:db8::1", "2001:db8::1"},
		"hostname":        {"api.example.com", "api.example.com"},
		"strips cr lf":    {"1.2.3.4\r\n1.2.3.5", "1.2.3.41.2.3.5"},
		"strips nul":      {"1.2.3.4\x00", "1.2.3.4"},
		"strips del":      {"1.2.3.4\x7f", "1.2.3.4"},
		"strips all c0":   {allControls, ""},
		"keeps printable": {"abc123.-:[]", "abc123.-:[]"},
		// Multi-byte UTF-8 bytes are all >= 0x80, so the ContainsAny fast path
		// must return them unchanged, matching the rune-stripping slow path.
		"keeps multibyte": {"\u00e4\u2192\u00e0", "\u00e4\u2192\u00e0"},
		"mixed ctrl utf8": {"a\rb\nc\u00e9\x00", "abc\u00e9"},
		"truncates":       {strings.Repeat("x", maxTokenLen+5), strings.Repeat("x", maxTokenLen)},
		"truncates ctrl":  {strings.Repeat("\n", maxTokenLen+5), ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := sanitizeAuditToken(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeAuditToken(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
