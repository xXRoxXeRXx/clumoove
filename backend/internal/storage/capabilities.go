package storage

import (
	"context"
	"errors"
	"io"
)

// ErrCapabilityUnsupported is returned by the capability helpers when the
// provider does not implement the optional capability. Callers choose their own
// fallback; a provider's own failure never matches this error.
var ErrCapabilityUnsupported = errors.New("storage provider does not support this capability")

// SupportsMetadata reports whether the provider can apply file metadata.
func SupportsMetadata(client any) bool {
	_, ok := client.(MetadataApplier)
	return ok
}

// ApplyMetadata applies file metadata through the provider's MetadataApplier,
// returning ErrCapabilityUnsupported when it does not implement one.
func ApplyMetadata(ctx context.Context, client any, resourceType, filePath string, meta FileMetadata) error {
	applier, ok := client.(MetadataApplier)
	if !ok {
		return ErrCapabilityUnsupported
	}
	return applier.ApplyMetadata(ctx, resourceType, filePath, meta)
}

// StreamUploadWithOptions uploads through the provider's TransferUploader,
// choosing the chunked variant above DefaultChunkedUploadThreshold. It returns
// ErrCapabilityUnsupported, without reading stream, when the provider does not
// implement TransferUploader.
func StreamUploadWithOptions(ctx context.Context, client any, resourceType, filePath string, stream io.Reader, size int64, progress chan<- int64, opts UploadOptions) (UploadResult, error) {
	uploader, ok := client.(TransferUploader)
	if !ok {
		return UploadResult{}, ErrCapabilityUnsupported
	}
	if size > DefaultChunkedUploadThreshold {
		return uploader.StreamUploadChunkedWithOptions(ctx, resourceType, filePath, stream, size, progress, opts)
	}
	return uploader.StreamUploadWithOptions(ctx, resourceType, filePath, stream, size, opts)
}

// UsesNativeDuplicateDetection reports whether the provider owns duplicate
// detection, so a filename-based existence preflight would be unsafe.
func UsesNativeDuplicateDetection(client any) bool {
	detector, ok := client.(NativeDuplicateDetector)
	return ok && detector.UsesNativeDuplicateDetection()
}

// SupportsRangeDownload reports whether the provider can stream byte ranges.
func SupportsRangeDownload(client any) bool {
	_, ok := client.(RangeDownloader)
	return ok
}

// StreamDownloadRange streams a byte range through the provider's
// RangeDownloader, returning ErrCapabilityUnsupported when it has none.
func StreamDownloadRange(ctx context.Context, client any, resourceType, filePath string, offset, length int64) (io.ReadCloser, error) {
	downloader, ok := client.(RangeDownloader)
	if !ok {
		return nil, ErrCapabilityUnsupported
	}
	return downloader.StreamDownloadRange(ctx, resourceType, filePath, offset, length)
}

// ResolveResourceSize resolves an exact transferable size through the
// provider's ResourceSizeResolver, returning ErrCapabilityUnsupported when it
// has none.
func ResolveResourceSize(ctx context.Context, client any, resourceType, resourcePath string) (int64, error) {
	resolver, ok := client.(ResourceSizeResolver)
	if !ok {
		return 0, ErrCapabilityUnsupported
	}
	return resolver.ResolveResourceSize(ctx, resourceType, resourcePath)
}
