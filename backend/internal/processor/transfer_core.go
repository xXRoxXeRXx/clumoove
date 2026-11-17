package processor

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"backend/internal/storage"
	"backend/internal/throttle"
)

type transferSource interface {
	StreamDownload(ctx context.Context, resourceType, path string) (io.ReadCloser, error)
}

type uploadTarget interface {
	StreamUpload(ctx context.Context, resourceType, path string, reader io.Reader, size int64) error
	StreamUploadChunked(ctx context.Context, resourceType, path string, reader io.Reader, size int64, progress chan<- int64) error
}

type transferTarget interface {
	uploadTarget
	storage.FileExister
}

// MetadataPolicy states how applying source file metadata to the target
// affects a transfer. Metadata is always applied after the destination has been
// finalized and size-verified, to the final (not staging) path.
type MetadataPolicy struct {
	// Timeout bounds one ApplyMetadata call. Zero inherits the request context.
	Timeout time.Duration
	// FailOn reports whether an ApplyMetadata error fails the transfer. A nil
	// FailOn never fails it. Errors that do not fail the transfer are returned
	// in transferResult.MetadataErr so the caller decides whether to log them.
	FailOn func(error) bool
}

// failMetadataOnAuth fails a transfer only when the target rejected credentials.
func failMetadataOnAuth(err error) bool { return errors.Is(err, storage.ErrAuth) }

// transferRequest contains the provider-facing part of a file transfer. It is
// intentionally independent of task persistence and queue lifecycle so it can
// be exercised with focused stream/existence adapters.
type transferRequest struct {
	Context          context.Context
	Source           transferSource
	Target           transferTarget
	SourceProvider   string
	TargetProvider   string
	ResourceType     string
	SourcePath       string
	TargetPath       string
	VerificationPath string
	TargetResourceID string
	UploadOptions    storage.UploadOptions
	FileSize         int64
	SourceHash       string
	Throttler        *throttle.MigrationThrottler
	Progress         chan<- int64
	// Finalize runs after the complete source stream has been uploaded and
	// before the destination is size-verified (for example, to promote an
	// overwrite temporary object).
	Finalize func(context.Context) error
	// AbortUpload runs when the upload or the source completeness check fails,
	// before Finalize, so a caller can remove its staging object. It is not
	// called for failures after Finalize has started.
	AbortUpload func(context.Context)
	// Metadata, when set, applies UploadOptions.Metadata to the final path
	// through the target's optional MetadataApplier under the given policy. It
	// is skipped when there is no modification time or description to apply.
	Metadata *MetadataPolicy
}

// transferResult retains hashers that are complete when runTransferCore
// returns. Callers must read Sum before writing to either hasher again.
type transferResult struct {
	SourceAlgorithm string
	SourceHasher    hash.Hash
	TargetAlgorithm string
	TargetHasher    hash.Hash
	UploadReceipt   storage.UploadReceipt
	// MetadataErr is a metadata application failure the request's policy did
	// not treat as fatal.
	MetadataErr error
}

func transferSourceHasher(provider, sourceHash string) (string, string, hash.Hash) {
	algo, expected := "SHA1", ""
	if sourceHash != "" && provider != "webdav" {
		parsed, value := storage.ParseHashString(sourceHash)
		if parsed == "SHA1" || parsed == "SHA256" || parsed == "MD5" || parsed == "DROPBOX" {
			algo, expected = parsed, value
		}
	}
	switch provider {
	case "dropbox":
		algo = "DROPBOX"
	case "google", "koofr":
		algo = "MD5"
	case "onedrive":
		algo = "QUICKXOR"
	}
	switch algo {
	case "MD5":
		return algo, expected, md5.New()
	case "DROPBOX":
		return algo, expected, storage.NewDropboxHasher()
	case "SHA256":
		return algo, expected, sha256.New()
	case "QUICKXOR":
		return algo, expected, storage.NewQuickXorHasher()
	default:
		return "SHA1", expected, sha1.New()
	}
}

func transferTargetHasher(provider string) (string, hash.Hash) {
	switch provider {
	case "dropbox":
		return "DROPBOX", storage.NewDropboxHasher()
	case "s3":
		return "SHA256", sha256.New()
	case "google", "koofr":
		return "MD5", md5.New()
	case "hidrive":
		return "HIDRIVE", storage.NewHiDriveHasher()
	case "onedrive":
		return "QUICKXOR", storage.NewQuickXorHasher()
	default:
		return "SHA1", sha1.New()
	}
}

func streamUploadWithOptions(ctx context.Context, target uploadTarget, resourceType, targetPath string, reader io.Reader, fileSize int64, progress chan<- int64, opts storage.UploadOptions) (storage.UploadResult, error) {
	if uploader, ok := target.(storage.TransferUploader); ok {
		if fileSize > chunkedUploadThreshold {
			return uploader.StreamUploadChunkedWithOptions(ctx, resourceType, targetPath, reader, fileSize, progress, opts)
		}
		return uploader.StreamUploadWithOptions(ctx, resourceType, targetPath, reader, fileSize, opts)
	}
	if fileSize > chunkedUploadThreshold {
		return storage.UploadResult{}, target.StreamUploadChunked(ctx, resourceType, targetPath, reader, fileSize, progress)
	}
	return storage.UploadResult{}, target.StreamUpload(ctx, resourceType, targetPath, reader, fileSize)
}

