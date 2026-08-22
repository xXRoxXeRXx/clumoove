package storage

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SFTPProvider struct {
	Host        string
	Port        string
	Username    string
	Password    string
	PrivateKey  string
	hostKeyHash [sha256.Size]byte

	operationGate chan struct{}
	sshClient     *ssh.Client
	sftpClient    *sftp.Client
}

const sftpConnectTimeout = 15 * time.Second

var _ StorageProvider = (*SFTPProvider)(nil)

func sftpHandshakeDeadline(ctx context.Context, now time.Time) time.Time {
	deadline := now.Add(sftpConnectTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}
	return deadline
}

func NewSFTPProvider(rawURL, username, password string) (*SFTPProvider, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SFTP URL")
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

	hostKeyHash, err := parseSFTPHostKeyFingerprint(u.Query().Get("host_key"))
	if err != nil {
		return nil, err
	}

	var privateKey string
	trimmedPassword := strings.TrimSpace(password)
	if strings.HasPrefix(trimmedPassword, "-----BEGIN") {
		privateKey = trimmedPassword
		password = ""
	}

	return &SFTPProvider{
		Host:          host,
		Port:          port,
		Username:      username,
		Password:      password,
		PrivateKey:    privateKey,
		hostKeyHash:   hostKeyHash,
		operationGate: make(chan struct{}, 1),
	}, nil
}

// parseSFTPHostKeyFingerprint accepts the SHA-256 fingerprint format emitted
// by ssh-keygen (for example, SHA256:AbCd...). The fingerprint must be
// obtained from a trusted administrator or server console before configuring
// the connection; accepting a key learned from the network would permit MITM.
func parseSFTPHostKeyFingerprint(fingerprint string) ([sha256.Size]byte, error) {
	var expected [sha256.Size]byte
	encoded, ok := strings.CutPrefix(strings.TrimSpace(fingerprint), "SHA256:")
	if !ok || encoded == "" {
		return expected, fmt.Errorf("SFTP host_key must be a SHA256 SSH host key fingerprint")
	}

	// ssh-keygen emits the SHA-256 fingerprint as unpadded Base64.
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != sha256.Size {
		return expected, fmt.Errorf("invalid SFTP host_key fingerprint")
	}
	copy(expected[:], decoded)
	return expected, nil
}

func (p *SFTPProvider) verifyHostKey(hostname string, remote net.Addr, key ssh.PublicKey) error {
	actual := sha256.Sum256(key.Marshal())
	if subtle.ConstantTimeCompare(actual[:], p.hostKeyHash[:]) != 1 {
		return fmt.Errorf("SFTP host key mismatch for %s (remote %v, key type %s)", hostname, remote, key.Type())
	}
	return nil
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
	if isConnectionFailure(err) {
		p.cleanup()
	}
	return err
}

// lock serializes access to the SSH/SFTP session while still allowing a caller
// waiting for an in-flight transfer to abandon its request.
func (p *SFTPProvider) lock(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case p.operationGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *SFTPProvider) unlock() { <-p.operationGate }

// closeWhenDone closes only the captured SSH client when the operation context
// expires. It never accesses provider state. pkg/sftp has no context-aware
// operations; closing the underlying SSH client is the supported way to
// unblock its pending requests.
func closeWhenDone(ctx context.Context, sshClient *ssh.Client) func() {
	if ctx == nil || ctx.Done() == nil {
		return func() {}
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if sshClient != nil {
				_ = sshClient.Close()
			}
		case <-stopped:
		}
	}()
	return func() { close(stopped) }
}

