package storage

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// magentaCloudBaseURL is the fixed WebDAV root of MagentaCLOUD. Unlike a
// standard Nextcloud instance it exposes the user's file root directly here
// (no /files/<user> segment). CalDAV/CardDAV are served by a separate service
// and are NOT reachable at this endpoint, so the provider is files-only.
const magentaCloudBaseURL = "https://magentacloud.de/remote.php/webdav"

// magentaPaths implements pathBuilder for MagentaCLOUD. The WebDAV root maps
// directly to the user's files, so paths are appended verbatim and resource
// types are ignored (only "files" is supported).
type magentaPaths struct{}

func (magentaPaths) resourceURL(baseURL, username, resourceType, endpointPath string) string {
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	escapedPath := (&url.URL{Path: cleanPath}).String()
	if baseURL == "" {
		return escapedPath
	}
	return fmt.Sprintf("%s/%s", strings.TrimSuffix(baseURL, "/"), escapedPath)
}

func (magentaPaths) uploadsURL(baseURL, username, endpointPath string) string {
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	escapedPath := (&url.URL{Path: cleanPath}).String()
	if baseURL == "" {
		return "/uploads/" + escapedPath
	}
	return fmt.Sprintf("%s/uploads/%s", strings.TrimSuffix(baseURL, "/"), escapedPath)
}

func (magentaPaths) listingPrefix(basePath, username, resourceType string) string {
	return basePath
}

// MagentacloudProvider is a thin wrapper around the shared davProvider protocol
// implementation, configured with the MagentaCLOUD path scheme. MagentaCLOUD is
// a Nextcloud instance and supports the Nextcloud /uploads chunked-upload API,
// so chunked uploads are enabled. CalDAV/CardDAV are served by a separate
// service and are not reachable here, so the provider is files-only.
type MagentacloudProvider struct {
	*davProvider
}

func NewMagentacloudProvider(username, password string) (*MagentacloudProvider, error) {
	return &MagentacloudProvider{
		davProvider: &davProvider{
			BaseURL:  magentaCloudBaseURL,
			Username: username,
			Password: password,
			HTTPClient: &http.Client{
				Transport: newDAVTransport("magentacloud.de"),
				Timeout:   0,
			},
			Threads:              4,
			UserAgent:            "MagentaCLOUD-Migration-Worker/1.0",
			pb:                   magentaPaths{},
			supportedResourceTypes: map[string]bool{"files": true},
		},
	}, nil
}
