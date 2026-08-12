package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/hirochachacha/go-smb2"
)

type SMBProvider struct {
	Host     string
	Port     string
	Share    string
	Domain   string
	Username string
	Password string

	mu      sync.Mutex
	conn    net.Conn
	session *smb2.Session
	fs      *smb2.Share
}

// Ensure SMBProvider implements StorageProvider
var _ StorageProvider = (*SMBProvider)(nil)

func NewSMBProvider(rawURL, username, password string) (*SMBProvider, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SMB URL")
	}

	if u.Scheme != "smb" {
		return nil, fmt.Errorf("invalid scheme %q, expected smb", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing host in SMB URL")
	}

	port := u.Port()
	if port == "" {
		port = "445"
	}

	pathParts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(pathParts) == 0 || pathParts[0] == "" {
		return nil, fmt.Errorf("missing share name in SMB URL path")
	}
	share := pathParts[0]

	domain := u.Query().Get("domain")

	return &SMBProvider{
		Host:     host,
		Port:     port,
		Share:    share,
		Domain:   domain,
		Username: username,
		Password: password,
	}, nil
}

func (p *SMBProvider) cleanPath(filePath string) string {
	filePath = strings.ReplaceAll(filePath, "\\", "/")
	filePath = path.Clean("/" + filePath)
	filePath = strings.TrimPrefix(filePath, "/")
	if filePath == "" {
		return "."
	}
	return filePath
}

func isSMBAuthError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "logon") ||
		strings.Contains(errStr, "bad username") ||
		strings.Contains(errStr, "login") ||
		strings.Contains(errStr, "authentication") ||
		strings.Contains(errStr, "unauthorized")
}

func smbHandshakeDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(15 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}
	return deadline
}

// closeSMBConnectionWhenDone interrupts go-smb2 operations, which otherwise
// may wait indefinitely for a stalled server response. The caller holds p.mu,
// so closing this captured connection cannot interrupt another provider call.
func closeSMBConnectionWhenDone(ctx context.Context, conn net.Conn) func() {
	if ctx == nil || ctx.Done() == nil || conn == nil {
		return func() {}
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopped:
		}
	}()
	return func() { close(stopped) }
}

// handleError resets the connection state only on network/socket errors.
// Must be called with p.mu lock held.
func (p *SMBProvider) handleError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return err
	}
	if isConnectionFailure(err) {
		p.cleanup()
	}
	return err
}

func (p *SMBProvider) ensureConnected(ctx context.Context) error {
	if p.fs != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	addr := net.JoinHostPort(p.Host, p.Port)
	conn, err := egressDialer(p.Host)(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to host %s: %w", addr, err)
	}
	if err := conn.SetDeadline(smbHandshakeDeadline(ctx)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to set SMB connection deadline: %w", err)
	}
	stop := closeSMBConnectionWhenDone(ctx, conn)

	dialer := p.dialer()

	s, err := dialer.DialContext(ctx, conn)
	if err != nil {
		stop()
		conn.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("failed to authenticate: %w", err)
	}

	fs, err := s.Mount(p.Share)
	if err != nil {
		stop()
		s.Logoff()
		conn.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("failed to mount share %s: %w", p.Share, err)
	}
	stop()
	if ctx.Err() != nil {
		_ = fs.Umount()
		_ = s.Logoff()
		_ = conn.Close()
		return ctx.Err()
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = fs.Umount()
		_ = s.Logoff()
		_ = conn.Close()
		return fmt.Errorf("failed to clear SMB connection deadline: %w", err)
	}

	p.conn = conn
	p.session = s
	p.fs = fs
	return nil
}

// operation executes one non-streaming SMB call while holding an exclusive
// session lock. Closing the raw connection is the only reliable cancellation
// mechanism provided by go-smb2 for a blocked receive.
func (p *SMBProvider) operation(ctx context.Context, fn func(*smb2.Share) error) error {
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}
	stop := closeSMBConnectionWhenDone(ctx, p.conn)
	err := fn(p.fs)
	stop()
	if ctx != nil && ctx.Err() != nil {
		p.cleanup()
		return ctx.Err()
	}
	return p.handleError(err)
}

