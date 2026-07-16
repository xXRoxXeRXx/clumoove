package auth

import (
	"context"
	"testing"
)

func TestGetUserIDFromContextEmpty(t *testing.T) {
	if uid := GetUserIDFromContext(context.Background()); uid != "" {
		t.Errorf("expected empty user id from empty context, got %q", uid)
	}
	type otherKey string
	c := context.WithValue(context.Background(), otherKey("x"), "y")
	if uid := GetUserIDFromContext(c); uid != "" {
		t.Errorf("expected empty user id for unrelated context value, got %q", uid)
	}
}
