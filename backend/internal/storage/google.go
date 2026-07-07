package storage

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

type GoogleProvider struct {
	driveService    *drive.Service
	calendarService *calendar.Service
	peopleService   *people.Service
}

func NewGoogleProvider(ctx context.Context, token string) (*GoogleProvider, error) {
	if token == "" {
		return nil, fmt.Errorf("google provider requires an oauth token")
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	client := oauth2.NewClient(ctx, ts)

	driveSvc, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create drive service: %v", err)
	}

	calendarSvc, err := calendar.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create calendar service: %v", err)
	}

	peopleSvc, err := people.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create people service: %v", err)
	}

	return &GoogleProvider{
		driveService:    driveSvc,
		calendarService: calendarSvc,
		peopleService:   peopleSvc,
	}, nil
}

func (p *GoogleProvider) Connect(ctx context.Context) (bool, error) {
	_, err := p.driveService.About.Get().Fields("user").Context(ctx).Do()
	if err != nil {
		return false, err
	}
	return true, nil
}

func (p *GoogleProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	var resources []CloudResource
	switch resourceType {
	case "files":
		parentID := "root"
		if dirPath != "/" && dirPath != "" {
			id, err := p.resolveDrivePath(ctx, dirPath)
			if err != nil {
				return nil, err
			}
			parentID = id
		}
		query := fmt.Sprintf("'%s' in parents and trashed = false", parentID)
		// Paginate through all results
		var pageToken string
		for {
			call := p.driveService.Files.List().
				Q(query).
				Fields("nextPageToken, files(id, name, mimeType, size, modifiedTime, md5Checksum)").
				Context(ctx)
			if pageToken != "" {
				call = call.PageToken(pageToken)
			}
			result, err := call.Do()
			if err != nil {
				return nil, err
			}
			for _, f := range result.Files {
				isDir := f.MimeType == "application/vnd.google-apps.folder"
				modTime, _ := time.Parse(time.RFC3339, f.ModifiedTime)
				fullPath := f.Name
				if dirPath == "/" || dirPath == "" {
					fullPath = "/" + f.Name
				} else {
					fullPath = strings.TrimSuffix(dirPath, "/") + "/" + f.Name
				}
				resources = append(resources, CloudResource{
					Path:         fullPath,
					Name:         f.Name,
					Size:         f.Size,
					IsDir:        isDir,
					Hash:         f.Md5Checksum,
					LastModified: modTime,
				})
			}
			if result.NextPageToken == "" {
				break
			}
			pageToken = result.NextPageToken
		}

	case "calendars":
		list, err := p.calendarService.CalendarList.List().Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		for _, c := range list.Items {
			resources = append(resources, CloudResource{
				Path:         "/" + c.Id,
				Name:         c.Summary,
				Size:         0,
				IsDir:        false,
				Hash:         c.Etag,
				LastModified: time.Now(),
			})
		}

	case "contacts":
		// Paginate through all contacts
		var pageToken string
		for {
			call := p.peopleService.People.Connections.List("people/me").
				PersonFields("names,emailAddresses").
				PageSize(1000)
			if pageToken != "" {
				call = call.PageToken(pageToken)
			}
			res, err := call.Do()
			if err != nil {
				return nil, err
			}
			for _, conn := range res.Connections {
				name := "Unknown"
				if len(conn.Names) > 0 {
					name = conn.Names[0].DisplayName
				}
				resources = append(resources, CloudResource{
					Path:         "/" + conn.ResourceName,
					Name:         name + ".vcf",
					Size:         0,
					IsDir:        false,
					Hash:         conn.Etag,
					LastModified: time.Now(),
				})
			}
			if res.NextPageToken == "" {
				break
			}
			pageToken = res.NextPageToken
		}

	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
	return resources, nil
}

// escapeDriveQuery escapes a string value for use inside a Drive API query.
// Single quotes must be escaped as \' to avoid query injection.
func escapeDriveQuery(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

func (p *GoogleProvider) resolveDrivePath(ctx context.Context, path string) (string, error) {
	if path == "/" || path == "" {
		return "root", nil
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	currentID := "root"
	for _, part := range parts {
		if part == "" {
			continue
		}
		query := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false and mimeType = 'application/vnd.google-apps.folder'",
			currentID, escapeDriveQuery(part))
		res, err := p.driveService.Files.List().Q(query).Fields("files(id)").Context(ctx).Do()
		if err != nil {
			return "", err
		}
		if len(res.Files) == 0 {
			return "", fmt.Errorf("path not found: %s", path)
		}
		currentID = res.Files[0].Id
	}
	return currentID, nil
}

func (p *GoogleProvider) resolveDriveFileID(ctx context.Context, filePath string) (string, error) {
	lastSlash := strings.LastIndex(filePath, "/")
	dirPath := filePath[:lastSlash]
	fileName := filePath[lastSlash+1:]
	if dirPath == "" {
		dirPath = "/"
	}
	parentID, err := p.resolveDrivePath(ctx, dirPath)
	if err != nil {
		return "", err
	}
	query := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false",
		parentID, escapeDriveQuery(fileName))
	res, err := p.driveService.Files.List().Q(query).Fields("files(id)").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	if len(res.Files) == 0 {
		return "", fmt.Errorf("file not found: %s", filePath)
	}
	return res.Files[0].Id, nil
}

// googleDocsExtension returns the export MIME type and file extension for a
// Google Workspace document MIME type. Returns ("", "") for non-Workspace types.
func googleDocsExtension(mimeType string) (exportMIME, extension string) {
	switch mimeType {
	case "application/vnd.google-apps.document":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"
	case "application/vnd.google-apps.spreadsheet":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"
	case "application/vnd.google-apps.presentation":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"
	case "application/vnd.google-apps.drawing":
		return "image/png", ".png"
	default:
		if strings.HasPrefix(mimeType, "application/vnd.google-apps.") {
			return "application/pdf", ".pdf"
		}
		return "", ""
	}
}

func (p *GoogleProvider) InspectResource(ctx context.Context, resourceType, path string) (CloudResource, error) {
	switch resourceType {
	case "files":
		id, err := p.resolveDriveFileID(ctx, path)
		if err != nil {
			return CloudResource{}, err
		}
		f, err := p.driveService.Files.Get(id).Fields("id, name, mimeType, size, modifiedTime, md5Checksum").Context(ctx).Do()
		if err != nil {
			return CloudResource{}, err
		}
		isDir := f.MimeType == "application/vnd.google-apps.folder"
		modTime, _ := time.Parse(time.RFC3339, f.ModifiedTime)

		// Adjust displayed name: append extension for exported Google Workspace files
		displayName := f.Name
		if _, ext := googleDocsExtension(f.MimeType); ext != "" {
			if !strings.HasSuffix(displayName, ext) {
				displayName += ext
			}
		}

		return CloudResource{
			Path:         path,
			Name:         displayName,
			Size:         f.Size,
			IsDir:        isDir,
			Hash:         f.Md5Checksum,
			LastModified: modTime,
		}, nil
	default:
		return CloudResource{}, fmt.Errorf("InspectResource not implemented for %s", resourceType)
	}
}

func (p *GoogleProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("StreamDownload not implemented for %s", resourceType)
	}

	id, err := p.resolveDriveFileID(ctx, filePath)
	if err != nil {
		return nil, err
	}

	fileMeta, err := p.driveService.Files.Get(id).Fields("mimeType").Context(ctx).Do()
	if err != nil {
		return nil, err
	}

	exportMIME, _ := googleDocsExtension(fileMeta.MimeType)
	if exportMIME != "" {
		resp, err := p.driveService.Files.Export(id, exportMIME).Context(ctx).Download()
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}

	resp, err := p.driveService.Files.Get(id).Context(ctx).Download()
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (p *GoogleProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return p.StreamUploadChunked(ctx, resourceType, filePath, stream, size, nil)
}

func (p *GoogleProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if resourceType != "files" {
		return fmt.Errorf("StreamUpload not implemented for %s", resourceType)
	}

	lastSlash := strings.LastIndex(filePath, "/")
	dirPath := filePath[:lastSlash]
	fileName := filePath[lastSlash+1:]
	if dirPath == "" {
		dirPath = "/"
	}

	parentID, err := p.resolveDrivePath(ctx, dirPath)
	if err != nil {
		return err
	}

	f := &drive.File{
		Name:    fileName,
		Parents: []string{parentID},
	}

	var uploadStream io.Reader = stream
	if progressChan != nil {
		uploadStream = &googleProgressReader{r: stream, progressChan: progressChan}
	}

	_, err = p.driveService.Files.Create(f).Context(ctx).Media(uploadStream).Do()
	return err
}

func (p *GoogleProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if resourceType != "files" {
		return false, 0, nil
	}
	res, err := p.InspectResource(ctx, resourceType, filePath)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, 0, nil
		}
		return false, 0, err
	}
	return true, res.Size, nil
}