func runTransferCore(req transferRequest) (transferResult, error) {
	if req.Context == nil || req.Source == nil || req.Target == nil {
		return transferResult{}, fmt.Errorf("transfer core requires context and providers")
	}
	deadline := transferTimeout(req.FileSize)
	downloadCtx, cancelDownload := context.WithTimeout(req.Context, deadline)
	defer cancelDownload()
	stream, err := req.Source.StreamDownload(downloadCtx, req.ResourceType, req.SourcePath)
	if err != nil {
		return transferResult{}, fmt.Errorf("failed to download from source: %w", err)
	}
	defer stream.Close()

	sourceAlgo, expectedHash, sourceHasher := transferSourceHasher(req.SourceProvider, req.SourceHash)
	targetAlgo, targetHasher := transferTargetHasher(req.TargetProvider)
	var hashWriter io.Writer = sourceHasher
	if sourceAlgo == targetAlgo {
		targetHasher = nil
	} else {
		hashWriter = io.MultiWriter(sourceHasher, targetHasher)
	}

	var downloadReader io.Reader = stream
	if req.Throttler != nil {
		downloadReader = throttle.NewThrottledReader(downloadCtx, downloadReader, req.Throttler)
	}
	reader := newExpectedSizeReader(downloadReader, req.FileSize)
	hashingReader := io.TeeReader(reader, hashWriter)
	uploadCtx, cancelUpload := context.WithTimeout(req.Context, deadline)
	defer cancelUpload()
	uploadOpts := req.UploadOptions
	if expectedHash != "" && uploadOpts.Checksum == "" {
		uploadOpts.Checksum = fmt.Sprintf("%s:%s", sourceAlgo, expectedHash)
	}
	uploadReader := io.Reader(hashingReader)
	if req.FileSize <= chunkedUploadThreshold {
		uploadReader = &ProgressReader{Reader: hashingReader, ProgressChan: req.Progress}
	}
	if req.Throttler != nil {
		uploadReader = throttle.NewUploadThrottledReader(uploadCtx, uploadReader, req.Throttler)
	}
	uploadResult, err := streamUploadWithOptions(uploadCtx, req.Target, req.ResourceType, req.TargetPath, uploadReader, req.FileSize, req.Progress, uploadOpts)
	if err != nil {
		if req.AbortUpload != nil {
			req.AbortUpload(req.Context)
		}
		return transferResult{}, fmt.Errorf("upload to target failed: %w", err)
	}
	if err := reader.VerifyComplete(); err != nil {
		if req.AbortUpload != nil {
			req.AbortUpload(req.Context)
		}
		return transferResult{}, err
	}
	if req.Finalize != nil {
		if err := req.Finalize(req.Context); err != nil {
			return transferResult{}, err
		}
	}
	if req.ResourceType == "files" {
		verificationPath := req.VerificationPath
		if verificationPath == "" {
			verificationPath = req.TargetPath
		}
		targetResourceID := uploadResult.Receipt.TargetResourceID
		if targetResourceID == "" {
			targetResourceID = req.TargetResourceID
		}
		exists, size, err := storage.VerifyTargetSizeByResourceID(req.Context, req.Target, req.ResourceType, verificationPath, targetResourceID)
		if err != nil {
			return transferResult{}, fmt.Errorf("failed to verify target size: %w", err)
		}
		if !exists || size != req.FileSize {
			return transferResult{}, fmt.Errorf("target size mismatch: got %d bytes, expected %d", size, req.FileSize)
		}
	}
	metadataErr, err := applyTransferMetadata(req)
	if err != nil {
		return transferResult{}, err
	}
	return transferResult{
		SourceAlgorithm: sourceAlgo,
		SourceHasher:    sourceHasher,
		TargetAlgorithm: targetAlgo,
		TargetHasher:    targetHasher,
		UploadReceipt:   uploadResult.Receipt,
		MetadataErr:     metadataErr,
	}, nil
}

// applyTransferMetadata applies the request's metadata under its policy. It
// returns a non-fatal failure separately from the error that fails the transfer.
func applyTransferMetadata(req transferRequest) (nonFatal, fatal error) {
	if req.Metadata == nil {
		return nil, nil
	}
	applier, ok := req.Target.(storage.MetadataApplier)
	meta := req.UploadOptions.Metadata
	if !ok || (meta.ModifiedTime.IsZero() && meta.Description == "") {
		return nil, nil
	}
	finalPath := req.VerificationPath
	if finalPath == "" {
		finalPath = req.TargetPath
	}
	ctx := req.Context
	if req.Metadata.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Metadata.Timeout)
		defer cancel()
	}
	err := applier.ApplyMetadata(ctx, req.ResourceType, finalPath, meta)
	if err == nil {
		return nil, nil
	}
	if req.Metadata.FailOn != nil && req.Metadata.FailOn(err) {
		return nil, err
	}
	return err, nil
}
