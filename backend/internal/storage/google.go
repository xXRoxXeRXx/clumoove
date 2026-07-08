package storage

import (
	"bufio"
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
	// Verify Drive access
	if _, err := p.driveService.About.Get().Fields("user").Context(ctx).Do(); err != nil {
		return false, fmt.Errorf("google drive not accessible: %w", err)
	}
	// Verify Calendar access
	if _, err := p.calendarService.CalendarList.List().MaxResults(1).Context(ctx).Do(); err != nil {
		return false, fmt.Errorf("google calendar not accessible: %w", err)
	}
	// Verify People (Contacts) access
	if _, err := p.peopleService.People.Get("people/me").PersonFields("names").Context(ctx).Do(); err != nil {
		return false, fmt.Errorf("google contacts (people) not accessible: %w", err)
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
				displayName := f.Name
				size := f.Size
				if _, ext := googleDocsExtension(f.MimeType); ext != "" {
					if !strings.HasSuffix(displayName, ext) {
						displayName += ext
					}
					size = 0 // Google Workspace files do not have a pre-determined export size
				}

				fullPath := displayName
				if dirPath == "/" || dirPath == "" {
					fullPath = "/" + displayName
				} else {
					fullPath = strings.TrimSuffix(dirPath, "/") + "/" + displayName
				}

				resources = append(resources, CloudResource{
					Path:         fullPath,
					Name:         displayName,
					Size:         size,
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
		if dirPath == "/" || dirPath == "" {
			list, err := p.calendarService.CalendarList.List().Context(ctx).Do()
			if err != nil {
				return nil, err
			}
			for _, c := range list.Items {
				resources = append(resources, CloudResource{
					Path:         "/" + c.Id,
					Name:         c.Summary,
					Size:         0,
					IsDir:        true,
					Hash:         c.Etag,
					LastModified: time.Now(),
				})
			}
		} else {
			calendarID := strings.TrimPrefix(dirPath, "/")
			var pageToken string
			for {
				call := p.calendarService.Events.List(calendarID).
					Fields("nextPageToken, items(id, summary, updated, etag)").
					Context(ctx)
				if pageToken != "" {
					call = call.PageToken(pageToken)
				}
				events, err := call.Do()
				if err != nil {
					return nil, err
				}
				for _, e := range events.Items {
					modTime, _ := time.Parse(time.RFC3339, e.Updated)
					name := e.Id + ".ics"
					if e.Summary != "" {
						name = e.Summary + ".ics"
					}
					resources = append(resources, CloudResource{
						Path:         dirPath + "/" + e.Id + ".ics",
						Name:         name,
						Size:         0,
						IsDir:        false,
						Hash:         e.Etag,
						LastModified: modTime,
					})
				}
				if events.NextPageToken == "" {
					break
				}
				pageToken = events.NextPageToken
			}
		}

	case "contacts":
		if dirPath == "/" || dirPath == "" {
			resources = append(resources, CloudResource{
				Path:         "/contacts",
				Name:         "All Contacts",
				Size:         0,
				IsDir:        true,
				Hash:         "virtual",
				LastModified: time.Now(),
			})
		} else {
			var pageToken string
			for {
				call := p.peopleService.People.Connections.List("people/me").
					PersonFields("names,emailAddresses,phoneNumbers").
					PageSize(1000).
					Context(ctx)
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
					id := strings.TrimPrefix(conn.ResourceName, "people/")
					resources = append(resources, CloudResource{
						Path:         "/contacts/" + id + ".vcf",
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
		}

	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
	return resources, nil
}

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

	var candidates []string
	candidates = append(candidates, fileName)

	// If fileName ends with a known Google Doc extension, add the stripped name as a candidate
	for _, ext := range []string{".docx", ".xlsx", ".pptx", ".png", ".pdf"} {
		if strings.HasSuffix(strings.ToLower(fileName), ext) {
			candidates = append(candidates, fileName[:len(fileName)-len(ext)])
			break
		}
	}

	var queryParts []string
	for _, c := range candidates {
		queryParts = append(queryParts, fmt.Sprintf("name = '%s'", escapeDriveQuery(c)))
	}
	nameQuery := "(" + strings.Join(queryParts, " or ") + ")"
	query := fmt.Sprintf("'%s' in parents and %s and trashed = false", parentID, nameQuery)

	res, err := p.driveService.Files.List().Q(query).Fields("files(id, name, mimeType)").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	if len(res.Files) == 0 {
		return "", fmt.Errorf("file not found: %s", filePath)
	}

	// Check for exact match (direct name match or Google Doc matching the requested extension)
	for _, f := range res.Files {
		if f.Name == fileName {
			return f.Id, nil
		}
		_, ext := googleDocsExtension(f.MimeType)
		if ext != "" && f.Name+ext == fileName {
			return f.Id, nil
		}
	}

	// Fallback to the first item if no exact match was resolved
	return res.Files[0].Id, nil
}

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

		displayName := f.Name
		size := f.Size
		if _, ext := googleDocsExtension(f.MimeType); ext != "" {
			if !strings.HasSuffix(displayName, ext) {
				displayName += ext
			}
			size = 0 // Google Workspace files do not have a pre-determined export size
		}

		return CloudResource{
			Path:         path,
			Name:         displayName,
			Size:         size,
			IsDir:        isDir,
			Hash:         f.Md5Checksum,
			LastModified: modTime,
		}, nil
	default:
		return CloudResource{}, fmt.Errorf("InspectResource not implemented for %s", resourceType)
	}
}

func (p *GoogleProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	switch resourceType {
	case "files":
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

	case "calendars":
		parts := strings.Split(strings.TrimPrefix(filePath, "/"), "/")
		if len(parts) == 2 {
			calendarID := parts[0]
			eventID := strings.TrimSuffix(parts[1], ".ics")
			event, err := p.calendarService.Events.Get(calendarID, eventID).Context(ctx).Do()
			if err != nil {
				return nil, err
			}
			icsData := formatEventToICS(event)
			return io.NopCloser(strings.NewReader(icsData)), nil
		}
		return nil, fmt.Errorf("invalid calendar event path: %s", filePath)

	case "contacts":
		parts := strings.Split(strings.TrimPrefix(filePath, "/"), "/")
		if len(parts) == 2 && parts[0] == "contacts" {
			personID := "people/" + strings.TrimSuffix(parts[1], ".vcf")
			person, err := p.peopleService.People.Get(personID).
				PersonFields("names,emailAddresses,phoneNumbers").
				Context(ctx).Do()
			if err != nil {
				return nil, err
			}
			vcfData := formatPersonToVCF(person)
			return io.NopCloser(strings.NewReader(vcfData)), nil
		}
		return nil, fmt.Errorf("invalid contact path: %s", filePath)

	default:
		return nil, fmt.Errorf("StreamDownload not implemented for %s", resourceType)
	}
}

func (p *GoogleProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return p.StreamUploadChunked(ctx, resourceType, filePath, stream, size, nil)
}

func (p *GoogleProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	switch resourceType {
	case "files":
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

	case "calendars":
		parts := strings.Split(strings.TrimPrefix(filePath, "/"), "/")
		if len(parts) == 2 {
			calendarID := parts[0]
			event, err := parseICS(stream)
			if err != nil {
				return fmt.Errorf("failed to parse ICS: %w", err)
			}
			_, err = p.calendarService.Events.Insert(calendarID, event).Context(ctx).Do()
			return err
		}
		return fmt.Errorf("invalid calendar event path: %s", filePath)

	case "contacts":
		person, err := parseVCF(stream)
		if err != nil {
			return fmt.Errorf("failed to parse VCF: %w", err)
		}
		_, err = p.peopleService.People.CreateContact(person).Context(ctx).Do()
		return err

	default:
		return fmt.Errorf("StreamUpload not implemented for %s", resourceType)
	}
}

func (p *GoogleProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if resourceType == "files" {
		res, err := p.InspectResource(ctx, resourceType, filePath)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return false, 0, nil
			}
			return false, 0, err
		}
		return true, res.Size, nil
	}
	return false, 0, nil
}

func (p *GoogleProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	switch resourceType {
	case "files":
		id, err := p.resolveDriveFileID(ctx, filePath)
		if err != nil {
			return err
		}
		return p.driveService.Files.Delete(id).Context(ctx).Do()
	default:
		return fmt.Errorf("DeleteFile not implemented for %s", resourceType)
	}
}

func (p *GoogleProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("RenameFile not implemented for %s", resourceType)
	}
	id, err := p.resolveDriveFileID(ctx, oldPath)
	if err != nil {
		return err
	}
	lastSlash := strings.LastIndex(newPath, "/")
	newName := newPath[lastSlash+1:]

	f := &drive.File{
		Name: newName,
	}
	_, err = p.driveService.Files.Update(id, f).Context(ctx).Do()
	return err
}

