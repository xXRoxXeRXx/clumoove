package main

import (
	"os"
	"testing"
	"time"

	configpkg "backend/internal/config"
)

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
	if config.databaseURL != configpkg.DefaultDatabaseURL {
		t.Fatalf("database URL = %q, want %q", config.databaseURL, configpkg.DefaultDatabaseURL)
	}
	if !config.databaseURLDefaulted {
		t.Fatal("database URL default was not recorded")
	}
	if config.redisURL != configpkg.DefaultRedisURL {
		t.Fatalf("Redis URL = %q, want localhost default", config.redisURL)
	}
}

func TestLoadWorkerConfigRejectsMissingEncryptionKey(t *testing.T) {
	if _, err := loadWorkerConfig(func(string) string { return "" }); err == nil {
		t.Fatal("loadWorkerConfig() succeeded without an encryption key")
	}
}

func TestLoadWorkerConfigValidatesRestorePackReaderLimit(t *testing.T) {
	for _, value := range []string{"0", "5", "invalid"} {
		_, err := loadWorkerConfig(func(key string) string {
			if key == "ENCRYPTION_SECRET_KEY" {
				return "encryption-secret"
			}
			if key == "MAX_RESTORE_PACK_READERS" {
				return value
			}
			return ""
		})
		if err == nil {
			t.Errorf("MAX_RESTORE_PACK_READERS=%q succeeded", value)
		}
	}
	config, err := loadWorkerConfig(func(key string) string {
		if key == "ENCRYPTION_SECRET_KEY" {
			return "encryption-secret"
		}
		if key == "MAX_RESTORE_PACK_READERS" {
			return "3"
		}
		return ""
	})
	if err != nil || config.maxRestorePackReaders != 3 {
		t.Fatalf("restore reader limit = %d, error = %v", config.maxRestorePackReaders, err)
	}
}

func TestWatchShutdownSignalsCancelsThenForcesExit(t *testing.T) {
	signals := make(chan os.Signal, 2)
	cancelled := make(chan struct{})
	exited := make(chan int, 1)

	go watchShutdownSignals(signals, func() { close(cancelled) }, func(code int) { exited <- code })
	signals <- os.Interrupt

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("first signal did not cancel")
	}

	signals <- os.Interrupt
	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("forced exit code = %d, want 1", code)
		}
	case <-time.After(time.Second):
		t.Fatal("second signal did not force exit")
	}
}
