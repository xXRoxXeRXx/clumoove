import type { FileEntry, FileMutationConflictStrategy } from '../../../api/files';
import type { ConnectionProfilePublic } from '../../../api/profiles';
import type { FileManagerBreadcrumb } from '../fileManagerTypes';

/**
 * What each action dialog shows and can do. Action hooks produce a model while their
 * dialog is open (otherwise `null`); `FileActionDialogs` renders it. Nothing outside
 * the action module reads these, so the screen never depends on dialog state.
 */

export type CreateFolderDialogModel = {
  name: string;
  busy: boolean;
  error: string;
  setName: (name: string) => void;
  submit: () => Promise<void>;
  close: () => void;
};

export type ConflictDialogModel = {
  strategies: FileMutationConflictStrategy[];
  busy: boolean;
  resolve: (strategy: FileMutationConflictStrategy) => Promise<void>;
  cancel: () => void;
};

export type RenameDialogModel = {
  name: string;
  busy: boolean;
  error: string;
  setName: (name: string) => void;
  submit: () => Promise<void>;
  close: () => void;
  conflict: ConflictDialogModel | null;
};

export type DeleteDialogModel = {
  entry: FileEntry;
  recursive: boolean;
  busy: boolean;
  error: string;
  /** False while the connection cannot delete this directory without recursion being confirmed. */
  canConfirm: boolean;
  /** The connection can only delete directories recursively. */
  onlyRecursiveSupported: boolean;
  confirm: () => Promise<void>;
  close: () => void;
};

export type BatchDeleteDialogModel = {
  itemCount: number;
  hasDirectories: boolean;
  busy: boolean;
  confirm: () => Promise<void>;
  close: () => void;
};

export type DestinationPickerDialogModel = {
  operation: 'copy' | 'move';
  /** Name of the single entry being transferred; `null` for a batch. */
  entryName: string | null;
  itemCount: number;
  profiles: ConnectionProfilePublic[];
  destinationProfileId: string;
  breadcrumbs: FileManagerBreadcrumb[];
  folders: FileEntry[];
  hasInvalidDestination: boolean;
  loading: boolean;
  busy: boolean;
  error: string;
  selectProfile: (id: string) => void;
  openFolder: (entry: FileEntry) => void;
  goToBreadcrumb: (index: number) => void;
  submit: () => Promise<void>;
  close: () => void;
};

export type FileActionDialogModels = {
  createFolder: CreateFolderDialogModel | null;
  rename: RenameDialogModel | null;
  delete: DeleteDialogModel | null;
  batchDelete: BatchDeleteDialogModel | null;
  destinationPicker: DestinationPickerDialogModel | null;
};
