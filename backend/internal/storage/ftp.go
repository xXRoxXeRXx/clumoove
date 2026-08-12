package storage

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
)

const (
	ftpTimeout     = 30 * time.Second
	ftpShutTimeout = 5 * time.Second
)

type ftpTLSMode int

const (
	ftpExplicitTLS ftpTLSMode = iota
	ftpImplicitTLS
)

// FTPProvider supports only FTPS. Its mutex serializes the FTP control channel;
// a download retains that mutex until Response.Close reads the final FTP status.
type FTPProvider struct {
	Host      string
	Port      string
	Username  string
	Password  string
	mode      ftpTLSMode
	tlsConfig *tls.Config

	mu            sync.Mutex
	client        *ftp.ServerConn
	dialContext   context.Context
	controlDialed bool
	controlConn   net.Conn
}

var _ StorageProvider = (*FTPProvider)(nil)

func NewFTPProvider(rawURL, username, password string) (*FTPProvider, error) {
	host, port, mode, err := parseFTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := validateEgressHost(host); err != nil {
		return nil, err
	}

	return &FTPProvider{
		Host:      host,
		Port:      port,
		Username:  username,
		Password:  password,
		mode:      mode,
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
	}, nil
}

func parseFTPURL(rawURL string) (string, string, ftpTLSMode, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !u.IsAbs() || u.Hostname() == "" {
		return "", "", 0, errors.New("invalid FTPS URL")
	}
	if u.User != nil || u.Fragment != "" || u.Path != "" || u.RawPath != "" || u.ForceQuery {
		return "", "", 0, errors.New("FTPS URL must not contain userinfo, a path, or a fragment")
	}
	port := u.Port()
	if port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return "", "", 0, errors.New("invalid FTPS port")
		}
	}
	query := u.Query()
	if len(query) > 1 {
		return "", "", 0, errors.New("invalid FTPS URL parameters")
	}

	switch u.Scheme {
	case "ftps":
		if len(query) != 0 {
			return "", "", 0, errors.New("implicit FTPS URL must not contain parameters")
		}
		if port == "" {
			port = "990"
		}
		return u.Hostname(), port, ftpImplicitTLS, nil
	case "ftp":
		values, ok := query["tls"]
		if !ok || len(values) != 1 || values[0] != "explicit" {
			return "", "", 0, errors.New("FTP URL must specify tls=explicit")
		}
		if port == "" {
			port = "21"
		}
		return u.Hostname(), port, ftpExplicitTLS, nil
	default:
		return "", "", 0, errors.New("FTPS URL must use ftp or ftps scheme")
	}
}

func (p *FTPProvider) cleanup() {
	if p.client != nil {
		_ = p.client.Quit()
		p.client = nil
	}
	p.controlConn = nil
}

func (p *FTPProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanup()
	return nil
}

func (p *FTPProvider) dial(network, address string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid FTPS dial address: %w", err)
	}
	ctx := p.dialContext
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ftpTimeout)
		defer cancel()
	}

	// The FTP library provides the host from EPSV/PASV here. Ignore it: a PASV
	// response is untrusted and may only select a port on the configured host.
	conn, err := egressDialer(p.Host)(ctx, network, net.JoinHostPort(p.Host, port))
	if err != nil {
		return nil, err
	}

	isControl := !p.controlDialed
	p.controlDialed = true
	if !isControl {
		// For passive FTPS data channels, the FTP server can wait for LIST,
		// RETR, or STOR before it starts the TLS handshake. Returning an
		// unhandshaken TLS connection lets the FTP client send that command
		// first; the first data read or write then performs the handshake.
		return tls.Client(conn, p.tlsConfig), nil
	}
	if p.mode == ftpImplicitTLS {
		tlsConn := tls.Client(conn, p.tlsConfig)
		if err := tlsConn.SetDeadline(ftpHandshakeDeadline(ctx)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		p.controlConn = tlsConn
		return tlsConn, nil
	}
	if err := conn.SetDeadline(ftpHandshakeDeadline(ctx)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	p.controlConn = conn
	return conn, nil
}

func ftpHandshakeDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(ftpTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}
	return deadline
}

