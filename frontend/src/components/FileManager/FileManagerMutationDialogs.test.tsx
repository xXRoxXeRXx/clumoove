import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
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

  it('retries a mutation with the server-offered conflict strategy without exposing its sealed ref', () => {
    const onExecuteMutation = vi.fn();
    const sealedRef = 'sealed:provider-internal-reference';
    const props: FileManagerMutationDialogsProps = {
      t: (key) => key,
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
        onProfileChange: vi.fn(),
        onBreadcrumbSelect: vi.fn(),
        onDirectorySelect: vi.fn(),
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
