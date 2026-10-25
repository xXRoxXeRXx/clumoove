import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
import {
  FileManagerMutationDialogs,
  type FileManagerMutationDialogsProps,
} from './FileManagerMutationDialogs';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

describe('FileManagerMutationDialogs', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  function makeProps(overrides: Partial<FileManagerMutationDialogsProps> = {}): FileManagerMutationDialogsProps {
    return {
      profiles: [],
      selectedEntries: [],
      renameEntry: null,
      renameName: '',
      mutationEntry: null,
      mutationOperation: null,
      batchOperation: null,
      batchConflictRefs: [],
      conflictStrategies: null,
      mutationError: '',
      mutationBusy: false,
      batchBusy: false,
      destinationPicker: {
        profileId: 'profile-1',
        breadcrumbs: [{ ref: null, name: 'Root' }],
        entries: [],
        loading: false,
        error: '',
        canCreateDirectory: false,
        creatingDirectory: false,
        newDirectoryName: '',
        onProfileChange: vi.fn(),
        onBreadcrumbSelect: vi.fn(),
        onDirectorySelect: vi.fn(),
        onNewDirectoryNameChange: vi.fn(),
        onCreateDirectory: vi.fn(),
      },
      dialogRefs: {
        rename: { current: null },
        renameCancel: { current: null },
        picker: { current: null },
        pickerCancel: { current: null },
        conflict: { current: null },
        conflictCancel: { current: null },
      },
      onRenameNameChange: vi.fn(),
      onClose: vi.fn(),
      onExecuteMutation: vi.fn(),
      onExecuteBatchMutation: vi.fn(),
      ...overrides,
    };
  }

  it('submits and closes the rename dialog through its caller callbacks', () => {
    const onExecuteMutation = vi.fn();
    const onClose = vi.fn();
    const props = makeProps({
      renameEntry: {
        ref: 'sealed:rename-me', name: 'old-name.txt', display_path: '/old-name.txt', kind: 'file', size: 1, allowed_actions: [],
      },
      renameName: 'new-name.txt',
      onExecuteMutation,
      onClose,
    });

    act(() => root.render(<FileManagerMutationDialogs {...props} />));

    const dialog = document.body.querySelector('[aria-labelledby="rename-entry-title"]');
    expect(dialog).not.toBeNull();
    const buttons = Array.from(dialog!.querySelectorAll('button'));
    act(() => buttons.find((button) => button.textContent === 'files.rename')?.click());
    act(() => buttons.find((button) => button.textContent === 'common.cancel')?.click());

    expect(onExecuteMutation).toHaveBeenCalledWith();
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('filters selected entries and delegates picker navigation to its caller', () => {
    const onProfileChange = vi.fn();
    const onBreadcrumbSelect = vi.fn();
    const onDirectorySelect = vi.fn();
    const selectedDirectory = {
      ref: 'sealed:selected', name: 'Selected', display_path: '/Selected', kind: 'directory' as const, size: 0, allowed_actions: [],
    };
    const destination = {
      ref: 'sealed:destination', name: 'Destination', display_path: '/Destination', kind: 'directory' as const, size: 0, allowed_actions: [],
    };
    const props = makeProps({
      mutationEntry: { ref: 'sealed:source', name: 'source.txt', display_path: '/source.txt', kind: 'file', size: 1, allowed_actions: [] },
      mutationOperation: 'copy',
      selectedEntries: [selectedDirectory],
      destinationPicker: {
        profileId: 'profile-1',
        breadcrumbs: [{ ref: null, name: 'Root' }, { ref: 'sealed:current', name: 'Current' }],
        entries: [selectedDirectory, destination],
        loading: false,
        error: '',
        canCreateDirectory: false,
        creatingDirectory: false,
        newDirectoryName: '',
        onProfileChange,
        onBreadcrumbSelect,
        onDirectorySelect,
        onNewDirectoryNameChange: vi.fn(),
        onCreateDirectory: vi.fn(),
      },
    });

    act(() => root.render(<FileManagerMutationDialogs {...props} />));

    expect(Array.from(document.body.querySelectorAll('button')).some((button) => button.textContent === 'Selected')).toBe(false);
    const destinationButton = Array.from(document.body.querySelectorAll('button')).find((button) => button.textContent === 'Destination');
    expect(destinationButton).toBeDefined();
    expect(document.body.textContent).not.toContain('files.conflictOverwrite');

    act(() => destinationButton?.click());
    act(() => Array.from(document.body.querySelectorAll('button')).find((button) => button.textContent === 'Root /')?.click());

    expect(onDirectorySelect).toHaveBeenCalledWith(destination);
    expect(onBreadcrumbSelect).toHaveBeenCalledWith(0);
  });

  it('shows no valid destination when the current folder is selected', () => {
    const currentDirectory = {
      ref: 'sealed:current', name: 'Current', display_path: '/Current', kind: 'directory' as const, size: 0, allowed_actions: [],
    };
    const props = makeProps({
      mutationEntry: { ref: 'sealed:source', name: 'source.txt', display_path: '/source.txt', kind: 'file', size: 1, allowed_actions: [] },
      mutationOperation: 'move',
      selectedEntries: [currentDirectory],
      destinationPicker: {
        ...makeProps().destinationPicker,
        breadcrumbs: [{ ref: null, name: 'Root' }, { ref: currentDirectory.ref, name: currentDirectory.name }],
      },
    });

    act(() => root.render(<FileManagerMutationDialogs {...props} />));

    expect(document.body.textContent).toContain('files.noValidDestination');
  });

  it('supports keyboard directory navigation and delegates safe folder creation to the picker workflow', () => {
    const onDirectorySelect = vi.fn();
    const onNewDirectoryNameChange = vi.fn();
    const onCreateDirectory = vi.fn();
    const first = { ref: 'sealed:first', name: 'First', display_path: '/First', kind: 'directory' as const, size: 0, allowed_actions: [] };
    const second = { ref: 'sealed:second', name: 'Second', display_path: '/Second', kind: 'directory' as const, size: 0, allowed_actions: [] };
    const props = makeProps({
      mutationEntry: { ref: 'sealed:source', name: 'source.txt', display_path: '/source.txt', kind: 'file', size: 1, allowed_actions: [] },
      mutationOperation: 'copy',
      destinationPicker: {
        ...makeProps().destinationPicker,
        entries: [first, second],
        canCreateDirectory: true,
        onDirectorySelect,
        onNewDirectoryNameChange,
        onCreateDirectory,
      },
    });

    act(() => root.render(<FileManagerMutationDialogs {...props} />));

    const directories = Array.from(document.body.querySelectorAll<HTMLButtonElement>('[data-destination-directory]'));
    act(() => directories[0]?.focus());
    act(() => directories[0]?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true })));
    expect(document.activeElement).toBe(directories[1]);

    const input = document.body.querySelector<HTMLInputElement>('#destination-folder-name');
    expect(input).not.toBeNull();
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, 'New destination');
      input!.dispatchEvent(new Event('input', { bubbles: true }));
    });
    expect(onNewDirectoryNameChange).toHaveBeenCalled();
    act(() => input!.form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
    expect(onCreateDirectory).toHaveBeenCalledOnce();
  });

  it('retries a mutation with the server-offered conflict strategy without exposing its sealed ref', () => {
    const onExecuteMutation = vi.fn();
    const sealedRef = 'sealed:provider-internal-reference';
    const props: FileManagerMutationDialogsProps = {
      profiles: [],
      selectedEntries: [],
      renameEntry: null,
      renameName: '',
      mutationEntry: {
        ref: sealedRef,
        name: 'report.txt',
        display_path: '/report.txt',
        kind: 'file',
        size: 42,
        allowed_actions: [],
      },
      mutationOperation: 'copy',
      batchOperation: null,
      batchConflictRefs: [],
      conflictStrategies: ['OVERWRITE'],
      mutationError: '',
      mutationBusy: false,
      batchBusy: false,
      destinationPicker: {
        profileId: 'profile-1',
        breadcrumbs: [],
        entries: [],
        loading: false,
        error: '',
        canCreateDirectory: false,
        creatingDirectory: false,
        newDirectoryName: '',
        onProfileChange: vi.fn(),
        onBreadcrumbSelect: vi.fn(),
        onDirectorySelect: vi.fn(),
        onNewDirectoryNameChange: vi.fn(),
        onCreateDirectory: vi.fn(),
      },
      dialogRefs: {
        rename: { current: null },
        renameCancel: { current: null },
        picker: { current: null },
        pickerCancel: { current: null },
        conflict: { current: null },
        conflictCancel: { current: null },
      },
      onRenameNameChange: vi.fn(),
      onClose: vi.fn(),
      onExecuteMutation,
      onExecuteBatchMutation: vi.fn(),
    };

    act(() => root.render(<FileManagerMutationDialogs {...props} />));

    expect(document.body.textContent).not.toContain(sealedRef);
    const retry = Array.from(document.body.querySelectorAll('button')).find(
      (button) => button.textContent === 'files.conflictOverwrite',
    );
    expect(retry).toBeDefined();

    act(() => retry?.click());

    expect(onExecuteMutation).toHaveBeenCalledWith('OVERWRITE');
  });
});