func (p *FTPProvider) ensureConnected(ctx context.Context) error {
	// Passive data connections are created lazily on an established FTP session.
	// Always refresh their dial context for the current provider operation.
	p.dialContext = ctx
	if p.client != nil {
		return nil
	}
	p.controlDialed = false
	addr := net.JoinHostPort(p.Host, p.Port)
	options := []ftp.DialOption{
		ftp.DialWithContext(ctx),
		ftp.DialWithTimeout(ftpTimeout),
		ftp.DialWithShutTimeout(ftpShutTimeout),
		ftp.DialWithDialFunc(p.dial),
	}
	if p.mode == ftpImplicitTLS {
		options = append(options, ftp.DialWithTLS(p.tlsConfig))
	} else {
		options = append(options, ftp.DialWithExplicitTLS(p.tlsConfig))
	}
	client, err := ftp.Dial(addr, options...)
	if err != nil {
		return err
	}
	if err := client.Login(p.Username, p.Password); err != nil {
		_ = client.Quit()
		p.controlConn = nil
		return err
	}
	if p.controlConn == nil {
		_ = client.Quit()
		return errors.New("FTPS control connection was not established")
	}
	if err := p.controlConn.SetDeadline(time.Time{}); err != nil {
		_ = client.Quit()
		p.controlConn = nil
		return fmt.Errorf("clear FTPS control connection deadline: %w", err)
	}
	p.client = client
	return nil
}

func isFTPAuthError(err error) bool {
	var protocolErr *textproto.Error
	return errors.As(err, &protocolErr) && protocolErr.Code == 530
}

func isFTPNotFound(err error) bool {
	var protocolErr *textproto.Error
	return errors.As(err, &protocolErr) && protocolErr.Code == 550
}

func (p *FTPProvider) handleError(err error) error {
	if err == nil {
		return nil
	}
	if isConnectionFailure(err) {
		p.cleanup()
	}
	return err
}

// getEntry prefers MLST but falls back to a deterministic parent listing for
// servers that do not advertise MLST. The fallback still uses the protected
// passive data-channel dial path.
func (p *FTPProvider) getEntry(cleanPath string) (*ftp.Entry, error) {
	entry, err := p.client.GetEntry(cleanPath)
	if err == nil || !isFTPMLSTUnsupported(err) {
		return entry, err
	}
	parent, name := path.Dir(cleanPath), path.Base(cleanPath)
	entries, err := p.client.List(parent)
	if err != nil {
		return nil, err
	}
	for _, candidate := range entries {
		if candidate.Name == name {
			return candidate, nil
		}
	}
	return nil, ErrNotFound
}

func isFTPMLSTUnsupported(err error) bool {
	var protocolErr *textproto.Error
	return errors.As(err, &protocolErr) && protocolErr.Code == ftp.StatusNotImplemented
}

func ftpPath(filePath string) (string, error) {
	for _, segment := range strings.Split(filePath, "/") {
		if segment == ".." {
			return "", ErrPathEscapesRoot
		}
	}
	clean := path.Clean("/" + strings.TrimPrefix(filePath, "/"))
	if clean == "." {
		return "/", nil
	}
	return clean, nil
}

func requireFTPFiles(resourceType string) error {
	if resourceType != "files" {
		return fmt.Errorf("FTP: %w", ErrUnsupportedResourceType)
	}
	return nil
}

func (p *FTPProvider) Connect(ctx context.Context) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		p.cleanup()
		if isFTPAuthError(err) {
			return false, fmt.Errorf("FTPS connect: %w", ErrAuth)
		}
		return false, errors.New("FTPS connection failed")
	}
	if err := ctx.Err(); err != nil {
		p.cleanup()
		return false, err
	}
	return true, nil
}

func resourceFromFTPEntry(parent string, entry *ftp.Entry) CloudResource {
	resourcePath := path.Join(parent, entry.Name)
	return CloudResource{
		Path:         "/" + strings.TrimPrefix(resourcePath, "/"),
		Name:         entry.Name,
		Size:         int64(entry.Size),
		IsDir:        entry.Type == ftp.EntryTypeFolder,
		LastModified: entry.Time,
	}
}

// isSafeFTPListingEntry excludes FTP's pseudo-directory entries and malformed
// names. Unlike most provider APIs, FTP LIST responses may contain "." and
// "..". Passing either to the client creates a path that points to the current
// or parent directory, which can turn a lazily expanded file tree into a cycle.
func isSafeFTPListingEntry(entry *ftp.Entry) bool {
	if entry == nil || entry.Name == "." || entry.Name == ".." {
		return false
	}
	// A LIST item names exactly one child of the requested directory. Reject
	// path-like names from non-conforming servers so path.Join cannot normalize
	// them into another directory.
	return entry.Name != "" && path.Base(entry.Name) == entry.Name
}

func (p *FTPProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if err := requireFTPFiles(resourceType); err != nil {
		return nil, err
	}
	clean, err := ftpPath(dirPath)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return nil, p.handleError(err)
	}
	entries, err := p.client.List(clean)
	if err != nil {
		return nil, p.handleError(err)
	}
	resources := make([]CloudResource, 0, len(entries))
	for _, entry := range entries {
		if !isSafeFTPListingEntry(entry) {
			continue
		}
		resources = append(resources, resourceFromFTPEntry(clean, entry))
	}
	return resources, nil
}

