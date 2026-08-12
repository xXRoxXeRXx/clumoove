package config

import "testing"

func TestDatabaseURL(t *testing.T) {
	if got, defaulted := DatabaseURL(""); got != DefaultDatabaseURL || !defaulted {
		t.Fatalf("DatabaseURL(\"\") = (%q, %t), want (%q, true)", got, defaulted, DefaultDatabaseURL)
	}
	if got, defaulted := DatabaseURL("postgres://db.example/clumoove"); got != "postgres://db.example/clumoove" || defaulted {
		t.Fatalf("DatabaseURL(configured) = (%q, %t), want configured value and false", got, defaulted)
	}
}

func TestRedisURL(t *testing.T) {
	if got, defaulted := RedisURL(""); got != DefaultRedisURL || !defaulted {
		t.Fatalf("RedisURL(\"\") = (%q, %t), want (%q, true)", got, defaulted, DefaultRedisURL)
	}
	if got, defaulted := RedisURL("redis://:secret@redis.example:6379"); got != "redis://:secret@redis.example:6379" || defaulted {
		t.Fatalf("RedisURL(configured) = (%q, %t), want configured value and false", got, defaulted)
	}
}
