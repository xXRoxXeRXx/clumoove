package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/notify"
)

func TestNotificationErrorCode(t *testing.T) {
	tests := []struct {
		err  error
		want APIErrorCode
	}{
		{err: notify.ErrIncomplete, want: ErrNotificationConfigIncomplete},
		{err: notify.ErrInvalidChannel, want: ErrNotificationChannelInvalid},
		{err: notify.ErrInvalidURL, want: ErrNotificationURLInvalid},
		{err: notify.ErrInvalidPriority, want: ErrNotificationPriorityInvalid},
		{err: notify.ErrURLBlocked, want: ErrNotificationURLBlocked},
		{err: errors.New("other"), want: ErrNotificationConfigIncomplete},
	}

	for _, tt := range tests {
		if got := notificationErrorCode(tt.err); got != tt.want {
			t.Errorf("notificationErrorCode(%v) = %s, want %s", tt.err, got, tt.want)
		}
	}
}

func TestNotificationRoutesRequireAnAuthenticatedUser(t *testing.T) {
	api := newNotificationAPI(notificationDependencies{
		requireUserID: (&APIServer{}).requireUserID,
	})
	mux := http.NewServeMux()
	middlewareCalls := 0
	api.registerRoutes(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			middlewareCalls++
			next.ServeHTTP(w, r)
		})
	})

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/settings/notifications"},
		{method: http.MethodPut, path: "/api/settings/notifications"},
		{method: http.MethodPost, path: "/api/settings/notifications/test"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			middlewareCalls = 0
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
			}
			if middlewareCalls != 1 {
				t.Fatalf("middleware calls = %d, want 1", middlewareCalls)
			}
		})
	}
}

func TestNotificationModuleRegistersRoutesWithOverrideHandler(t *testing.T) {
	api := newNotificationAPI(notificationDependencies{})
	mux := http.NewServeMux()
	authCalls := 0
	api.registerRoutesWithHandler(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalls++
			next.ServeHTTP(w, r)
		})
	}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	for _, spec := range api.routes() {
		authCalls = 0
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, requestForRoutePattern(spec.pattern))
		if rec.Code != http.StatusNoContent || authCalls != 1 {
			t.Errorf("%s: status=%d authCalls=%d, want 204 and 1", spec.pattern, rec.Code, authCalls)
		}
	}
}
