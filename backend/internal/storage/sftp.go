package storage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
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

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SFTPProvider struct {
	Host       string
	Port       string
	Username   string
	Password   string
	PrivateKey string

	mu         sync.Mutex
	sshClient  *ssh.Client
	sftpClient *sftp.Client
}

var _ StorageProvider = (*SFTPProvider)(nil)

func NewSFTPProvider(rawURL, username, password string) (*SFTPProvider, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SFTP URL: %w", err)
	}

	if u.Scheme != "sftp" {
		return nil, fmt.Errorf("invalid scheme %q, expected sftp", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing host in SFTP URL")
	}

	port := u.Port()
	if port == "" {
		port = "22"
	}

	var privateKey string
	trimmedPassword := strings.TrimSpace(password)
	if strings.HasPrefix(trimmedPassword, "-----BEGIN") {
		privateKey = trimmedPassword
		password = ""
	}

	return &SFTPProvider{
		Host:       host,
		Port:       port,
		Username:   username,
		Password:   password,
		PrivateKey: privateKey,
	}, nil
}

func (p *SFTPProvider) cleanPath(filePath string) string {
	filePath = strings.ReplaceAll(filePath, "\\", "/")
	filePath = path.Clean("/" + filePath)
	if filePath == "" {
		return "."
	}
	return filePath
}

func isSFTPAuthError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "authentication") ||
		strings.Contains(errStr, "permission denied") ||
		strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "unable to authenticate")
}

func (p *SFTPProvider) handleError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return err
	}
	p.cleanup()
	return err
}

// ensureConnected establishes the SSH and SFTP connections if not already connected.
// Note: pkg/sftp does not support context propagation on individual operations
// (no WithContext equivalent). The SSH dial uses a fixed 15s timeout.
func (p *SFTPProvider) ensureConnected() error {
	if p.sftpClient != nil {
		return nil
	}

	var authMethods []ssh.AuthMethod
	if p.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(p.PrivateKey))
		if err != nil {
			return fmt.Errorf("failed to parse private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if p.Password != "" {
		authMethods = append(authMethods, ssh.Password(p.Password))
	}
	if len(authMethods) == 0 {
		return fmt.Errorf("no authentication method provided")
	}

	config := &ssh.ClientConfig{
		User:            p.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	// Pin egress to a re-validated IP on every connection so a DNS rebind
	// between the construction-time SSRF check (validateEgressURL, called from
	// the factory) and the actual dial cannot reach internal/metadata
	// addresses. Mirrors the WebDAV/Nextcloud/S3 egressDialer behaviour;
	// without this, SFTP was the only provider still exposed to the
	// DNS-rebinding (TOCTOU) SSRF window. ssh.Dial has no custom-dialer hook in
	// this x/crypto version, so we dial the connection ourselves via
	// egressDialer and hand it to NewClientConn.
	addr := net.JoinHostPort(p.Host, p.Port)
	dialCtx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()
	conn, err := egressDialer(p.Host)(dialCtx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to host %s: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to connect to host %s: %w", addr, err)
	}
	sshClient := ssh.NewClient(sshConn, chans, reqs)

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}

	p.sshClient = sshClient
	p.sftpClient = sftpClient
	return nil
}

func (p *SFTPProvider) cleanup() {
	if p.sftpClient != nil {
		_ = p.sftpClient.Close()
		p.sftpClient = nil
	}
	if p.sshClient != nil {
		_ = p.sshClient.Close()
		p.sshClient = nil
	}
}

func (p *SFTPProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanup()
	return nil
}

func (p *SFTPProvider) Connect(ctx context.Context) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureConnected(); err != nil {
		if isSFTPAuthError(err) {
			return false, fmt.Errorf("sftp connect: %w", ErrAuth)
		}
		log.Printf("sftp connect failed: %v", err)
		return false, fmt.Errorf("sftp connect: connection failed")
	}

	_, err := p.sftpClient.ReadDir(".")
	if err != nil {
		p.cleanup()
		if isSFTPAuthError(err) {
			return false, fmt.Errorf("sftp connect: %w", ErrAuth)
		}
		log.Printf("sftp read root failed: %v", err)
		return false, fmt.Errorf("sftp connect: failed to list root directory")
	}

	return true, nil
}

func (p *SFTPProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return nil, p.handleError(err)
	}

	cleanDirPath := p.cleanPath(dirPath)

	infos, err := p.sftpClient.ReadDir(cleanDirPath)
	if err != nil {
		return nil, p.handleError(fmt.Errorf("sftp list directory failed: %w", err))
	}

	var resources []CloudResource
	for _, info := range infos {
		name := info.Name()
		var relPath string
		if cleanDirPath == "." || cleanDirPath == "/" {
			relPath = name
		} else {
			relPath = path.Join(cleanDirPath, name)
		}

		resources = append(resources, CloudResource{
			Path:         "/" + strings.TrimPrefix(relPath, "/"),
			Name:         name,
			Size:         info.Size(),
			IsDir:        info.IsDir(),
			LastModified: info.ModTime(),
		})
	}

	return resources, nil
}

