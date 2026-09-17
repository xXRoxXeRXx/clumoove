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

// deleteManagerPathItem adapts HTTP-style path-backed providers that use their
// DeleteFile primitive for both files and directories. Deleting a directory
// without recursive=true verifies that the directory is empty; non-empty
// directories return ErrManagerDirectoryNotEmpty.
func deleteManagerPathItem(ctx context.Context, provider StorageProvider, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItemWithDirectoryDeleter(ctx, provider, locator, recursive, func(ctx context.Context, path string, _ bool) error {
		return provider.DeleteFile(ctx, "files", path)
	})
}

// deleteManagerPathItemWithDirectoryDeleter applies the standard manager
// safety checks to a path-backed provider while allowing providers whose file
// deletion primitive cannot recursively remove directories to supply their
// native directory operation.
func deleteManagerPathItemWithDirectoryDeleter(ctx context.Context, provider StorageProvider, locator ManagerLocator, recursive bool, deleteDirectory func(context.Context, string, bool) error) error {
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
		return deleteDirectory(ctx, locator.Path, recursive)
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
	return deleteManagerPathItemWithDirectoryDeleter(ctx, p, locator, recursive, p.deleteManagerDirectory)
}

func (p *S3Provider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, p, locator, recursive)
}

func (p *SFTPProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItemWithDirectoryDeleter(ctx, p, locator, recursive, p.deleteManagerDirectory)
}

func (p *FTPProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItemWithDirectoryDeleter(ctx, p, locator, recursive, p.deleteManagerDirectory)
}

func (p *LocalProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItemWithDirectoryDeleter(ctx, p, locator, recursive, p.deleteManagerDirectory)
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
