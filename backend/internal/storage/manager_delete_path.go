package storage

import (
	"context"
	"fmt"
)

// deleteManagerFileOnly adapts providers whose manager locators are sealed,
// canonical paths. It intentionally verifies the selected item is a file: a
// StorageProvider.DeleteFile method may otherwise recursively remove a folder.
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

func (p *DropboxProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *NextcloudProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *OpenCloudProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *OneDriveProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *HiDriveProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *MagentacloudProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *KoofrProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *SeafileProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *WebDAVProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
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
	return deleteManagerFileOnly(ctx, p, locator, recursive)
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