// ensureConnected establishes the SSH and SFTP connections if not already connected.
// It must be called with operationGate held.
func (p *SFTPProvider) ensureConnected(ctx context.Context) error {
	if p.sftpClient != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
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
		HostKeyCallback: p.verifyHostKey,
		Timeout:         sftpConnectTimeout,
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
	dialCtx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()
	conn, err := egressDialer(p.Host)(dialCtx, "tcp", addr)
	if err != nil {
		if dialCtx.Err() != nil {
			return dialCtx.Err()
		}
		return fmt.Errorf("failed to connect to host %s: %w", addr, err)
	}
	// ssh.NewClientConn does not apply ClientConfig.Timeout when it receives an
	// already-dialed socket. Keep the socket deadline in place through both the
	// SSH handshake and SFTP subsystem startup, then clear it only for a fully
	// established session. The cancellation watcher below also closes the socket
	// for contexts cancelled before their deadline.
	if err := conn.SetDeadline(sftpHandshakeDeadline(dialCtx, time.Now())); err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to set SFTP connection deadline: %w", err)
	}
	// ssh.Client is not available until NewClientConn succeeds, so close the
	// raw connection during both the handshake and SFTP subsystem startup.
	stopped := make(chan struct{})
	go func() {
		select {
		case <-dialCtx.Done():
			_ = conn.Close()
		case <-stopped:
		}
	}()
	stopConnectionClose := func() { close(stopped) }
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		stopConnectionClose()
		_ = conn.Close()
		if dialCtx.Err() != nil {
			return dialCtx.Err()
		}
		return fmt.Errorf("failed to connect to host %s: %w", addr, err)
	}
	sshClient := ssh.NewClient(sshConn, chans, reqs)

	sftpClient, err := sftp.NewClient(sshClient)
	stopConnectionClose()
	if err != nil {
		_ = sshClient.Close()
		if dialCtx.Err() != nil {
			return dialCtx.Err()
		}
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}
	if dialCtx.Err() != nil {
		_ = sftpClient.Close()
		_ = sshClient.Close()
		return dialCtx.Err()
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = sftpClient.Close()
		_ = sshClient.Close()
		return fmt.Errorf("failed to clear SFTP connection deadline: %w", err)
	}

	p.sshClient = sshClient
	p.sftpClient = sftpClient
	return nil
}

func (p *SFTPProvider) cleanup() {
	// cleanup runs with operationGate held. A context watcher can concurrently
	// close only its captured sshClient; ssh.Client.Close is idempotent, and the
	// watcher never reads or mutates these provider fields. Keep that ownership
	// split if this teardown path changes.
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
	if err := p.lock(context.Background()); err != nil {
		return err
	}
	defer p.unlock()
	p.cleanup()
	return nil
}

// operation runs a non-streaming SFTP request with exclusive session access.
// The cancellation watcher closes the SSH client to interrupt pkg/sftp calls.
func (p *SFTPProvider) operation(ctx context.Context, fn func() error) error {
	if err := p.lock(ctx); err != nil {
		return err
	}
	defer p.unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}
	stop := closeWhenDone(ctx, p.sshClient)
	err := fn()
	stop()
	if ctx != nil && ctx.Err() != nil {
		p.cleanup()
		return ctx.Err()
	}
	return p.handleError(err)
}

func (p *SFTPProvider) Connect(ctx context.Context) (bool, error) {
	err := p.operation(ctx, func() error {
		_, err := p.sftpClient.ReadDir(".")
		return err
	})
	if err != nil {
		if isSFTPAuthError(err) {
			return false, fmt.Errorf("sftp connect: %w", ErrAuth)
		}
		if ctx != nil && ctx.Err() != nil {
			return false, ctx.Err()
		}
		slog.ErrorContext(ctx, "storage provider connection failed", slog.String("provider", "sftp"), slog.String("operation", "connect"))
		return false, fmt.Errorf("sftp connect: connection failed")
	}
	return true, nil
}

