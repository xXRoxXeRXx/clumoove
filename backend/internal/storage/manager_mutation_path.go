package storage

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
)

// PathManagerMutator is an opt-in adapter for providers with canonical,
// path-backed manager locators. It validates manager semantics before invoking
// migration primitives; callers must never construct it for native-ID locators.
type PathManagerMutator struct {
	provider StorageProvider
}

func NewPathManagerMutator(provider StorageProvider) *PathManagerMutator {
	return &PathManagerMutator{provider: provider}
}

func (m *PathManagerMutator) RenameManagerItem(ctx context.Context, locator, parent ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	return m.move(ctx, locator, parent, name, options, "renamed")
}

func (m *PathManagerMutator) MoveManagerItem(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	return m.move(ctx, locator, destination, name, options, "moved")
}

func (m *PathManagerMutator) move(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions, status string) (ManagerMutationResult, error) {
	source, target, err := m.validate(ctx, locator, destination, name)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	finalName, resultStatus, err := m.resolveDestination(ctx, destination.Path, name, source.IsDir, options)
	if err != nil || resultStatus == "skipped" {
		return ManagerMutationResult{Status: resultStatus, FinalName: finalName}, err
	}
	if resultStatus == "renamed_on_conflict" {
		status = resultStatus
	}
	if source.Path == target {
		return ManagerMutationResult{}, ErrManagerNoop
	}
	if err := m.provider.RenameFile(ctx, "files", source.Path, target); err != nil {
		return ManagerMutationResult{}, err
	}
	return ManagerMutationResult{Status: status, FinalName: finalName}, nil
}

func (m *PathManagerMutator) CopyManagerItem(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	source, _, err := m.validate(ctx, locator, destination, name)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	finalName, status, err := m.resolveDestination(ctx, destination.Path, name, source.IsDir, options)
	if err != nil || status == "skipped" {
		return ManagerMutationResult{Status: status, FinalName: finalName}, err
	}
	target := managerJoin(destination.Path, finalName)
	if source.IsDir {
		if err := m.copyDirectory(ctx, source.Path, target); err != nil {
			return ManagerMutationResult{}, fmt.Errorf("copy directory: %w", ErrManagerPartial)
		}
	} else if err := m.copyFile(ctx, source.Path, target, source.Size); err != nil {
		return ManagerMutationResult{}, err
	}
	if status == "renamed_on_conflict" {
		return ManagerMutationResult{Status: status, FinalName: finalName}, nil
	}
	return ManagerMutationResult{Status: "copied", FinalName: finalName}, nil
}

func (m *PathManagerMutator) validate(ctx context.Context, locator, destination ManagerLocator, name string) (CloudResource, string, error) {
	if locator.NativeID != "" || destination.NativeID != "" || locator.Path == "" || locator.Path == "/" || destination.Path == "" {
		return CloudResource{}, "", ErrManagerInvalidDestination
	}
	source, err := m.provider.InspectResource(ctx, "files", locator.Path)
	if err != nil {
		return CloudResource{}, "", err
	}
	if destination.Path != "/" {
		destinationItem, err := m.provider.InspectResource(ctx, "files", destination.Path)
		if err != nil {
			return CloudResource{}, "", err
		}
		if !destinationItem.IsDir {
			return CloudResource{}, "", ErrManagerInvalidDestination
		}
	}
	if source.IsDir && (destination.Path == locator.Path || strings.HasPrefix(strings.TrimSuffix(destination.Path, "/")+"/", strings.TrimSuffix(locator.Path, "/")+"/")) {
		return CloudResource{}, "", ErrManagerDirectoryCycle
	}
	return source, managerJoin(destination.Path, name), nil
}

func (m *PathManagerMutator) resolveDestination(ctx context.Context, parent, name string, sourceIsDir bool, options ManagerMutationOptions) (string, string, error) {
	items, err := m.provider.GetDirectoryListing(ctx, "files", parent)
	if err != nil {
		return "", "", err
	}
	names := make(map[string]bool, len(items))
	var existing *CloudResource
	for i := range items {
		names[items[i].Name] = true
		if items[i].Name == name {
			existing = &items[i]
		}
	}
	if existing == nil {
		return name, "", nil
	}
	switch options.ConflictStrategy {
	case ManagerConflictSkip:
		return name, "skipped", nil
	case ManagerConflictRename:
		for suffix := 1; suffix <= 100; suffix++ {
			candidate := managerRenamedName(name, suffix)
			if !names[candidate] {
				return candidate, "renamed_on_conflict", nil
			}
		}
		return "", "", ErrManagerConflict
	case ManagerConflictOverwrite:
		// Replacing a directory is provider-specific and can be recursive. Refuse
		// it here rather than accidentally deleting an existing tree.
		if existing.IsDir || sourceIsDir {
			return "", "", ErrManagerConflict
		}
		// Let the provider's native rename/upload primitive apply its overwrite
		// semantics. Pre-deleting would lose a destination if that operation fails.
		return name, "", nil
	default:
		return "", "", ErrManagerConflict
	}
}

func (m *PathManagerMutator) copyDirectory(ctx context.Context, source, target string) error {
	if err := m.provider.CreateDirectory(ctx, "files", target); err != nil {
		return err
	}
	type directory struct{ source, target string }
	queue := []directory{{source, target}}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := queue[0]
		queue = queue[1:]
		items, err := m.provider.GetDirectoryListing(ctx, "files", current.source)
		if err != nil {
			return err
		}
		for _, item := range items {
			childSource, childTarget := item.Path, managerJoin(current.target, item.Name)
			if item.IsDir {
				if err := m.provider.CreateDirectory(ctx, "files", childTarget); err != nil {
					return err
				}
				queue = append(queue, directory{childSource, childTarget})
				continue
			}
			if err := m.copyFile(ctx, childSource, childTarget, item.Size); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *PathManagerMutator) copyFile(ctx context.Context, source, target string, size int64) error {
	stream, err := m.provider.StreamDownload(ctx, "files", source)
	if err != nil {
		return err
	}
	defer stream.Close()
	exact := NewExactSizeReader(stream, size)
	if err := m.provider.StreamUpload(ctx, "files", target, exact, size); err != nil {
		return err
	}
	return exact.Verify()
}

func managerJoin(parent, name string) string {
	joined := path.Join("/"+strings.TrimPrefix(parent, "/"), name)
	if !strings.HasPrefix(joined, "/") {
		return "/" + joined
	}
	return joined
}

var (
	_ ManagerRenamer = (*PathManagerMutator)(nil)
	_ ManagerMover   = (*PathManagerMutator)(nil)
	_ ManagerCopier  = (*PathManagerMutator)(nil)
	_ io.Reader      = (*ExactSizeReader)(nil)
)