func (p *FTPProvider) InspectResource(ctx context.Context, resourceType, filePath string) (CloudResource, error) {
	if err := requireFTPFiles(resourceType); err != nil {
		return CloudResource{}, err
	}
	clean, err := ftpPath(filePath)
	if err != nil {
		return CloudResource{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return CloudResource{}, p.handleError(err)
	}
	entry, err := p.getEntry(clean)
	if err != nil {
		if errors.Is(err, ErrNotFound) || isFTPNotFound(err) {
			return CloudResource{}, ErrNotFound
		}
		return CloudResource{}, p.handleError(err)
	}
	return resourceFromFTPEntry(path.Dir(clean), entry), nil
}

type ftpDownload struct {
	response *ftp.Response
	provider *FTPProvider
	once     sync.Once
	err      error
}

func (r *ftpDownload) Read(buf []byte) (int, error) { return r.response.Read(buf) }

func (r *ftpDownload) Close() error {
	r.once.Do(func() {
		r.err = r.response.Close()
		if r.err != nil {
			r.provider.cleanup()
		}
		r.provider.mu.Unlock()
	})
	return r.err
}

func (p *FTPProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if err := requireFTPFiles(resourceType); err != nil {
		return nil, err
	}
	clean, err := ftpPath(filePath)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if err := p.ensureConnected(ctx); err != nil {
		p.handleError(err)
		p.mu.Unlock()
		return nil, err
	}
	response, err := p.client.Retr(clean)
	if err != nil {
		p.handleError(err)
		p.mu.Unlock()
		return nil, err
	}
	return &ftpDownload{response: response, provider: p}, nil
}

func (p *FTPProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if err := requireFTPFiles(resourceType); err != nil {
		return err
	}
	clean, err := ftpPath(filePath)
	if err != nil {
		return err
	}
	if err := p.CreateParentDirectories(ctx, resourceType, clean); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}
	if err := p.client.Stor(clean, stream); err != nil {
		return p.handleError(err)
	}
	return ctx.Err()
}

func (p *FTPProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	return p.StreamUpload(ctx, resourceType, filePath, &ProgressReader{Reader: stream, ProgressChan: progressChan}, size)
}

func (p *FTPProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if err := requireFTPFiles(resourceType); err != nil {
		return false, 0, err
	}
	clean, err := ftpPath(filePath)
	if err != nil {
		return false, 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return false, 0, p.handleError(err)
	}
	entry, err := p.getEntry(clean)
	if errors.Is(err, ErrNotFound) || isFTPNotFound(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, p.handleError(err)
	}
	return true, int64(entry.Size), nil
}

func (p *FTPProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if err := requireFTPFiles(resourceType); err != nil {
		return err
	}
	clean, err := ftpPath(filePath)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}
	if _, err := p.getEntry(clean); err != nil {
		if errors.Is(err, ErrNotFound) || isFTPNotFound(err) {
			return nil
		}
		return p.handleError(err)
	}
	if err := p.client.Delete(clean); err != nil {
		return p.handleError(err)
	}
	return nil
}

func (p *FTPProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if err := requireFTPFiles(resourceType); err != nil {
		return "", err
	}
	return "", ErrHashNotSupported
}

func (p *FTPProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if err := requireFTPFiles(resourceType); err != nil {
		return err
	}
	clean, err := ftpPath(filePath)
	if err != nil {
		return err
	}
	dir := path.Dir(clean)
	if dir == "/" || dir == "." {
		return nil
	}
	return p.CreateDirectory(ctx, resourceType, dir)
}

func (p *FTPProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if err := requireFTPFiles(resourceType); err != nil {
		return err
	}
	clean, err := ftpPath(dirPath)
	if err != nil {
		return err
	}
	if clean == "/" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}
	current := ""
	for _, component := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		current = path.Join(current, component)
		if err := p.client.MakeDir("/" + current); err != nil {
			// FTP has no standard "already exists" status. Confirm the path is
			// present before treating a failed MKD as idempotent.
			entry, statErr := p.getEntry("/" + current)
			if statErr != nil || entry.Type != ftp.EntryTypeFolder {
				return p.handleError(err)
			}
		}
	}
	return nil
}

func (p *FTPProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if err := requireFTPFiles(resourceType); err != nil {
		return err
	}
	oldClean, err := ftpPath(oldPath)
	if err != nil {
		return err
	}
	newClean, err := ftpPath(newPath)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnected(ctx); err != nil {
		return p.handleError(err)
	}
	if err := p.client.Rename(oldClean, newClean); err != nil {
		return p.handleError(err)
	}
	return nil
}

func (p *FTPProvider) VerificationMode() VerificationMode { return VerificationSizeOnly }
func (p *FTPProvider) SupportsAtomicRename() bool         { return true }