func (p *SFTPProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}
	if err := validateStoragePath(dirPath); err != nil {
		return nil, err
	}

	cleanDirPath := p.cleanPath(dirPath)
	var infos []os.FileInfo
	err := p.operation(ctx, func() error {
		var err error
		infos, err = p.sftpClient.ReadDir(cleanDirPath)
		return err
	})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("sftp list directory failed: %w", err)
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
	if err := validateStoragePath(filePath); err != nil {
		return CloudResource{}, err
	}

	cleanPath := p.cleanPath(filePath)
	var info os.FileInfo
	err := p.operation(ctx, func() error {
		var err error
		info, err = p.sftpClient.Stat(cleanPath)
		return err
	})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return CloudResource{}, ctx.Err()
		}
		if errors.Is(err, os.ErrNotExist) {
			return CloudResource{}, fmt.Errorf("sftp inspect: %w", ErrNotFound)
		}
		return CloudResource{}, fmt.Errorf("sftp inspect resource failed: %w", err)
	}

	return CloudResource{
		Path:         "/" + strings.TrimPrefix(cleanPath, "."),
		Name:         info.Name(),
		Size:         info.Size(),
		IsDir:        info.IsDir(),
		LastModified: info.ModTime(),
	}, nil
}

type sftpDownload struct {
	file     *sftp.File
	provider *SFTPProvider
	ctx      context.Context
	stop     func()
	once     sync.Once
	err      error
}

func (r *sftpDownload) Read(buf []byte) (int, error) {
	if r.ctx != nil && r.ctx.Err() != nil {
		return 0, r.ctx.Err()
	}
	n, err := r.file.Read(buf)
	if r.ctx != nil && r.ctx.Err() != nil {
		return n, r.ctx.Err()
	}
	return n, err
}

func (r *sftpDownload) Close() error {
	r.once.Do(func() {
		r.stop()
		r.err = r.file.Close()
		if (r.ctx != nil && r.ctx.Err() != nil) || r.err != nil {
			r.provider.cleanup()
		}
		r.provider.unlock()
	})
	return r.err
}

func (p *SFTPProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return nil, err
	}

	if err := p.lock(ctx); err != nil {
		return nil, err
	}
	if err := p.ensureConnected(ctx); err != nil {
		err = p.handleError(err)
		p.unlock()
		return nil, err
	}
	cleanPath := p.cleanPath(filePath)
	stop := closeWhenDone(ctx, p.sshClient)
	file, err := p.sftpClient.Open(cleanPath)
	if err != nil {
		stop()
		err = p.handleError(fmt.Errorf("sftp open file failed: %w", err))
		p.unlock()
		return nil, err
	}
	if ctx != nil && ctx.Err() != nil {
		stop()
		_ = file.Close()
		p.cleanup()
		p.unlock()
		return nil, ctx.Err()
	}
	// Keep session access exclusively owned until the stream is closed. This
	// prevents a cancelled stream from closing a session another operation uses.
	return &sftpDownload{file: file, provider: p, ctx: ctx, stop: stop}, nil
}

// StreamDownloadRange implements RangeDownloader for SFTPProvider.
func (p *SFTPProvider) StreamDownloadRange(ctx context.Context, resourceType, filePath string, offset, length int64) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}
	if _, err := ValidateByteRange(offset, length); err != nil {
		return nil, err
	}
	if err := validateStoragePath(filePath); err != nil {
		return nil, err
	}

	if err := p.lock(ctx); err != nil {
		return nil, err
	}
	if err := p.ensureConnected(ctx); err != nil {
		err = p.handleError(err)
		p.unlock()
		return nil, err
	}
	cleanPath := p.cleanPath(filePath)
	stop := closeWhenDone(ctx, p.sshClient)
	file, err := p.sftpClient.Open(cleanPath)
	if err != nil {
		stop()
		err = p.handleError(fmt.Errorf("sftp open file range failed: %w", err))
		p.unlock()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() || offset > info.Size() || length > info.Size()-offset {
		_ = file.Close()
		stop()
		p.unlock()
		if err != nil {
			return nil, p.handleError(err)
		}
		return nil, ErrInvalidByteRange
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		stop()
		p.unlock()
		return nil, p.handleError(err)
	}

	return newRangedReadCloser(&sftpDownload{file: file, provider: p, ctx: ctx, stop: stop}, length), nil
}

