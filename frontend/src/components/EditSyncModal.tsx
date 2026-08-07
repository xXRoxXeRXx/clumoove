import React, { useState, useEffect, useCallback, useId, useRef } from "react";
import {
  FolderIcon as Folder,
  FolderOpenIcon as FolderOpen,
  XMarkIcon as X,
  ArrowPathIcon as RefreshCw,
  CheckIcon as Check,
  ChevronRightIcon as ChevronRight,
  ChevronDownIcon as ChevronDown,
  ArrowLeftIcon as ArrowLeft,
  DocumentIcon as FileIcon,
} from "@heroicons/react/24/outline";
import { useTranslation } from "react-i18next";
import type { CloudFile, SyncJob } from "../types";
import { useFocusTrap } from "../hooks/useFocusTrap";
import { useApiError } from "../utils/apiError";
import { apiFetch } from "../utils/apiClient";
import { SelectedPathsViewer } from "./SelectedPathsViewer";
import { SyncOptionsForm } from "./SyncOptionsForm";
import { Button } from "./Button";

interface EditSyncModalProps {
  job: SyncJob;
  apiUrl: string;
  token: string;
  onClose: () => void;
  onSuccess: () => void;
}

export const EditSyncModal: React.FC<EditSyncModalProps> = ({
  job,
  apiUrl,
  token,
  onClose,
  onSuccess,
}) => {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  useFocusTrap(dialogRef, closeButtonRef, onClose);

  const titleId = useId();

  // Selected paths state
  const [selectedPaths, setSelectedPaths] = useState<Record<string, boolean>>(() => {
    const initial: Record<string, boolean> = {};
    if (Array.isArray(job.selected_paths)) {
      job.selected_paths.forEach((p) => {
        if (p) initial[p] = true;
      });
    }
    return initial;
  });

  // Sync options state
  const [targetDir, setTargetDir] = useState<string>(job.target_dir || "/");
  const [conflictStrategy, setConflictStrategy] = useState<string>(job.conflict_strategy || "SKIP");
  const [direction, setDirection] = useState<"one_way" | "two_way">(job.direction || "one_way");
  const [deletePropagation, setDeletePropagation] = useState<boolean>(job.delete_propagation || false);
  const [intervalMinutes, setIntervalMinutes] = useState<number>(job.interval_minutes || 15);
  const [bandwidthLimit, setBandwidthLimit] = useState<number>(job.bandwidth_limit_mbps || 0);

  // Modal UI state
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Source tree browsing state
  const [folderContents, setFolderContents] = useState<Record<string, CloudFile[]>>({});
  const [expandedFolders, setExpandedFolders] = useState<Record<string, boolean>>({ "/": true });
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({});

  // Target directory browser state
  const [isBrowsingTarget, setIsBrowsingTarget] = useState(false);
  const [targetCurrentFolder, setTargetCurrentFolder] = useState("/");
  const [targetFolderContents, setTargetFolderContents] = useState<Record<string, CloudFile[]>>({});
  const [targetLoadingPaths, setTargetLoadingPaths] = useState<Record<string, boolean>>({});

  const pathsToMigrate = Object.keys(selectedPaths).filter((p) => selectedPaths[p]);

const sortEntries = (entries: CloudFile[]): CloudFile[] => {
  return [...entries].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
    return a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
  });
};

  // Fetch directory contents for source
  const fetchSourceDirectory = useCallback(
    async (folderPath: string) => {
      setLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
      try {
        const res = await apiFetch(
          `${apiUrl}/api/sync/${job.id}/browse?role=source&path=${encodeURIComponent(folderPath)}`,
          { headers: { Authorization: `Bearer ${token}` } }
        );
        if (!res.ok) {
          const body = await res.json().catch(() => ({}));
          throw new Error(body?.error_code ? translateApiError(body.error_code) : t("fileBrowser.loadFailed"));
        }
        const data = await res.json();
        if (!data.success) {
          throw new Error(data.error_code ? translateApiError(data.error_code) : t("fileBrowser.loadFailed"));
        }
        const items = sortEntries(data.items || data.files || []);
        setFolderContents((prev) => ({ ...prev, [folderPath]: items }));
      } catch (err) {
        console.error("Error loading source directory:", err);
        setError(err instanceof Error ? err.message : t("fileBrowser.loadFailed"));
      } finally {
        setLoadingPaths((prev) => ({ ...prev, [folderPath]: false }));
      }
    },
    [apiUrl, job.id, token, t, translateApiError]
  );

  // Fetch directory contents for target
  const fetchTargetDirectory = useCallback(
    async (folderPath: string) => {
      setTargetLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
      try {
        const res = await apiFetch(
          `${apiUrl}/api/sync/${job.id}/browse?role=target&path=${encodeURIComponent(folderPath)}`,
          { headers: { Authorization: `Bearer ${token}` } }
        );
        if (!res.ok) {
          const body = await res.json().catch(() => ({}));
          throw new Error(body?.error_code ? translateApiError(body.error_code) : t("fileBrowser.loadFailed"));
        }
        const data = await res.json();
        if (!data.success) {
          throw new Error(data.error_code ? translateApiError(data.error_code) : t("fileBrowser.loadFailed"));
        }
        const items = sortEntries(data.items || data.files || []);
        setTargetFolderContents((prev) => ({ ...prev, [folderPath]: items }));
      } catch (err) {
        console.error("Error loading target directory:", err);
        setError(err instanceof Error ? err.message : t("fileBrowser.loadFailed"));
      } finally {
        setTargetLoadingPaths((prev) => ({ ...prev, [folderPath]: false }));
      }
    },
    [apiUrl, job.id, token, t, translateApiError]
  );

  // Load root folder on mount
  useEffect(() => {
    const timer = setTimeout(() => {
      void fetchSourceDirectory("/");
    }, 0);
    return () => clearTimeout(timer);
  }, [fetchSourceDirectory]);

  // Memoized target browser opener (Point 3)
  const openTargetBrowser = useCallback(() => {
    setIsBrowsingTarget(true);
    if (!targetFolderContents[targetCurrentFolder] && !targetLoadingPaths[targetCurrentFolder]) {
      void fetchTargetDirectory(targetCurrentFolder);
    }
  }, [fetchTargetDirectory, targetCurrentFolder, targetFolderContents, targetLoadingPaths]);

  // Toggle path selection
  const togglePathSelection = (path: string) => {
    setSelectedPaths((prev) => {
      const next = { ...prev };
      if (next[path]) {
        delete next[path];
      } else {
        next[path] = true;
      }
      return next;
    });
  };

  const deselectAll = () => {
    setSelectedPaths({});
  };

  // Toggle folder expansion in source tree
  const toggleFolderExpand = (folderPath: string) => {
    setExpandedFolders((prev) => {
      const isExpanding = !prev[folderPath];
      if (isExpanding && !folderContents[folderPath] && !loadingPaths[folderPath]) {
        void fetchSourceDirectory(folderPath);
      }
      return { ...prev, [folderPath]: isExpanding };
    });
  };

  // Handle Save
  const handleSave = async () => {
    if (pathsToMigrate.length === 0) {
      setError(t("fileBrowser.errors.selectOne"));
      return;
    }
    setSaving(true);
    setError(null);
    try {
      // 1. Update Scope (atomic: selected_paths, target_dir, conflict_strategy, direction, delete_propagation)
      const scopeRes = await apiFetch(`${apiUrl}/api/sync/${job.id}/scope`, {
        method: "PUT",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify({
          selected_paths: pathsToMigrate,
          target_dir: targetDir,
          conflict_strategy: conflictStrategy,
          direction: direction,
          delete_propagation: deletePropagation,
        }),
      });
      if (!scopeRes.ok) {
        const body = await scopeRes.json().catch(() => ({}));
        throw new Error(body?.error_code ? translateApiError(body.error_code) : t("sync.createFailed"));
      }

      // 2. Update Schedule if interval changed
      if (intervalMinutes !== job.interval_minutes) {
        const schedRes = await apiFetch(`${apiUrl}/api/sync/${job.id}/schedule`, {
          method: "PUT",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
          body: JSON.stringify({ interval_minutes: intervalMinutes }),
        });
        if (!schedRes.ok) {
          const body = await schedRes.json().catch(() => ({}));
          throw new Error(body?.error_code ? translateApiError(body.error_code) : t("sync.createFailed"));
        }
      }

      // 3. Update Bandwidth if changed
      if (bandwidthLimit !== job.bandwidth_limit_mbps) {
        const bwRes = await apiFetch(`${apiUrl}/api/sync/${job.id}/bandwidth`, {
          method: "PUT",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
          body: JSON.stringify({ limit_mbps: bandwidthLimit }),
        });
        if (!bwRes.ok) {
          const body = await bwRes.json().catch(() => ({}));
          throw new Error(body?.error_code ? translateApiError(body.error_code) : t("sync.createFailed"));
        }
      }

      onSuccess();
    } catch (err) {
      console.error("Save error:", err);
      setError(err instanceof Error ? err.message : t("sync.createFailed"));
    } finally {
      setSaving(false);
    }
  };

  // Helper for source tree node rendering
  const renderSourceTreeNode = (file: CloudFile, depth = 0) => {
    const isSelected = !!selectedPaths[file.path];
    const isFolder = file.is_dir;
    const isExpanded = !!expandedFolders[file.path];
    const isLoading = !!loadingPaths[file.path];
    const children = folderContents[file.path] || [];

    return (
      <div key={file.path} className="space-y-0.5">
        <div
          className={`flex items-center justify-between p-1.5 rounded-lg transition-colors text-xs font-mono ${
            isSelected ? "bg-[var(--color-bg-tertiary)]/60" : "hover:bg-[var(--color-bg-secondary)]"
          }`}
          style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
        >
          <div className="flex items-center gap-2 truncate flex-1">
            {isFolder ? (
              <button
                type="button"
                onClick={() => toggleFolderExpand(file.path)}
                className="p-0.5 text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors rounded"
                aria-label={isExpanded ? t("common.collapse") : t("common.expand")}
              >
                {isLoading ? (
                  <RefreshCw className="w-3.5 h-3.5 animate-spin" />
                ) : isExpanded ? (
                  <ChevronDown className="w-3.5 h-3.5" />
                ) : (
                  <ChevronRight className="w-3.5 h-3.5" />
                )}
              </button>
            ) : (
              <span className="w-3.5 h-3.5 inline-block" />
            )}

            <label className="flex items-center gap-2 cursor-pointer flex-1 truncate">
              <input
                type="checkbox"
                checked={isSelected}
                onChange={() => togglePathSelection(file.path)}
                className="rounded accent-[var(--color-text-primary)] cursor-pointer"
              />
              {isFolder ? (
                <Folder className="w-4 h-4 text-[var(--color-text-secondary)] shrink-0" />
              ) : (
                <FileIcon className="w-4 h-4 text-[var(--color-text-muted)] shrink-0" />
              )}
              <span className="truncate">{file.name}</span>
            </label>
          </div>
        </div>

        {/* Render child nodes if expanded */}
        {isFolder && isExpanded && (
          <div className="space-y-0.5">
            {children.length === 0 && !isLoading ? (
              <div
                className="text-[11px] text-[var(--color-text-muted)] italic font-mono p-1"
                style={{ paddingLeft: `${(depth + 1) * 1.25 + 0.5}rem` }}
              >
                {t("fileBrowser.noFiles")}
              </div>
            ) : (
              children.map((child) => renderSourceTreeNode(child, depth + 1))
            )}
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="fixed inset-0 bg-[var(--color-overlay)] z-[var(--layer-dialog)] flex items-center justify-center p-4 overflow-y-auto">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="ui-card w-full max-w-5xl p-6 bg-[var(--color-bg-primary)] border-[var(--color-border)] shadow-xl relative max-h-[90vh] overflow-y-auto text-left space-y-6"
      >
        {/* Header */}
        <div className="flex items-center justify-between border-b border-[var(--color-border)]/50 pb-4">
          <h2 id={titleId} className="font-display text-xl font-semibold leading-none text-[var(--color-text-primary)]">
            {t("sync.editModalTitle")}
          </h2>
          <button
            ref={closeButtonRef}
            type="button"
            onClick={onClose}
            aria-label={t("common.close")}
            className="p-2 text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors rounded-lg"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Source & Target Cards Grid */}
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          {/* Source Card */}
          <div className="ui-card space-y-4 p-5">
            <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-2.5">
              <div className="flex items-center gap-2">
                <Folder className="w-4 h-4 text-[var(--color-text-secondary)] shrink-0" />
                <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">
                  {t("migrations.source")}
                </h3>
              </div>
              <span className="ui-badge ui-badge-muted text-[10px] font-mono font-bold px-2.5 py-0.5">
                {t("fileBrowser.itemCount", { count: pathsToMigrate.length })}
              </span>
            </div>

            <div className="space-y-2">
              <div className="font-extrabold text-sm text-[var(--color-text-primary)] capitalize">
                {job.source_provider}
              </div>
              <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">
                {job.source_url || t("migrations.oauth")}
              </div>
              <SelectedPathsViewer paths={pathsToMigrate} maxVisible={3} />
            </div>
          </div>

          {/* Target Card */}
          <div className="ui-card space-y-4 p-5">
            <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-2.5">
              <div className="flex items-center gap-2">
                <Folder className="w-4 h-4 text-[var(--color-text-secondary)] shrink-0" />
                <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">
                  {t("migrations.target")}
                </h3>
              </div>
              {job.target_provider !== "immich" && (
                <button
                  type="button"
                  onClick={openTargetBrowser}
                  className="ui-link text-[10px] font-mono font-bold hover:text-[var(--color-text-secondary)] transition-colors cursor-pointer underline flex items-center gap-1"
                >
                  <FolderOpen className="w-3.5 h-3.5" />
                  <span>{t("fileBrowser.selectFolder")}</span>
                </button>
              )}
            </div>

            <div className="space-y-2">
              <div className="font-extrabold text-sm text-[var(--color-text-primary)] capitalize">
                {job.target_provider}
              </div>
              <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">
                {job.target_url || t("migrations.oauth")}
              </div>
              <div className="flex flex-wrap gap-1.5 pt-1">
                <span className="ui-card inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-mono text-[var(--color-text-primary)] shadow-2xs font-bold">
                  <Folder className="w-3.5 h-3.5 text-[var(--color-text-secondary)] shrink-0" />
                  <span>{targetDir || "/"}</span>
                </span>
              </div>
            </div>
          </div>
        </div>

        {/* Sync Options Form */}
        <SyncOptionsForm
          effectiveJobType="sync"
          direction={direction}
          setDirection={setDirection}
          deletePropagation={deletePropagation}
          setDeletePropagation={setDeletePropagation}
          conflictStrategy={conflictStrategy}
          setConflictStrategy={setConflictStrategy}
          intervalMinutes={intervalMinutes}
          setIntervalMinutes={setIntervalMinutes}
          bandwidthLimit={bandwidthLimit}
          setBandwidthLimit={setBandwidthLimit}
          isImmichTarget={job.target_provider === "immich"}
          targetDir={targetDir}
          openTargetBrowser={openTargetBrowser}
          error={error}
          hideTargetDirConfig
        />

        {/* Target Folder Browser Sub-modal (Points 7 & 8: multi-level & keyboard accessible) */}
        {isBrowsingTarget && (
          <div className="fixed inset-0 bg-[var(--color-overlay)] z-[calc(var(--layer-dialog)+10)] flex items-center justify-center p-4">
            <div className="ui-card w-full max-w-2xl p-5 bg-[var(--color-bg-primary)] border-[var(--color-border)] shadow-xl relative max-h-[80vh] flex flex-col">
              <div className="flex items-center justify-between pb-3 border-b border-[var(--color-border)] mb-3">
                <div className="flex items-center gap-2">
                  {targetCurrentFolder !== "/" && (
                    <button
                      type="button"
                      onClick={() => {
                        const parts = targetCurrentFolder.split("/").filter(Boolean);
                        parts.pop();
                        const parent = "/" + parts.join("/");
                        setTargetCurrentFolder(parent);
                        if (!targetFolderContents[parent]) {
                          void fetchTargetDirectory(parent);
                        }
                      }}
                      className="ui-button-secondary p-1 text-xs"
                      title={t("common.back")}
                    >
                      <ArrowLeft className="w-4 h-4" />
                    </button>
                  )}
                  <h3 className="font-bold text-sm text-[var(--color-text-primary)] font-mono truncate">
                    {t("fileBrowser.selectTargetFolder")}: <span className="text-[var(--color-text-secondary)]">{targetCurrentFolder}</span>
                  </h3>
                </div>
                <button
                  type="button"
                  onClick={() => setIsBrowsingTarget(false)}
                  className="p-1.5 text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]"
                >
                  <X className="w-4 h-4" />
                </button>
              </div>

              <div className="flex-1 overflow-y-auto space-y-1 font-mono text-xs p-2">
                {targetLoadingPaths[targetCurrentFolder] ? (
                  <div className="p-4 text-center text-[var(--color-text-muted)] flex items-center justify-center gap-2">
                    <RefreshCw className="w-4 h-4 animate-spin" />
                    <span>{t("common.loading")}</span>
                  </div>
                ) : (
                  <>
                    {/* Current folder selection button */}
                    <button
                      type="button"
                      onClick={() => setTargetDir(targetCurrentFolder)}
                      className={`w-full text-left p-2.5 rounded-lg cursor-pointer flex items-center justify-between transition-colors ${
                        targetDir === targetCurrentFolder
                          ? "bg-[var(--color-bg-tertiary)] font-bold text-[var(--color-text-primary)] border border-[var(--color-border)]"
                          : "bg-[var(--color-bg-secondary)] hover:bg-[var(--color-bg-tertiary)]/50"
                      }`}
                    >
                      <div className="flex items-center gap-2 truncate">
                        <Folder className="w-4 h-4 text-[var(--color-text-secondary)] shrink-0" />
                        <span className="truncate">{targetCurrentFolder} ({t("fileBrowser.useCurrentFolder")})</span>
                      </div>
                      {targetDir === targetCurrentFolder && <Check className="w-4 h-4 text-[var(--color-text-primary)]" />}
                    </button>

                    {/* Subfolders list */}
                    {(targetFolderContents[targetCurrentFolder] || [])
                      .filter((f) => f.is_dir)
                      .map((folder) => (
                        <button
                          key={folder.path}
                          type="button"
                          onClick={() => {
                            setTargetCurrentFolder(folder.path);
                            if (!targetFolderContents[folder.path]) {
                              void fetchTargetDirectory(folder.path);
                            }
                          }}
                          className="w-full text-left p-2 rounded-lg cursor-pointer flex items-center justify-between hover:bg-[var(--color-bg-secondary)] transition-colors"
                        >
                          <div className="flex items-center gap-2 truncate">
                            <Folder className="w-4 h-4 text-[var(--color-text-secondary)] shrink-0" />
                            <span className="truncate">{folder.name}</span>
                          </div>
                          <ChevronRight className="w-3.5 h-3.5 text-[var(--color-text-muted)]" />
                        </button>
                      ))}
                  </>
                )}
              </div>

              <div className="pt-4 border-t border-[var(--color-border)] flex justify-end gap-2">
                <Button type="button" variant="secondary" onClick={() => setIsBrowsingTarget(false)}>
                  {t("common.done")}
                </Button>
              </div>
            </div>
          </div>
        )}

        {/* Source File Tree Card (Points 4 & 8: drill-down & keyboard accessibility) */}
        <div className="ui-card flex flex-col p-5 space-y-3">
          <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-3">
            <h3 className="font-mono text-xs font-bold uppercase tracking-wider text-[var(--color-text-primary)]">
              {t("fileBrowser.files")} ({pathsToMigrate.length})
            </h3>
            <button
              type="button"
              onClick={deselectAll}
              className="ui-button-secondary py-1 px-2.5 text-[10px] font-mono font-bold uppercase"
            >
              {t("common.deselectAll")}
            </button>
          </div>

          <div className="max-h-72 overflow-y-auto space-y-1 font-mono text-xs p-1">
            {loadingPaths["/"] ? (
              <div className="p-4 text-center text-[var(--color-text-muted)] flex items-center justify-center gap-2">
                <RefreshCw className="w-4 h-4 animate-spin" />
                <span>{t("common.loading")}</span>
              </div>
            ) : (folderContents["/"] || []).length === 0 ? (
              <div className="p-4 text-center text-[var(--color-text-muted)]">
                {t("fileBrowser.noFiles")}
              </div>
            ) : (
              (folderContents["/"] || []).map((file) => renderSourceTreeNode(file, 0))
            )}
          </div>
        </div>

        {/* Footer Actions */}
        <div className="flex items-center justify-end gap-3 pt-4 border-t border-[var(--color-border)]">
          <Button type="button" variant="secondary" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button type="button" variant="primary" onClick={handleSave} disabled={saving}>
            {saving ? (
              <>
                <RefreshCw className="w-4 h-4 animate-spin" />
                <span>{t("common.saving")}</span>
              </>
            ) : (
              <>
                <Check className="w-4 h-4" />
                <span>{t("sync.saveChanges")}</span>
              </>
            )}
          </Button>
        </div>
      </div>
    </div>
  );
};
