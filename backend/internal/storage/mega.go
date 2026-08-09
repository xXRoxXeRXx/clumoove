package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	mega "github.com/t3rm1n4l/go-mega"
)

// MegaProvider exposes only the authenticated user's personal Cloud Drive.
// A provider is task-scoped: its in-memory tree and cleartext session material
// are never shared across accounts or operations.
type MegaProvider struct {
	email      string
	password   string
	session    MegaSession
	client     *mega.Mega
	httpClient *http.Client
	mu         sync.Mutex
}

func NewMegaProvider(email, password string, session MegaSession) *MegaProvider {
	return &MegaProvider{email: email, password: password, session: session}
}

func (p *MegaProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.httpClient != nil {
		p.httpClient.CloseIdleConnections()
	}
	for i := range p.session.MasterKey {
		p.session.MasterKey[i] = 0
	}
	p.session = MegaSession{}
	p.client = nil
	return nil
}

func (p *MegaProvider) Connect(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return true, nil
	}
	if strings.TrimSpace(p.email) == "" || p.password == "" {
		return false, fmt.Errorf("%w: missing MEGA email or password", ErrAuth)
	}

	httpClient := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	client := mega.New().SetClient(httpClient)
	client.SetHTTPS(true)
	client.SetLogger(func(string, ...any) {})
	client.SetDebugger(func(string, ...any) {})

	if p.session.ID != "" && len(p.session.MasterKey) > 0 {
		if err := client.LoginWithKeys(p.session.ID, p.session.MasterKey); err == nil {
			p.client, p.httpClient = client, httpClient
			return true, nil
		}
	}
	if err := client.Login(p.email, p.password); err != nil {
		return false, classifyMegaError(err)
	}
	key := append([]byte(nil), client.GetMasterKey()...)
	for i := range p.session.MasterKey {
		p.session.MasterKey[i] = 0
	}
	p.session = MegaSession{ID: client.GetSessionID(), MasterKey: key}
	p.client, p.httpClient = client, httpClient
	return true, nil
}

// Session returns an independent copy suitable for encryption by the caller.
func (p *MegaProvider) Session() MegaSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return MegaSession{ID: p.session.ID, MasterKey: append([]byte(nil), p.session.MasterKey...)}
}

func classifyMegaError(err error) error {
	switch err {
	case mega.EMFAREQUIRED:
		return fmt.Errorf("%w: %w", ErrAuth, ErrMegaMFARequired)
	case mega.ESID, mega.EACCESS, mega.EBLOCKED, mega.EKEY:
		return fmt.Errorf("%w: MEGA rejected credentials", ErrAuth)
	default:
		return fmt.Errorf("MEGA request failed: %w", err)
	}
}

func (p *MegaProvider) requireFiles(resourceType string) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}
	return nil
}

func cleanMegaPath(value string) ([]string, string, error) {
	if strings.Contains(value, "\\") {
		return nil, "", ErrPathEscapesRoot
	}
	clean := path.Clean("/" + value)
	trimmed := strings.Trim(clean, "/")
	if trimmed == "" {
		return nil, "/", nil
	}
	return strings.Split(trimmed, "/"), "/" + trimmed, nil
}

func (p *MegaProvider) lookup(value string) (*mega.Node, string, error) {
	parts, clean, err := cleanMegaPath(value)
	if err != nil {
		return nil, "", err
	}
	if p.client == nil || p.client.FS == nil {
		return nil, "", ErrNotConnected
	}
	root := p.client.FS.GetRoot()
	if len(parts) == 0 {
		return root, clean, nil
	}
	// Resolve manually because PathLookup chooses an arbitrary same-name child.
	current := root
	for _, part := range parts {
		children, childErr := p.client.FS.GetChildren(current)
		if childErr != nil {
			return nil, clean, classifyMegaError(childErr)
		}
		var match *mega.Node
		for _, child := range children {
			if child.GetName() == part {
				if match != nil {
					return nil, clean, ErrAmbiguousPath
				}
				match = child
			}
		}
		if match == nil {
			return nil, clean, ErrNotFound
		}
		current = match
	}
	return current, clean, nil
}

