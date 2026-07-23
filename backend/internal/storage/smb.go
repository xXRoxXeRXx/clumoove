package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
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
		return nil, fmt.Errorf("invalid SMB URL: %w", err)
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
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "logon") ||
		strings.Contains(errStr, "bad username") ||
		strings.Contains(errStr, "login") ||
		strings.Contains(errStr, "authentication") ||
		strings.Contains(errStr, "unauthorized")
}

// handleError resets the connection state on potential network/socket errors.
// Must be called with p.mu lock held.
func (p *SMBProvider) handleError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return err
	}
	p.cleanup()
	return err
}

func (p *SMBProvider) ensureConnected(ctx context.Context) error {
	if p.fs != nil {
		return nil
	}

	addr := net.JoinHostPort(p.Host, p.Port)
	conn, err := egressDialer(p.Host)(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to host %s: %w", addr, err)
	}

	dialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     p.Username,
			Password: p.Password,
			Domain:   p.Domain,
		},
	}

	s, err := dialer.DialContext(ctx, conn)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to authenticate: %w", err)
	}

	fs, err := s.Mount(p.Share)
	if err != nil {
		s.Logoff()
		conn.Close()
		return fmt.Errorf("failed to mount share %s: %w", p.Share, err)
	}

	p.conn = conn
	p.session = s
	p.fs = fs
	return nil
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
		log.Printf("smb connect failed: %v", err)
		return false, fmt.Errorf("smb connect: connection failed")
	}

	// Verify by listing the share root
	fsWithCtx := p.fs.WithContext(ctx)
	_, err := fsWithCtx.ReadDir(".")
	if err != nil {
		p.cleanup()
		if isSMBAuthError(err) {
			return false, fmt.Errorf("smb connect: %w", ErrAuth)
		}
		log.Printf("smb read root failed: %v", err)
		return false, fmt.Errorf("smb connect: failed to list share root")
	}

	return true, nil
}

func (p *SMBProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return nil, p.handleError(err)
	}

	cleanDirPath := p.cleanPath(dirPath)
	fsWithCtx := p.fs.WithContext(ctx)

	infos, err := fsWithCtx.ReadDir(cleanDirPath)
	if err != nil {
		return nil, p.handleError(fmt.Errorf("smb list directory failed: %w", err))
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

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return CloudResource{}, p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)
	fsWithCtx := p.fs.WithContext(ctx)

	info, err := fsWithCtx.Stat(cleanPath)
	if err != nil {
		return CloudResource{}, p.handleError(fmt.Errorf("smb inspect resource failed: %w", err))
	}

	return CloudResource{
		Path:         "/" + strings.TrimPrefix(cleanPath, "."),
		Name:         info.Name(),
		Size:         info.Size(),
		IsDir:        info.IsDir(),
		LastModified: info.ModTime(),
	}, nil
}

func (p *SMBProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return nil, p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)
	fsWithCtx := p.fs.WithContext(ctx)

	file, err := fsWithCtx.Open(cleanPath)
	if err != nil {
		return nil, p.handleError(fmt.Errorf("smb open file failed: %w", err))
	}

	return file, nil
}

func (p *SMBProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)
	fsWithCtx := p.fs.WithContext(ctx)

	file, err := fsWithCtx.Create(cleanPath)
	if err != nil {
		return p.handleError(fmt.Errorf("smb create file failed: %w", err))
	}
	defer file.Close()

	if _, err := io.Copy(file, stream); err != nil {
		return p.handleError(fmt.Errorf("smb write file failed: %w", err))
	}

	return nil
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

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return false, 0, p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)
	fsWithCtx := p.fs.WithContext(ctx)

	info, err := fsWithCtx.Stat(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, 0, nil
		}
		return false, 0, p.handleError(fmt.Errorf("smb stat failed: %w", err))
	}

	return true, info.Size(), nil
}

func (p *SMBProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)
	fsWithCtx := p.fs.WithContext(ctx)

	err := fsWithCtx.Remove(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return p.handleError(fmt.Errorf("smb remove failed: %w", err))
	}

	return nil
}

func (p *SMBProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}

	cleanOld := p.cleanPath(oldPath)
	cleanNew := p.cleanPath(newPath)
	fsWithCtx := p.fs.WithContext(ctx)

	err := fsWithCtx.Rename(cleanOld, cleanNew)
	if err != nil {
		return p.handleError(fmt.Errorf("smb rename failed: %w", err))
	}

	return nil
}

// SupportsAtomicRename is true: SMB rename is supported.
func (p *SMBProvider) SupportsAtomicRename() bool {
	return true
}

func (p *SMBProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	return "", ErrChecksumNotAvailable
}

func (p *SMBProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}

	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}

	return p.CreateDirectory(ctx, resourceType, dir)
}

var globalSMBCreatedDirs sync.Map

func (p *SMBProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SMB", resourceType)
	}

	cleanDirPath := p.cleanPath(dirPath)
	if cleanDirPath == "." {
		return nil
	}

	globalDirKey := p.Host + "|" + p.Share + "|" + cleanDirPath
	if _, exists := globalSMBCreatedDirs.Load(globalDirKey); exists {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}

	fsWithCtx := p.fs.WithContext(ctx)

	err := fsWithCtx.MkdirAll(cleanDirPath, 0755)
	if err != nil {
		return p.handleError(fmt.Errorf("smb mkdirall failed: %w", err))
	}

	globalSMBCreatedDirs.Store(globalDirKey, true)
	return nil
}

func (p *SMBProvider) ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error {
	if resourceType != "files" || meta.ModifiedTime.IsZero() {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)
	fsWithCtx := p.fs.WithContext(ctx)

	err := fsWithCtx.Chtimes(cleanPath, time.Now(), meta.ModifiedTime)
	if err != nil {
		return p.handleError(err)
	}

	return nil
}
