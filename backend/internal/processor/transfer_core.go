package processor

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"

	"backend/internal/storage"
	"backend/internal/throttle"
)

// transferRequest contains the provider-facing part of a file transfer. It is
// intentionally independent of task persistence and queue lifecycle so it can
// be exercised with in-memory StorageProvider implementations.
type transferRequest struct {
	Context          context.Context
	UploadContext    context.Context
	Source           storage.StorageProvider
	Target           storage.StorageProvider
	SourceProvider   string
	TargetProvider   string
	ResourceType     string
	SourcePath       string
	TargetPath       string
	VerificationPath string
	// VerificationContext is evaluated after upload/finalization, allowing a
	// provider to expose an upload-created target identity for size checks.
	VerificationContext func() context.Context
	FileSize            int64
	SourceHash          string
	Throttler           *throttle.MigrationThrottler
	Progress            chan<- int64
	// Finalize runs after the complete source stream has been uploaded and
	// before the destination is size-verified (for example, to promote an
	// overwrite temporary object).
	Finalize func(context.Context) error
}

// transferResult retains hashers that are complete when runTransferCore
// returns. Callers must read Sum before writing to either hasher again.
type transferResult struct {
	SourceAlgorithm string
	SourceHasher    hash.Hash
	TargetAlgorithm string
	TargetHasher    hash.Hash
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
	case "google":
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
	case "google":
		return "MD5", md5.New()
	case "hidrive":
		return "HIDRIVE", storage.NewHiDriveHasher()
	case "onedrive":
		return "QUICKXOR", storage.NewQuickXorHasher()
	default:
		return "SHA1", sha1.New()
	}
}

func runTransferCore(req transferRequest) (transferResult, error) {
	if req.Context == nil || req.UploadContext == nil || req.Source == nil || req.Target == nil {
		return transferResult{}, fmt.Errorf("transfer core requires contexts and providers")
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
		downloadReader = throttle.NewThrottledReader(downloadReader, req.Throttler, downloadCtx)
	}
	reader := newExpectedSizeReader(downloadReader, req.FileSize)
	hashingReader := io.TeeReader(reader, hashWriter)
	uploadCtx, cancelUpload := context.WithTimeout(req.UploadContext, deadline)
	defer cancelUpload()
	if expectedHash != "" {
		uploadCtx = storage.WithUploadChecksum(uploadCtx, fmt.Sprintf("%s:%s", sourceAlgo, expectedHash))
	}
	if req.FileSize > chunkedUploadThreshold {
		uploadReader := io.Reader(hashingReader)
		if req.Throttler != nil {
			uploadReader = throttle.NewUploadThrottledReader(uploadReader, req.Throttler, uploadCtx)
		}
		err = req.Target.StreamUploadChunked(uploadCtx, req.ResourceType, req.TargetPath, uploadReader, req.FileSize, req.Progress)
	} else {
		progress := &ProgressReader{Reader: hashingReader, ProgressChan: req.Progress}
		uploadReader := io.Reader(progress)
		if req.Throttler != nil {
			uploadReader = throttle.NewUploadThrottledReader(uploadReader, req.Throttler, uploadCtx)
		}
		err = req.Target.StreamUpload(uploadCtx, req.ResourceType, req.TargetPath, uploadReader, req.FileSize)
	}
	if err != nil {
		return transferResult{}, fmt.Errorf("upload to target failed: %w", err)
	}
	if err := reader.VerifyComplete(); err != nil {
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
		verificationCtx := req.Context
		if req.VerificationContext != nil {
			verificationCtx = req.VerificationContext()
		}
		exists, size, err := verifyTargetSize(verificationCtx, req.Target, req.ResourceType, verificationPath)
		if err != nil {
			return transferResult{}, fmt.Errorf("failed to verify target size: %w", err)
		}
		if !exists || size != req.FileSize {
			return transferResult{}, fmt.Errorf("target size mismatch: got %d bytes, expected %d", size, req.FileSize)
		}
	}
	return transferResult{
		SourceAlgorithm: sourceAlgo,
		SourceHasher:    sourceHasher,
		TargetAlgorithm: targetAlgo,
		TargetHasher:    targetHasher,
	}, nil
}
