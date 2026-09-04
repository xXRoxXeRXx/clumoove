package storage

import (
	"context"
	"fmt"
)

// deleteManagerFileOnly adapts providers whose manager locators are sealed,
// canonical paths where directory deletion is unsupported or requires specialized logic.
func deleteManagerFileOnly(ctx context.Context, provider StorageProvider, locator ManagerLocator, _ bool) error {
	if locator.Path == "" || locator.Path == "/" {
		return ErrManagerUnsupported
	}
	item, err := provider.InspectResource(ctx, "files", locator.Path)
	if err != nil {
		return err
	}
	if item.IsDir {
		return fmt.Errorf("manager directory deletion: %w", ErrManagerUnsupported)
	}
	return provider.DeleteFile(ctx, "files", locator.Path)
}

// deleteManagerPathItem adapts path-backed providers that support both file
// and directory deletion. Deleting a directory without recursive=true verifies
// that the directory is empty; non-empty directories return ErrManagerDirectoryNotEmpty.
func deleteManagerPathItem(ctx context.Context, provider StorageProvider, locator ManagerLocator, recursive bool) error {
	if locator.Path == "" || locator.Path == "/" {
		return ErrManagerUnsupported
	}
	item, err := provider.InspectResource(ctx, "files", locator.Path)
	if err != nil {
		return err
	}
	if item.IsDir {
		if !recursive {
			children, err := provider.GetDirectoryListing(ctx, "files", locator.Path)
			if err != nil {
				return err
			}
			if len(children) > 0 {
				return ErrManagerDirectoryNotEmpty
			}
		}
		return provider.DeleteFile(ctx, "files", locator.Path)
	}
	return provider.DeleteFile(ctx, "files", locator.Path)
}

func (p *DropboxProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, p, locator, recursive)
}

func (p *NextcloudProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, p, locator, recursive)
}

func (p *OpenCloudProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, p, locator, recursive)
}

func (p *OneDriveProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, p, locator, recursive)
}

func (p *HiDriveProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, p, locator, recursive)
}

func (p *MagentacloudProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, p, locator, recursive)
}

func (p *KoofrProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, p, locator, recursive)
}

func (p *SeafileProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *WebDAVProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, p, locator, recursive)
}

func (p *SMBProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *S3Provider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *SFTPProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *FTPProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *LocalProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *MegaProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, p, locator, recursive)
}

var (
	_ ManagerDeleter = (*DropboxProvider)(nil)
	_ ManagerDeleter = (*NextcloudProvider)(nil)
	_ ManagerDeleter = (*OpenCloudProvider)(nil)
	_ ManagerDeleter = (*OneDriveProvider)(nil)
	_ ManagerDeleter = (*HiDriveProvider)(nil)
	_ ManagerDeleter = (*MagentacloudProvider)(nil)
	_ ManagerDeleter = (*KoofrProvider)(nil)
	_ ManagerDeleter = (*SeafileProvider)(nil)
	_ ManagerDeleter = (*WebDAVProvider)(nil)
	_ ManagerDeleter = (*SMBProvider)(nil)
	_ ManagerDeleter = (*S3Provider)(nil)
	_ ManagerDeleter = (*SFTPProvider)(nil)
	_ ManagerDeleter = (*FTPProvider)(nil)
	_ ManagerDeleter = (*LocalProvider)(nil)
	_ ManagerDeleter = (*MegaProvider)(nil)
)