func megaResource(node *mega.Node, fullPath string) (CloudResource, error) {
	if node.GetType() != mega.FILE && node.GetType() != mega.FOLDER {
		return CloudResource{}, ErrNotFound
	}
	return CloudResource{
		Path:         fullPath,
		Name:         node.GetName(),
		Size:         node.GetSize(),
		IsDir:        node.GetType() == mega.FOLDER,
		LastModified: node.GetTimeStamp(),
	}, nil
}

func (p *MegaProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if err := p.requireFiles(resourceType); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	dir, clean, err := p.lookup(dirPath)
	if err != nil {
		return nil, err
	}
	if dir.GetType() != mega.FOLDER {
		return nil, ErrNotFound
	}
	children, err := p.client.FS.GetChildren(dir)
	if err != nil {
		return nil, classifyMegaError(err)
	}
	seen := make(map[string]struct{}, len(children))
	items := make([]CloudResource, 0, len(children))
	for _, child := range children {
		if child.GetType() != mega.FILE && child.GetType() != mega.FOLDER {
			continue
		}
		name := child.GetName()
		if _, exists := seen[name]; exists {
			return nil, ErrAmbiguousPath
		}
		seen[name] = struct{}{}
		item, err := megaResource(child, path.Join(clean, name))
		if err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (p *MegaProvider) InspectResource(ctx context.Context, resourceType, filePath string) (CloudResource, error) {
	if err := p.requireFiles(resourceType); err != nil {
		return CloudResource{}, err
	}
	if err := ctx.Err(); err != nil {
		return CloudResource{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	node, clean, err := p.lookup(filePath)
	if err != nil {
		return CloudResource{}, err
	}
	return megaResource(node, clean)
}

type megaDownloadReader struct {
	ctx      context.Context
	download *mega.Download
	chunk    int
	data     []byte
	offset   int
	closed   bool
}

func (r *megaDownloadReader) Read(dst []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	for r.offset == len(r.data) {
		if r.chunk >= r.download.Chunks() {
			return 0, io.EOF
		}
		data, err := r.download.DownloadChunk(r.chunk)
		if err != nil {
			return 0, classifyMegaError(err)
		}
		r.chunk++
		r.data, r.offset = data, 0
	}
	n := copy(dst, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func (r *megaDownloadReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.data = nil
	if err := r.ctx.Err(); err != nil {
		_ = r.download.Finish()
		return err
	}
	return classifyMegaError(r.download.Finish())
}

func (p *MegaProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if err := p.requireFiles(resourceType); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	node, _, err := p.lookup(filePath)
	if err != nil {
		return nil, err
	}
	if node.GetType() != mega.FILE {
		return nil, ErrNotFound
	}
	download, err := p.client.NewDownload(node)
	if err != nil {
		return nil, classifyMegaError(err)
	}
	return &megaDownloadReader{ctx: ctx, download: download}, nil
}

func (p *MegaProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return p.StreamUploadChunked(ctx, resourceType, filePath, stream, size, nil)
}

func (p *MegaProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	if size < 0 {
		return errors.New("MEGA upload requires a known size")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	parts, clean, err := cleanMegaPath(filePath)
	if err != nil || len(parts) == 0 {
		return ErrPathEscapesRoot
	}
	parent, _, err := p.lookup(path.Dir(clean))
	if err != nil {
		return err
	}
	if parent.GetType() != mega.FOLDER {
		return ErrNotFound
	}
	if _, _, err := p.uniqueChild(parent, parts[len(parts)-1]); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	upload, err := p.client.NewUpload(parent, parts[len(parts)-1], size)
	if err != nil {
		return classifyMegaError(err)
	}
	maxChunkSize := 0
	for chunk := 0; chunk < upload.Chunks(); chunk++ {
		_, chunkSize, locationErr := upload.ChunkLocation(chunk)
		if locationErr != nil {
			return classifyMegaError(locationErr)
		}
		if chunkSize > maxChunkSize {
			maxChunkSize = chunkSize
		}
	}
	buffer := make([]byte, maxChunkSize)
	for chunk := 0; chunk < upload.Chunks(); chunk++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, chunkSize, err := upload.ChunkLocation(chunk)
		if err != nil {
			return classifyMegaError(err)
		}
		chunkBuffer := buffer[:chunkSize]
		if _, err := io.ReadFull(stream, chunkBuffer); err != nil {
			return err
		}
		if err := upload.UploadChunk(chunk, chunkBuffer); err != nil {
			return classifyMegaError(err)
		}
		if progressChan != nil && chunkSize > 0 {
			progressChan <- int64(chunkSize)
		}
	}
	if _, err := upload.Finish(); err != nil {
		return classifyMegaError(err)
	}
	return nil
}

func (p *MegaProvider) uniqueChild(parent *mega.Node, name string) (*mega.Node, bool, error) {
	children, err := p.client.FS.GetChildren(parent)
	if err != nil {
		return nil, false, classifyMegaError(err)
	}
	var match *mega.Node
	for _, child := range children {
		if child.GetName() == name {
			if match != nil {
				return nil, false, ErrAmbiguousPath
			}
			match = child
		}
	}
	return match, match != nil, nil
}

func (p *MegaProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	item, err := p.InspectResource(ctx, resourceType, filePath)
	if errors.Is(err, ErrNotFound) {
		return false, 0, nil
	}
	return err == nil, item.Size, err
}

func (p *MegaProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	node, _, err := p.lookup(filePath)
	if err != nil {
		return err
	}
	if err := p.client.Delete(node, false); err != nil {
		return classifyMegaError(err)
	}
	return nil
}

func (p *MegaProvider) GetFileHash(context.Context, string, string) (string, error) {
	return "", ErrHashNotSupported
}

func (p *MegaProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	_, clean, err := cleanMegaPath(filePath)
	if err != nil {
		return err
	}
	return p.CreateDirectory(ctx, resourceType, path.Dir(clean))
}

func (p *MegaProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	parts, _, err := cleanMegaPath(dirPath)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil || p.client.FS == nil {
		return ErrNotConnected
	}
	current := p.client.FS.GetRoot()
	for _, part := range parts {
		child, exists, err := p.uniqueChild(current, part)
		if err != nil {
			return err
		}
		if exists {
			if child.GetType() != mega.FOLDER {
				return fmt.Errorf("MEGA path component is a file")
			}
			current = child
			continue
		}
		current, err = p.client.CreateDir(part, current)
		if err != nil {
			return classifyMegaError(err)
		}
	}
	return nil
}

func (p *MegaProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	source, _, err := p.lookup(oldPath)
	if err != nil {
		return err
	}
	parts, clean, err := cleanMegaPath(newPath)
	if err != nil || len(parts) == 0 {
		return ErrPathEscapesRoot
	}
	parent, _, err := p.lookup(path.Dir(clean))
	if err != nil {
		return err
	}
	if _, exists, err := p.uniqueChild(parent, parts[len(parts)-1]); err != nil || exists {
		if err != nil {
			return err
		}
		return fmt.Errorf("MEGA destination already exists")
	}
	if err := p.client.Move(source, parent); err != nil {
		return classifyMegaError(err)
	}
	if source.GetName() != parts[len(parts)-1] {
		if err := p.client.Rename(source, parts[len(parts)-1]); err != nil {
			return classifyMegaError(err)
		}
	}
	return nil
}

func (*MegaProvider) SupportsAtomicRename() bool         { return true }
func (*MegaProvider) VerificationMode() VerificationMode { return VerificationSizeOnly }
