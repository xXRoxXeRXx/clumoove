package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// supersededPersistencePaths names package-level backup, restore and sync
// functions that were replaced by the domain stores (BackupStore,
// RestoreStore, SyncStore) or by a fenced, transactional equivalent. They must
// not return: a second write path for the same table bypasses the generation
// fencing, ownership and schedule invariants the retained path enforces.
// Each entry maps to the path that replaced it, or to "unused" when no
// production caller existed.
var supersededPersistencePaths = map[string]string{
	// backup
	"ListPendingBackupVerifyTargetsContext": "unused",
	"CreateBackupSnapshotItemContext":       "BatchCreateBackupSnapshotItemsAndBlocksContext",
	"CreateBackupSnapshotDirectoryContext":  "BatchCreateBackupSnapshotItemsAndBlocksContext",
	"LinkBackupSnapshotItemBlocksContext":   "BatchCreateBackupSnapshotItemsAndBlocksContext",
	"ClaimNextBackupMaintenanceContext":     "ClaimNextBackupMaintenanceForWorkerContext",
	"CreateBackupJobAndScheduleContext":     "unused",
	"nullableTime":                          "unused",
	"insertBackupJob":                       "unused",
	// restore
	"ClaimNextRestoreItemContext":             "unused",
	"ClaimNextQueuedRestoreRunContext":        "ClaimNextQueuedRestoreRunForWorkerContext",
	"ClaimNextRestorePreviewContext":          "ClaimNextRestorePreviewForWorkerContext",
	"CompleteRestorePreviewContext":           "CompleteRestorePreviewWithStatsFencedContext",
	"CompleteRestorePreviewWithStatsContext":  "CompleteRestorePreviewWithStatsFencedContext",
	"FailRestorePreviewContext":               "FailRestorePreviewFencedContext",
	"CountRestorePreviewSelectionContext":     "unused",
	"RestoreConfigFingerprint":                "RestoreConfigFingerprintWithIdentity",
	"SetRestoreItemResolvedTargetPathContext": "unused",
	// sync
	"CreateSyncJob":             "unused",
	"GetSyncJobsForUser":        "GetSyncJobsForUserContext",
	"GetSyncErrors":             "TaskStore.GetSyncErrorsContext",
	"PauseSyncJob":              "SyncStore.PauseSyncJobAndDeactivateSchedules",
	"ResumeSyncJob":             "SyncStore.ResumeSyncJobAndReactivateSchedules",
	"AddSyncJobLiveBytes":       "unused",
	"UpdateSyncJobRunStats":     "unused",
	"FinalizeSyncJobPass":       "SyncStore.FinalizeSyncJobPassWithStates",
	"FinalizeEmptySyncJobPass":  "SyncStore.FinalizeEmptySyncJobPassWithStates",
	"ListActiveSyncJobs":        "unused",
	"VerifySyncJobOwnership":    "VerifySyncJobOwnershipContext",
	"IncrementSyncJobProgress":  "unused",
	"UpsertSyncState":           "unused",
	"GetSyncState":              "unused",
	"DeleteSyncState":           "unused",
	"BulkUpsertSyncStates":      "SyncStore.FinalizeSyncJobPassWithStates",
	"ListAllSyncJobs":           "unused",
	"UpdateSyncStateTargetHash": "unused",
}

func TestSupersededPersistencePathsAreRemoved(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var found []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if replacement, superseded := supersededPersistencePaths[fn.Name.Name]; superseded {
				found = append(found, name+": "+fn.Name.Name+" (replaced by: "+replacement+")")
			}
		}
	}
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("superseded persistence path still present: %s", f)
	}
}
