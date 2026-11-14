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

func workerEnv(overrides map[string]string) func(string) string {
	values := map[string]string{"ENCRYPTION_SECRET_KEY": "encryption-secret"}
	for key, value := range overrides {
		values[key] = value
	}
	return func(key string) string { return values[key] }
}

func TestLoadWorkerConfigDefaultsMaxThreadsToSixteen(t *testing.T) {
	config, err := loadWorkerConfig(workerEnv(nil))
	if err != nil {
		t.Fatalf("loadWorkerConfig() error = %v", err)
	}
	if config.maxThreads != 16 {
		t.Fatalf("maxThreads = %d, want 16", config.maxThreads)
	}
}

func TestLoadWorkerConfigUsesConfiguredMaxThreads(t *testing.T) {
	config, err := loadWorkerConfig(workerEnv(map[string]string{"MAX_THREADS": "50"}))
	if err != nil {
		t.Fatalf("loadWorkerConfig() error = %v", err)
	}
	if config.maxThreads != 50 {
		t.Fatalf("maxThreads = %d, want 50", config.maxThreads)
	}
}

func TestLoadWorkerConfigRejectsInvalidMaxThreads(t *testing.T) {
	for _, value := range []string{"0", "-4", "many", "1.5"} {
		if _, err := loadWorkerConfig(workerEnv(map[string]string{"MAX_THREADS": value})); err == nil {
			t.Errorf("MAX_THREADS=%q succeeded", value)
		}
	}
}

func TestLoadWorkerConfigRejectsMaxThreadsAboveCeiling(t *testing.T) {
	if _, err := loadWorkerConfig(workerEnv(map[string]string{"MAX_THREADS": "256"})); err != nil {
		t.Fatalf("MAX_THREADS=256 rejected: %v", err)
	}
	if _, err := loadWorkerConfig(workerEnv(map[string]string{"MAX_THREADS": "257"})); err == nil {
		t.Fatal("MAX_THREADS=257 succeeded")
	}
}

func TestLoadWorkerConfigDerivesDatabasePoolSizeFromMaxThreads(t *testing.T) {
	for threads, want := range map[string]int{"16": 50, "25": 50, "50": 100} {
		config, err := loadWorkerConfig(workerEnv(map[string]string{"MAX_THREADS": threads}))
		if err != nil {
			t.Fatalf("MAX_THREADS=%s: %v", threads, err)
		}
		if config.dbMaxConns != want {
			t.Errorf("MAX_THREADS=%s: dbMaxConns = %d, want %d", threads, config.dbMaxConns, want)
		}
	}
}

func TestWorkerConfigProcessorConfigCarriesExplicitSettings(t *testing.T) {
	config, err := loadWorkerConfig(workerEnv(map[string]string{
		"DATABASE_URL": "postgres://db.example/clumoove?sslmode=require",
		"MAX_THREADS":  "7",
	}))
	if err != nil {
		t.Fatalf("loadWorkerConfig() error = %v", err)
	}
	got := config.processorConfig("worker-1")
	if got.WorkerID != "worker-1" || got.SecretKey != "encryption-secret" || got.MaxThreads != 7 || got.NotifyDSN != "postgres://db.example/clumoove?sslmode=require" {
		t.Fatalf("processorConfig() = %#v", got)
	}
}

func TestLoadWorkerConfigValidatesBackupPackWriterLimit(t *testing.T) {
	for _, value := range []string{"0", "5", "invalid"} {
		if _, err := loadWorkerConfig(workerEnv(map[string]string{"MAX_BACKUP_PACK_WRITERS": value})); err == nil {
			t.Errorf("MAX_BACKUP_PACK_WRITERS=%q succeeded", value)
		}
	}
	config, err := loadWorkerConfig(workerEnv(map[string]string{"MAX_BACKUP_PACK_WRITERS": "4"}))
	if err != nil || config.maxBackupPackWriters != 4 {
		t.Fatalf("backup writer limit = %d, error = %v", config.maxBackupPackWriters, err)
	}
	if config, err = loadWorkerConfig(workerEnv(nil)); err != nil || config.maxBackupPackWriters != 1 {
		t.Fatalf("default backup writer limit = %d, error = %v", config.maxBackupPackWriters, err)
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