func (p *SFTPProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return err
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	cleanPath := p.cleanPath(filePath)
	return p.operation(ctx, func() error {
		file, err := p.sftpClient.Create(cleanPath)
		if err != nil {
			return fmt.Errorf("sftp create file failed: %w", err)
		}
		defer file.Close()
		if _, err := io.Copy(file, &sftpContextReader{ctx: ctx, reader: stream}); err != nil {
			return fmt.Errorf("sftp write file failed: %w", err)
		}
		return nil
	})
}

type sftpContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *sftpContextReader) Read(p []byte) (int, error) {
	if r.ctx != nil && r.ctx.Err() != nil {
		return 0, r.ctx.Err()
	}
	n, err := r.reader.Read(p)
	if r.ctx != nil && r.ctx.Err() != nil {
		return n, r.ctx.Err()
	}
	return n, err
}

func (p *SFTPProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	progressReader := &ProgressReader{
		Reader:       stream,
		ProgressChan: progressChan,
	}
	return p.StreamUpload(ctx, resourceType, filePath, progressReader, size)
}

func (p *SFTPProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if resourceType != "files" {
		return false, 0, fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return false, 0, err
	}

	cleanPath := p.cleanPath(filePath)
	var info os.FileInfo
	err := p.operation(ctx, func() error {
		var err error
		info, err = p.sftpClient.Stat(cleanPath)
		return err
	})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return false, 0, ctx.Err()
		}
		if errors.Is(err, os.ErrNotExist) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("sftp stat failed: %w", err)
	}

	return true, info.Size(), nil
}

func (p *SFTPProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return err
	}

	cleanPath := p.cleanPath(filePath)
	err := p.operation(ctx, func() error { return p.sftpClient.Remove(cleanPath) })
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("sftp remove failed: %w", err)
	}

	return nil
}

func (p *SFTPProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}
	if err := validateStoragePath(oldPath); err != nil {
		return err
	}
	if err := validateStoragePath(newPath); err != nil {
		return err
	}

	cleanOld := p.cleanPath(oldPath)
	cleanNew := p.cleanPath(newPath)
	err := p.operation(ctx, func() error { return p.sftpClient.Rename(cleanOld, cleanNew) })
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("sftp rename failed: %w", err)
	}

	return nil
}

// SupportsAtomicRename is true: SFTP rename is supported.
func (p *SFTPProvider) VerificationMode() VerificationMode { return VerificationSizeOnly }
func (p *SFTPProvider) SupportsAtomicRename() bool {
	return true
}

func (p *SFTPProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if resourceType != "files" {
		return "", fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return "", err
	}
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	return "", ErrChecksumNotAvailable
}

func (p *SFTPProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by SFTP", resourceType)
	}
	if err := validateStoragePath(filePath); err != nil {
		return err
	}

	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
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
	if err := validateStoragePath(dirPath); err != nil {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	cleanDirPath := p.cleanPath(dirPath)
	if cleanDirPath == "." || cleanDirPath == "/" {
		return nil
	}

	err := p.operation(ctx, func() error { return p.sftpClient.MkdirAll(cleanDirPath) })
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("sftp mkdirall failed: %w", err)
	}

	return nil
}

func (p *SFTPProvider) ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error {
	if resourceType != "files" || meta.ModifiedTime.IsZero() {
		return nil
	}
	if err := validateStoragePath(filePath); err != nil {
		return err
	}
	cleanPath := p.cleanPath(filePath)
	if err := p.operation(ctx, func() error {
		// Metadata propagation is intentionally best effort at the processor,
		// but provider failures must be returned so it can record the warning.
		if err := p.sftpClient.Chtimes(cleanPath, time.Now(), meta.ModifiedTime); err != nil {
			return fmt.Errorf("sftp apply metadata: set file modification time: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}