func (p *GoogleProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("DeleteFile not implemented for %s", resourceType)
	}
	id, err := p.resolveDriveFileID(ctx, filePath)
	if err != nil {
		return err
	}
	return p.driveService.Files.Delete(id).Context(ctx).Do()
}

func (p *GoogleProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if resourceType != "files" {
		return "", nil
	}
	res, err := p.InspectResource(ctx, resourceType, filePath)
	if err != nil {
		return "", err
	}
	return res.Hash, nil
}

func (p *GoogleProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return nil
	}
	lastSlash := strings.LastIndex(filePath, "/")
	if lastSlash <= 0 {
		return nil
	}
	dirPath := filePath[:lastSlash]
	return p.CreateDirectory(ctx, resourceType, dirPath)
}

func (p *GoogleProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("CreateDirectory not implemented for %s", resourceType)
	}
	parts := strings.Split(strings.Trim(dirPath, "/"), "/")
	currentID := "root"
	for _, part := range parts {
		if part == "" {
			continue
		}
		query := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false and mimeType = 'application/vnd.google-apps.folder'",
			currentID, escapeDriveQuery(part))
		res, err := p.driveService.Files.List().Q(query).Fields("files(id)").Context(ctx).Do()
		if err != nil {
			return err
		}
		if len(res.Files) == 0 {
			f := &drive.File{
				Name:     part,
				MimeType: "application/vnd.google-apps.folder",
				Parents:  []string{currentID},
			}
			created, err := p.driveService.Files.Create(f).Context(ctx).Do()
			if err != nil {
				return err
			}
			currentID = created.Id
		} else {
			currentID = res.Files[0].Id
		}
	}
	return nil
}

type googleProgressReader struct {
	r            io.Reader
	progressChan chan<- int64
}

func (pr *googleProgressReader) Read(p []byte) (n int, err error) {
	n, err = pr.r.Read(p)
	if n > 0 && pr.progressChan != nil {
		pr.progressChan <- int64(n)
	}
	return n, err
}