func (p *GoogleProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if resourceType == "files" {
		res, err := p.InspectResource(ctx, resourceType, filePath)
		if err != nil {
			return "", err
		}
		return res.Hash, nil
	}
	return "", nil
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

// Helpers for ICS/VCF conversions

func formatEventToICS(e *calendar.Event) string {
	var sb strings.Builder
	sb.WriteString("BEGIN:VCALENDAR\r\n")
	sb.WriteString("VERSION:2.0\r\n")
	sb.WriteString("PRODID:-//Clumove//NONSGML v1.0//EN\r\n")
	sb.WriteString("BEGIN:VEVENT\r\n")
	sb.WriteString(fmt.Sprintf("UID:%s\r\n", e.Id))

	formatTime := func(tStr string) string {
		if tStr == "" {
			return ""
		}
		t, err := time.Parse(time.RFC3339, tStr)
		if err != nil {
			t, err = time.Parse("2006-01-02", tStr)
			if err != nil {
				return ""
			}
			return t.Format("20060102")
		}
		return t.UTC().Format("20060102T150405Z")
	}

	dtstamp := formatTime(e.Created)
	if dtstamp == "" {
		dtstamp = time.Now().UTC().Format("20060102T150405Z")
	}
	sb.WriteString(fmt.Sprintf("DTSTAMP:%s\r\n", dtstamp))

	if e.Start != nil {
		if e.Start.DateTime != "" {
			sb.WriteString(fmt.Sprintf("DTSTART:%s\r\n", formatTime(e.Start.DateTime)))
		} else if e.Start.Date != "" {
			sb.WriteString(fmt.Sprintf("DTSTART;VALUE=DATE:%s\r\n", formatTime(e.Start.Date)))
		}
	}
	if e.End != nil {
		if e.End.DateTime != "" {
			sb.WriteString(fmt.Sprintf("DTEND:%s\r\n", formatTime(e.End.DateTime)))
		} else if e.End.Date != "" {
			sb.WriteString(fmt.Sprintf("DTEND;VALUE=DATE:%s\r\n", formatTime(e.End.Date)))
		}
	}

	if e.Summary != "" {
		sb.WriteString(fmt.Sprintf("SUMMARY:%s\r\n", escapeICSValue(e.Summary)))
	}
	if e.Description != "" {
		sb.WriteString(fmt.Sprintf("DESCRIPTION:%s\r\n", escapeICSValue(e.Description)))
	}
	if e.Location != "" {
		sb.WriteString(fmt.Sprintf("LOCATION:%s\r\n", escapeICSValue(e.Location)))
	}
	sb.WriteString("END:VEVENT\r\n")
	sb.WriteString("END:VCALENDAR\r\n")
	return sb.String()
}

func escapeICSValue(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

func unescapeICSValue(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\,", ",")
	s = strings.ReplaceAll(s, "\\;", ";")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// unfoldLines implements RFC 5545 / RFC 6350 line unfolding:
// a CRLF or LF immediately followed by a whitespace character (SPACE or TAB)
// is a fold and must be removed, joining the continuation to the previous line.
func unfoldLines(r io.Reader) []string {
	scanner := bufio.NewScanner(r)
	var lines []string
	var current strings.Builder
	for scanner.Scan() {
		raw := scanner.Text()
		// Continuation line: starts with a single space or tab
		if len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t') {
			current.WriteString(raw[1:])
		} else {
			if current.Len() > 0 {
				lines = append(lines, current.String())
			}
			current.Reset()
			current.WriteString(raw)
		}
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func parseICS(r io.Reader) (*calendar.Event, error) {
	event := &calendar.Event{}

	var inEvent bool
	for _, line := range unfoldLines(r) {
		line = strings.TrimSpace(line)
		if line == "BEGIN:VEVENT" {
			inEvent = true
			continue
		}
		if line == "END:VEVENT" {
			inEvent = false
			continue
		}
		if !inEvent {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}
		keyAttr := parts[0]
		value := parts[1]

		key := keyAttr
		if idx := strings.Index(keyAttr, ";"); idx != -1 {
			key = keyAttr[:idx]
		}

		switch key {
		case "SUMMARY":
			event.Summary = unescapeICSValue(value)
		case "DESCRIPTION":
			event.Description = unescapeICSValue(value)
		case "LOCATION":
			event.Location = unescapeICSValue(value)
		case "DTSTART":
			event.Start = parseICSTime(keyAttr, value)
		case "DTEND":
			event.End = parseICSTime(keyAttr, value)
		}
	}
	return event, nil
}

func parseICSTime(keyAttr, value string) *calendar.EventDateTime {
	edt := &calendar.EventDateTime{}
	if strings.Contains(keyAttr, "VALUE=DATE") {
		if len(value) >= 8 {
			edt.Date = fmt.Sprintf("%s-%s-%s", value[0:4], value[4:6], value[6:8])
		}
	} else {
		if strings.HasSuffix(value, "Z") && len(value) >= 15 {
			t, err := time.Parse("20060102T150405Z", value)
			if err == nil {
				edt.DateTime = t.Format(time.RFC3339)
			}
		} else if len(value) >= 15 {
			t, err := time.Parse("20060102T150405", value[:15])
			if err == nil {
				edt.DateTime = t.Format(time.RFC3339)
			}
		}
	}
	return edt
}

func formatPersonToVCF(p *people.Person) string {
	var sb strings.Builder
	sb.WriteString("BEGIN:VCARD\r\n")
	sb.WriteString("VERSION:3.0\r\n")
	sb.WriteString("FN:")
	if len(p.Names) > 0 {
		sb.WriteString(escapeICSValue(p.Names[0].DisplayName))
		sb.WriteString("\r\n")
		sb.WriteString(fmt.Sprintf("N:%s;%s;;;\r\n", escapeICSValue(p.Names[0].FamilyName), escapeICSValue(p.Names[0].GivenName)))
	} else {
		sb.WriteString("Unknown\r\n")
		sb.WriteString("N:;;;;;\r\n")
	}

	for _, email := range p.EmailAddresses {
		sb.WriteString(fmt.Sprintf("EMAIL;TYPE=INTERNET:%s\r\n", escapeICSValue(email.Value)))
	}
	for _, phone := range p.PhoneNumbers {
		sb.WriteString(fmt.Sprintf("TEL;TYPE=CELL:%s\r\n", escapeICSValue(phone.Value)))
	}
	sb.WriteString("END:VCARD\r\n")
	return sb.String()
}

func parseVCF(r io.Reader) (*people.Person, error) {
	person := &people.Person{}

	for _, line := range unfoldLines(r) {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}
		keyAttr := parts[0]
		value := parts[1]

		key := keyAttr
		if idx := strings.Index(keyAttr, ";"); idx != -1 {
			key = keyAttr[:idx]
		}

		switch key {
		case "FN":
			person.Names = append(person.Names, &people.Name{
				DisplayName: unescapeICSValue(value),
			})
		case "EMAIL":
			person.EmailAddresses = append(person.EmailAddresses, &people.EmailAddress{
				Value: value,
			})
		case "TEL":
			person.PhoneNumbers = append(person.PhoneNumbers, &people.PhoneNumber{
				Value: value,
			})
		}
	}
	return person, nil
}