func (p *SFTPProvider) InspectResource(ctx context.Context, resourceType, filePath string) (CloudResource, error) {
	if resourceType != "files" {
		return CloudResource{}, fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return CloudResource{}, p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)

	info, err := p.sftpClient.Stat(cleanPath)
	if err != nil {
		return CloudResource{}, p.handleError(fmt.Errorf("sftp inspect resource failed: %w", err))
	}

	return CloudResource{
		Path:         "/" + strings.TrimPrefix(cleanPath, "."),
		Name:         info.Name(),
		Size:         info.Size(),
		IsDir:        info.IsDir(),
		LastModified: info.ModTime(),
	}, nil
}

func (p *SFTPProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return nil, p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)

	file, err := p.sftpClient.Open(cleanPath)
	if err != nil {
		return nil, p.handleError(fmt.Errorf("sftp open file failed: %w", err))
	}

	return file, nil
}

func (p *SFTPProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)

	file, err := p.sftpClient.Create(cleanPath)
	if err != nil {
		return p.handleError(fmt.Errorf("sftp create file failed: %w", err))
	}
	defer file.Close()

	if _, err := io.Copy(file, stream); err != nil {
		return p.handleError(fmt.Errorf("sftp write file failed: %w", err))
	}

	return nil
}

type sftpProgressReader struct {
	reader       io.Reader
	progressChan chan<- int64
}

func (pr *sftpProgressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 && pr.progressChan != nil {
		pr.progressChan <- int64(n)
	}
	return n, err
}

func (p *SFTPProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	progressReader := &sftpProgressReader{
		reader:       stream,
		progressChan: progressChan,
	}
	return p.StreamUpload(ctx, resourceType, filePath, progressReader, size)
}

func (p *SFTPProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if resourceType != "files" {
		return false, 0, fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return false, 0, p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)

	info, err := p.sftpClient.Stat(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, 0, nil
		}
		return false, 0, p.handleError(fmt.Errorf("sftp stat failed: %w", err))
	}

	return true, info.Size(), nil
}

func (p *SFTPProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return p.handleError(err)
	}

	cleanPath := p.cleanPath(filePath)

	err := p.sftpClient.Remove(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return p.handleError(fmt.Errorf("sftp remove failed: %w", err))
	}

	return nil
}

func (p *SFTPProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return p.handleError(err)
	}

	cleanOld := p.cleanPath(oldPath)
	cleanNew := p.cleanPath(newPath)

	err := p.sftpClient.Rename(cleanOld, cleanNew)
	if err != nil {
		return p.handleError(fmt.Errorf("sftp rename failed: %w", err))
	}

	return nil
}

// SupportsAtomicRename is true: SFTP rename is supported.
func (p *SFTPProvider) SupportsAtomicRename() bool {
	return true
}

func (p *SFTPProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if resourceType != "files" {
		return "", fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return "", p.handleError(err)
	}

	if p.sshClient == nil || p.sftpClient == nil {
		return "", nil
	}

	cleanPath := p.cleanPath(filePath)

	// Compute the SHA-1 in-process by streaming the remote file through the
	// SFTP client instead of shelling out to `sha1sum` over an SSH session.
	// This removes the only remote-shell dependency and any quoting concerns.
	f, err := p.sftpClient.Open(cleanPath)
	if err != nil {
		// A missing/uned file is not a hash error; mirror the prior
		// behaviour of returning an empty hash in that case.
		return "", nil
	}
	defer f.Close()

	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", nil
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p *SFTPProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}

	return p.CreateDirectory(ctx, resourceType, dir)
}

func (p *SFTPProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return p.handleError(err)
	}

	cleanDirPath := p.cleanPath(dirPath)
	if cleanDirPath == "." || cleanDirPath == "/" {
		return nil
	}

	err := p.sftpClient.MkdirAll(cleanDirPath)
	if err != nil {
		return p.handleError(fmt.Errorf("sftp mkdirall failed: %w", err))
	}

	return nil
}

func (p *SFTPProvider) ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error {
	if resourceType != "files" || meta.ModifiedTime.IsZero() {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(); err != nil {
		return nil
	}

	cleanPath := p.cleanPath(filePath)
	_ = p.sftpClient.Chtimes(cleanPath, time.Now(), meta.ModifiedTime)
	return nil
}
