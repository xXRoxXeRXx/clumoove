package restore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
)

// warnRecorder captures the warning codes the coordinator persists for an item.
type warnRecorder struct {
	db.RestoreStore
	codes []string
}

func (w *warnRecorder) WarnRestoreItemContext(_ context.Context, _ db.Lease, _, code string) error {
	w.codes = append(w.codes, code)
	return nil
}

type failingMetadataTarget struct{ err error }

func (f failingMetadataTarget) ApplyMetadata(context.Context, string, string, storage.FileMetadata) error {
	return f.err
}

func TestApplyItemMetadataWarnsByCapabilityAndOutcome(t *testing.T) {
	item := &db.RestoreItem{
		ID:           "item-1",
		RestoreRunID: "run-1",
		ClaimEpoch:   1,
		TargetPath:   "/restore/a.txt",
		SourceMTime:  sql.NullTime{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Valid: true},
	}
	cases := []struct {
		name      string
		target    any
		wantCodes []string
	}{
		{"target without metadata support is reported unsupported", struct{}{}, []string{"RESTORE_METADATA_UNSUPPORTED"}},
		{"failing metadata application is reported failed", failingMetadataTarget{err: errors.New("denied")}, []string{"RESTORE_METADATA_FAILED"}},
		{"successful metadata application raises no warning", &metadataOnlyTarget{}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &warnRecorder{}
			if err := (&Coordinator{store: store}).applyItemMetadata(context.Background(), c.target, item); err != nil {
				t.Fatalf("applyItemMetadata() error = %v", err)
			}
			if len(store.codes) != len(c.wantCodes) || (len(c.wantCodes) == 1 && store.codes[0] != c.wantCodes[0]) {
				t.Fatalf("warning codes = %v, want %v", store.codes, c.wantCodes)
			}
		})
	}
}

// metadataPreviewTarget is a preview target that can apply metadata.
type metadataPreviewTarget struct{ minimalWorkflowPreviewTarget }

func (*metadataPreviewTarget) ApplyMetadata(context.Context, string, string, storage.FileMetadata) error {
	return nil
}

func TestPreviewCountsMetadataWarningsOnlyForTargetsWithoutMetadataSupport(t *testing.T) {
	const encKey = "test-restore-encryption-key"
	secret, err := crypto.EncryptWithDomain("preview-plaintext", encKey, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatal(err)
	}
	newReader := func() minimalPreviewReader {
		return minimalPreviewReader{
			files: map[string]bool{"/restore": true},
			dirs:  map[string][]storage.CloudResource{"/restore": nil},
		}
	}
	cases := []struct {
		name         string
		target       PreviewTargetClient
		wantWarnings int
	}{
		{"target without metadata support warns for the item", &minimalWorkflowPreviewTarget{minimalPreviewReader: newReader()}, 1},
		{"target with metadata support does not warn", &metadataPreviewTarget{minimalWorkflowPreviewTarget{minimalPreviewReader: newReader()}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coord := &Coordinator{encryptionKey: encKey}
			coord.SetPreviewTargetFactory(func(context.Context, string, string, string, string, ...storage.ProviderOption) (PreviewTargetClient, error) {
				return c.target, nil
			})
			stats, err := coord.previewTargetWithItems(context.Background(), &db.RestorePreview{
				TargetProvider:          "webdav",
				TargetRoot:              "/restore",
				ConflictStrategy:        "OVERWRITE",
				TargetPasswordEncrypted: sql.NullString{String: secret, Valid: true},
			}, []db.RestorePreviewItem{{
				RelativePath: "a.txt",
				SizeBytes:    10,
				State:        "AVAILABLE",
				SourceMTime:  sql.NullTime{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Valid: true},
			}})
			if err != nil {
				t.Fatalf("previewTargetWithItems() error = %v", err)
			}
			if stats.MetadataWarnings != c.wantWarnings {
				t.Fatalf("MetadataWarnings = %d, want %d", stats.MetadataWarnings, c.wantWarnings)
			}
		})
	}
}

// plainUploadTarget exposes only the RestoreTarget interface, hiding the
// wrapped provider's typed upload and metadata capabilities, and reports a
// target without atomic rename so restore uploads straight to the final path.
type plainUploadTarget struct {
	RestoreTarget
	plainUploads int
}

func (p *plainUploadTarget) SupportsAtomicRename() bool { return false }

func (p *plainUploadTarget) StreamUpload(ctx context.Context, resourceType, path string, reader io.Reader, size int64) error {
	p.plainUploads++
	return p.RestoreTarget.StreamUpload(ctx, resourceType, path, reader, size)
}

func TestRestoreItemFallsBackToPlainUploadWithoutTypedUploader(t *testing.T) {
	repoSrv := newBoxRestoreFakeServer("box-repo-token")
	defer repoSrv.close()
	targetSrv := newBoxRestoreFakeServer("box-target-token")
	defer targetSrv.close()

	payload := []byte("restored-through-the-plain-upload-fallback")
	coord, store := newBoxRestoreTestSetup(t, repoSrv, targetSrv, payload, "/restored/fallback.txt", "RENAME")
	target := &plainUploadTarget{RestoreTarget: targetSrv.newProvider(t)}
	coord.SetRestoreTargetFactory(func(context.Context, string, string, string, string, ...storage.ProviderOption) (RestoreTarget, error) {
		return target, nil
	})
	if _, ok := any(target).(storage.TransferUploader); ok {
		t.Fatal("test target must not be a TransferUploader")
	}

	if err := coord.restoreItem(context.Background(), store.item); err != nil {
		t.Fatalf("restoreItem() error = %v", err)
	}
	if target.plainUploads != 1 {
		t.Fatalf("plain uploads = %d, want 1", target.plainUploads)
	}

	verify := targetSrv.newProvider(t)
	defer verify.Close()
	body, err := verify.StreamDownload(context.Background(), "files", "/restored/fallback.txt")
	if err != nil {
		t.Fatalf("StreamDownload(restored target): %v", err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if !bytes.Equal(got, payload) {
		t.Fatalf("restored content = %q, want %q", got, payload)
	}
}