func (p *SMBProvider) dialer() *smb2.Dialer {
	return &smb2.Dialer{
		// The dependency otherwise accepts unsigned responses from servers that
		// merely advertise (rather than require) SMB signing.
		Negotiator: smb2.Negotiator{
			RequireMessageSigning: true,
		},
		Initiator: &smb2.NTLMInitiator{
			User:     p.Username,
			Password: p.Password,
			Domain:   p.Domain,
		},
	}
}

func (p *SMBProvider) cleanup() {
	if p.fs != nil {
		_ = p.fs.Umount()
		p.fs = nil
	}
	if p.session != nil {
		_ = p.session.Logoff()
		p.session = nil
	}
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
}

func (p *SMBProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanup()
	return nil
}

func (p *SMBProvider) Connect(ctx context.Context) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureConnected(ctx); err != nil {
		if isSMBAuthError(err) {
			return false, fmt.Errorf("smb connect: %w", ErrAuth)
		}
		slog.ErrorContext(ctx, "storage provider connection failed", slog.String("provider", "smb"), slog.String("operation", "connect"))
		return false, fmt.Errorf("smb connect: connection failed")
	}

	// Verify by listing the share root
	stop := closeSMBConnectionWhenDone(ctx, p.conn)
	_, err := p.fs.WithContext(ctx).ReadDir(".")
	stop()
	if ctx != nil && ctx.Err() != nil {
		p.cleanup()
		return false, ctx.Err()
	}
	if err != nil {
		p.cleanup()
		if isSMBAuthError(err) {
			return false, fmt.Errorf("smb connect: %w", ErrAuth)
		}
		slog.ErrorContext(ctx, "storage provider operation failed", slog.String("provider", "smb"), slog.String("operation", "list_root"))
		return false, fmt.Errorf("smb connect: failed to list share root")
	}

	return true, nil
}

