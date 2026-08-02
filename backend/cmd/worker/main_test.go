package main

import "testing"

func TestLoadWorkerConfigUsesConfiguredValues(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":          "postgres://db.example/clumoove?sslmode=require",
		"REDIS_URL":             "redis://:secret@redis.example:6379",
		"ENCRYPTION_SECRET_KEY": "encryption-secret",
	}
	config, err := loadWorkerConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("loadWorkerConfig() error = %v", err)
	}
	if config.databaseURL != values["DATABASE_URL"] || config.redisURL != values["REDIS_URL"] || config.encryptionKey != values["ENCRYPTION_SECRET_KEY"] {
		t.Fatalf("loadWorkerConfig() = %#v, want configured values", config)
	}
}

func TestLoadWorkerConfigAppliesSafeDefaults(t *testing.T) {
	config, err := loadWorkerConfig(func(key string) string {
		if key == "ENCRYPTION_SECRET_KEY" {
			return "encryption-secret"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("loadWorkerConfig() error = %v", err)
	}
	if config.databaseURL != defaultDatabaseURL {
		t.Fatalf("database URL = %q, want %q", config.databaseURL, defaultDatabaseURL)
	}
	if config.redisURL != "localhost:6379" {
		t.Fatalf("Redis URL = %q, want localhost default", config.redisURL)
	}
}

func TestLoadWorkerConfigRejectsMissingEncryptionKey(t *testing.T) {
	if _, err := loadWorkerConfig(func(string) string { return "" }); err == nil {
		t.Fatal("loadWorkerConfig() succeeded without an encryption key")
	}
}
