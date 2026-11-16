import type { FileActionDialogModels } from './dialogModels';
import { DestinationPickerDialog } from './DestinationPickerDialog';
import { BatchDeleteDialog, CreateFolderDialog, DeleteDialog, RenameDialog } from './EntryDialogs';

/** Renders whichever action dialogs are currently open. */
export function FileActionDialogs({ models }: { models: FileActionDialogModels }) {
  return (
    <>
      {models.createFolder && <CreateFolderDialog model={models.createFolder} />}
      {models.delete && <DeleteDialog model={models.delete} />}
      {models.batchDelete && <BatchDeleteDialog model={models.batchDelete} />}
      {models.rename && <RenameDialog model={models.rename} />}
      {models.destinationPicker && <DestinationPickerDialog model={models.destinationPicker} />}
    </>
  );
}