func (p *SMBProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(dirPath); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	cleanDirPath := p.cleanPath(dirPath)
	var infos []os.FileInfo
	err := p.operation(ctx, func(fs *smb2.Share) error {
		var err error
		infos, err = fs.WithContext(ctx).ReadDir(cleanDirPath)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("smb list directory failed: %w", err)
	}

	var resources []CloudResource
	for _, info := range infos {
		name := info.Name()
		var relPath string
		if cleanDirPath == "." {
			relPath = name
		} else {
			relPath = path.Join(cleanDirPath, name)
		}

		resources = append(resources, CloudResource{
			Path:         "/" + relPath,
			Name:         name,
			Size:         info.Size(),
			IsDir:        info.IsDir(),
			LastModified: info.ModTime(),
		})
	}

	return resources, nil
}

func (p *SMBProvider) InspectResource(ctx context.Context, resourceType, filePath string) (CloudResource, error) {
	if resourceType != "files" {
		return CloudResource{}, fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return CloudResource{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	cleanPath := p.cleanPath(filePath)
	var info os.FileInfo
	err := p.operation(ctx, func(fs *smb2.Share) error {
		var err error
		info, err = fs.WithContext(ctx).Stat(cleanPath)
		return err
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CloudResource{}, fmt.Errorf("smb inspect: %w", ErrNotFound)
		}
		return CloudResource{}, fmt.Errorf("smb inspect resource failed: %w", err)
	}

	return CloudResource{
		Path:         "/" + strings.TrimPrefix(cleanPath, "."),
		Name:         info.Name(),
		Size:         info.Size(),
		IsDir:        info.IsDir(),
		LastModified: info.ModTime(),
	}, nil
}

type smbDownload struct {
	file     *smb2.File
	provider *SMBProvider
	ctx      context.Context
	stop     func()
	once     sync.Once
	err      error
}

func (r *smbDownload) Read(buf []byte) (int, error) {
	if r.ctx != nil && r.ctx.Err() != nil {
		return 0, r.ctx.Err()
	}
	n, err := r.file.Read(buf)
	if r.ctx != nil && r.ctx.Err() != nil {
		return n, r.ctx.Err()
	}
	return n, err
}

func (r *smbDownload) Close() error {
	r.once.Do(func() {
		r.stop()
		r.err = r.file.Close()
		if r.err != nil || (r.ctx != nil && r.ctx.Err() != nil) {
			r.provider.cleanup()
		}
		r.provider.mu.Unlock()
	})
	return r.err
}

func (p *SMBProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return nil, err
	}

	p.mu.Lock()
	if err := p.ensureConnected(ctx); err != nil {
		err = p.handleError(err)
		p.mu.Unlock()
		return nil, err
	}

	cleanPath := p.cleanPath(filePath)
	stop := closeSMBConnectionWhenDone(ctx, p.conn)
	file, err := p.fs.WithContext(ctx).Open(cleanPath)
	if err != nil {
		stop()
		err = p.handleError(fmt.Errorf("smb open file failed: %w", err))
		p.mu.Unlock()
		return nil, err
	}
	if ctx != nil && ctx.Err() != nil {
		stop()
		_ = file.Close()
		p.cleanup()
		p.mu.Unlock()
		return nil, ctx.Err()
	}
	// Keep the session locked until the reader closes. Providers are currently
	// per task, but this also preserves stream validity if pooling is added.
	return &smbDownload{file: file, provider: p, ctx: ctx, stop: stop}, nil
}

func (p *SMBProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return err
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	cleanPath := p.cleanPath(filePath)
	return p.operation(ctx, func(fs *smb2.Share) error {
		file, err := fs.WithContext(ctx).Create(cleanPath)
		if err != nil {
			return fmt.Errorf("smb create file failed: %w", err)
		}
		defer file.Close()
		if _, err := io.Copy(file, stream); err != nil {
			return fmt.Errorf("smb write file failed: %w", err)
		}
		return nil
	})
}

func (p *SMBProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	progressReader := &ProgressReader{
		Reader:       stream,
		ProgressChan: progressChan,
	}
	return p.StreamUpload(ctx, resourceType, filePath, progressReader, size)
}

func (p *SMBProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if resourceType != "files" {
		return false, 0, fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return false, 0, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	cleanPath := p.cleanPath(filePath)
	var info os.FileInfo
	err := p.operation(ctx, func(fs *smb2.Share) error {
		var err error
		info, err = fs.WithContext(ctx).Stat(cleanPath)
		return err
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("smb stat failed: %w", err)
	}

	return true, info.Size(), nil
}

func (p *SMBProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	cleanPath := p.cleanPath(filePath)
	err := p.operation(ctx, func(fs *smb2.Share) error { return fs.WithContext(ctx).Remove(cleanPath) })
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("smb remove failed: %w", err)
	}

	return nil
}

func (p *SMBProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(oldPath); err != nil {
		return err
	}
	if err := validateStoragePath(newPath); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	cleanOld := p.cleanPath(oldPath)
	cleanNew := p.cleanPath(newPath)
	err := p.operation(ctx, func(fs *smb2.Share) error { return fs.WithContext(ctx).Rename(cleanOld, cleanNew) })
	if err != nil {
		return fmt.Errorf("smb rename failed: %w", err)
	}

	return nil
}

// SupportsAtomicRename is true: SMB rename is supported.
func (p *SMBProvider) VerificationMode() VerificationMode { return VerificationSizeOnly }
func (p *SMBProvider) SupportsAtomicRename() bool {
	return true
}

func (p *SMBProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if resourceType != "files" {
		return "", fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return "", err
	}
	return "", ErrChecksumNotAvailable
}

func (p *SMBProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return err
	}

	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}

	return p.CreateDirectory(ctx, resourceType, dir)
}

func (p *SMBProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}
	if err := validateStoragePath(dirPath); err != nil {
		return err
	}

	cleanDirPath := p.cleanPath(dirPath)
	if cleanDirPath == "." {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.operation(ctx, func(fs *smb2.Share) error { return fs.WithContext(ctx).MkdirAll(cleanDirPath, 0755) })
	if err != nil {
		return fmt.Errorf("smb mkdirall failed: %w", err)
	}
	return nil
}

func (p *SMBProvider) ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error {
	if resourceType != "files" || meta.ModifiedTime.IsZero() {
		return nil
	}
	if err := validateStoragePath(filePath); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	cleanPath := p.cleanPath(filePath)
	err := p.operation(ctx, func(fs *smb2.Share) error {
		return fs.WithContext(ctx).Chtimes(cleanPath, time.Now(), meta.ModifiedTime)
	})
	if err != nil {
		return fmt.Errorf("smb apply metadata: %w", err)
	}

	return nil
}
