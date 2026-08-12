package main

import (
	"errors"
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
