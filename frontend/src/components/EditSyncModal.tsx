import React, { useState, useEffect, useCallback, useId, useRef } from "react";
import {
  ArrowPathIcon as RefreshCw,
  CheckIcon as Check,
  ChevronDownIcon as ChevronDown,
  ChevronRightIcon as ChevronRight,
  ExclamationTriangleIcon as AlertTriangle,
  FolderIcon as Folder,
  FolderOpenIcon as FolderOpen,
  FolderPlusIcon as FolderPlus,
  XMarkIcon as X,
} from "@heroicons/react/24/outline";
import { useTranslation } from "react-i18next";
import type { CloudFile, SyncJob } from "../types";
import { useFocusTrap } from "../hooks/useFocusTrap";
import { useApiError } from "../utils/apiError";
import { useFormat } from "../utils/format";
import { apiFetch } from "../utils/apiClient";
import { logger } from "../utils/logger";
import { FileIcon } from "./FileIcon";
import { SelectedPathsViewer } from "./SelectedPathsViewer";
import { SyncOptionsForm } from "./SyncOptionsForm";
import { Button } from "./Button";

interface EditSyncModalProps {
  job: SyncJob;
  apiUrl: string;
  token: string;
  onClose: () => void;
  onSuccess: (updates: Partial<Pick<SyncJob, "selected_paths" | "target_dir" | "conflict_strategy" | "direction" | "delete_propagation" | "interval_minutes" | "bandwidth_limit_mbps">>, partial: boolean) => void;
}

const sortEntries = (entries: CloudFile[]): CloudFile[] => {
  return [...entries].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
    return a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
  });
};

export const EditSyncModal: React.FC<EditSyncModalProps> = ({
  job,
  apiUrl,
  token,
  onClose,
  onSuccess,
}) => {
  const { t } = useTranslation();
  const { formatBytes } = useFormat();
  const translateApiError = useApiError();
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const targetDialogRef = useRef<HTMLDivElement>(null);
  const targetCloseButtonRef = useRef<HTMLButtonElement>(null);
  const targetDialogTitleId = useId();

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
  const [intervalMinutes, setIntervalMinutes] = useState<number>(job.interval_minutes ?? 15);
  const [bandwidthLimit, setBandwidthLimit] = useState<number>(job.bandwidth_limit_mbps ?? 0);

  // Modal UI state
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Source tree browsing state
  const [directoryContents, setDirectoryContents] = useState<Record<string, CloudFile[]>>({});
  const [expandedPaths, setExpandedPaths] = useState<Record<string, boolean>>({ "/": true });
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({});
  const directoryContentsRef = useRef(directoryContents);
  const loadingPathsRef = useRef(loadingPaths);

  useEffect(() => {
    directoryContentsRef.current = directoryContents;
    loadingPathsRef.current = loadingPaths;
  }, [directoryContents, loadingPaths]);

  // Target directory browser state
  const [isTargetBrowserOpen, setIsTargetBrowserOpen] = useState(false);
  const [targetExpandedPaths, setTargetExpandedPaths] = useState<Record<string, boolean>>({ "/": true });
  const [targetDirectoryContents, setTargetDirectoryContents] = useState<Record<string, CloudFile[]>>({});
  const [targetLoadingPaths, setTargetLoadingPaths] = useState<Record<string, boolean>>({});
  const [targetError, setTargetError] = useState<string | null>(null);
  const [isCreatingFolder, setIsCreatingFolder] = useState(false);
  const [creatingFolder, setCreatingFolder] = useState(false);
  const [newFolderName, setNewFolderName] = useState("");

  const pathsToMigrate = Object.keys(selectedPaths).filter((p) => selectedPaths[p]);

  // Fetch directory contents for source
  const fetchSourceDirectory = useCallback(
    async (folderPath: string, force?: boolean) => {
      if (loadingPathsRef.current[folderPath]) return;
      if (!force && directoryContentsRef.current[folderPath]) return;

      setLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
      try {
        const res = await apiFetch(
          `${apiUrl}/api/sync/${job.id}/browse?role=source&path=${encodeURIComponent(folderPath)}`,
          { headers: { Authorization: `Bearer ${token}` } }
        );
        if (!res.ok) {
          const body = await res.json().catch(() => ({}));
          throw new Error(body?.error_code ? translateApiError(body.error_code) : t("fileBrowser.errors.loadDir"));
        }
        const data = await res.json();
        if (!data.success) {
          throw new Error(data.error_code ? translateApiError(data.error_code) : t("fileBrowser.errors.loadDir"));
        }
        const items = sortEntries(data.items || data.files || []);
        setDirectoryContents((prev) => ({ ...prev, [folderPath]: items }));
      } catch (err) {
        logger.error("Error loading source directory", err);
        setError(err instanceof Error ? err.message : t("fileBrowser.errors.loadDir"));
      } finally {
        setLoadingPaths((prev) => ({ ...prev, [folderPath]: false }));
      }
    },
    [apiUrl, job.id, token, t, translateApiError]
  );

  // Fetch directory contents for target
  const fetchTargetChildren = useCallback(
    async (folderPath: string) => {
      setTargetLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
      setTargetError(null);
      try {
        const res = await apiFetch(
          `${apiUrl}/api/sync/${job.id}/browse?role=target&path=${encodeURIComponent(folderPath)}`,
          { headers: { Authorization: `Bearer ${token}` } }
        );
        if (!res.ok) {
          const body = await res.json().catch(() => ({}));
          throw new Error(body?.error_code ? translateApiError(body.error_code) : t("fileBrowser.errors.loadTarget"));
        }
        const data = await res.json();
        if (!data.success) {
          throw new Error(data.error_code ? translateApiError(data.error_code) : t("fileBrowser.errors.loadTarget"));
        }
        const items = sortEntries(data.items || data.files || []);
        setTargetDirectoryContents((prev) => ({ ...prev, [folderPath]: items }));
      } catch (err) {
        logger.error("Error loading target directory", err);
        setTargetError(err instanceof Error ? err.message : t("fileBrowser.errors.loadTarget"));
      } finally {
        setTargetLoadingPaths((prev) => ({ ...prev, [folderPath]: false }));
      }
    },
    [apiUrl, job.id, token, t, translateApiError]
  );

  // Load root folder on mount
  useEffect(() => {
    const timer = setTimeout(() => {
      void fetchSourceDirectory("/", true);
    }, 0);
    return () => clearTimeout(timer);
  }, [fetchSourceDirectory]);

  // Target browser opener
  const openTargetBrowser = useCallback(() => {
    setIsTargetBrowserOpen(true);
    setTargetExpandedPaths((prev) => ({ ...prev, "/": true }));
    void fetchTargetChildren("/");
  }, [fetchTargetChildren]);

  const closeTargetBrowser = useCallback(() => {
    setIsTargetBrowserOpen(false);
    setIsCreatingFolder(false);
    setCreatingFolder(false);
    setNewFolderName("");
    setTargetError(null);
  }, []);

  useFocusTrap(dialogRef, closeButtonRef, onClose, !isTargetBrowserOpen);
  useFocusTrap(targetDialogRef, targetCloseButtonRef, closeTargetBrowser, isTargetBrowserOpen);

  const handleCreateTargetFolder = async (parentPath: string) => {
    if (!newFolderName.trim() || creatingFolder) return;
    setCreatingFolder(true);
    try {
      const res = await apiFetch(`${apiUrl}/api/sync/${job.id}/mkdir`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify({ role: "target", path: parentPath, name: newFolderName.trim() }),
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error(body?.error_code ? translateApiError(body.error_code) : t("fileBrowser.mkdirFailed"));
      }
      setIsCreatingFolder(false);
      setNewFolderName("");
      await fetchTargetChildren(parentPath);
    } catch (err) {
      logger.error("Failed to create target directory", err);
      setTargetError(err instanceof Error ? err.message : t("fileBrowser.mkdirFailed"));
    } finally {
      setCreatingFolder(false);
    }
  };

  // Toggle path selection
  const toggleSelect = (filePath: string) => {
    setSelectedPaths((prev) => ({ ...prev, [filePath]: !prev[filePath] }));
  };

  const deselectAll = () => {
    setSelectedPaths({});
  };

  // Toggle folder expansion in source tree
  const toggleExpand = (folderPath: string) => {
    const isExpanded = !!expandedPaths[folderPath];
    setExpandedPaths((prev) => ({ ...prev, [folderPath]: !isExpanded }));
    if (!isExpanded) {
      void fetchSourceDirectory(folderPath);
    }
  };

  const refreshFiles = async () => {
    setDirectoryContents({});
    setExpandedPaths({ "/": true });
    await fetchSourceDirectory("/", true);
  };

  // Handle Save
  const handleSave = async () => {
    if (pathsToMigrate.length === 0) {
      setError(t("fileBrowser.errors.selectOne"));
      return;
    }
    setSaving(true);
    setError(null);
    const committedUpdates: Partial<Pick<SyncJob, "selected_paths" | "target_dir" | "conflict_strategy" | "direction" | "delete_propagation" | "interval_minutes" | "bandwidth_limit_mbps">> = {};
    try {
      // Scope changes are committed independently by the API. Keep the parent in
      // sync if a later optional update fails so it never shows stale settings.
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
      Object.assign(committedUpdates, {
        selected_paths: pathsToMigrate,
        target_dir: targetDir,
        conflict_strategy: conflictStrategy as SyncJob["conflict_strategy"],
        direction,
        delete_propagation: deletePropagation,
      });

      if (intervalMinutes !== (job.interval_minutes ?? 15)) {
        const schedRes = await apiFetch(`${apiUrl}/api/sync/${job.id}/schedule`, {
          method: "PUT",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
          body: JSON.stringify({ interval_minutes: intervalMinutes }),
        });
        if (!schedRes.ok) {
          const body = await schedRes.json().catch(() => ({}));
          throw new Error(body?.error_code ? translateApiError(body.error_code) : t("sync.createFailed"));
        }
        committedUpdates.interval_minutes = intervalMinutes;
      }

      if (bandwidthLimit !== (job.bandwidth_limit_mbps ?? 0)) {
        const bwRes = await apiFetch(`${apiUrl}/api/sync/${job.id}/bandwidth`, {
          method: "PUT",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
          body: JSON.stringify({ limit_mbps: bandwidthLimit }),
        });
        if (!bwRes.ok) {
          const body = await bwRes.json().catch(() => ({}));
          throw new Error(body?.error_code ? translateApiError(body.error_code) : t("sync.createFailed"));
        }
        committedUpdates.bandwidth_limit_mbps = bandwidthLimit;
      }

      onSuccess(committedUpdates, false);
    } catch (err) {
      logger.error("Save error", err);
      if (Object.keys(committedUpdates).length > 0) {
        onSuccess(committedUpdates, true);
        return;
      }
      setError(err instanceof Error ? err.message : t("sync.createFailed"));
    } finally {
      setSaving(false);
    }
  };

  // Render tree node 1:1 matching FileBrowser.tsx
  const renderNode = (file: CloudFile, depth: number = 0) => {
    const isExpanded = !!expandedPaths[file.path];
    const isSelected = !!selectedPaths[file.path];
    const isLoading = !!loadingPaths[file.path];
    const children = directoryContents[file.path] || [];

    return (
      <div key={file.path} className="select-none font-sans text-xs">
        {/* Row */}
        <div
          className={`flex items-center gap-3 py-3.5 px-4 border-b border-[var(--color-border-light)] hover:bg-[var(--color-bg-tertiary)] transition-colors duration-150 ${
            isSelected ? "bg-[var(--color-bg-tertiary)] font-semibold" : ""
          }`}
          style={{ paddingLeft: `${depth * 20 + 16}px` }}
        >
          {/* Collapse/Expand Arrow */}
          {file.is_dir ? (
            <button
              type="button"
              className="w-5 h-5 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors"
              onClick={() => toggleExpand(file.path)}
              aria-label={
                isExpanded
                  ? t("common.collapse", { name: file.name })
                  : t("common.expand", { name: file.name })
              }
            >
              {isLoading ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin text-[var(--color-text-primary)]" />
              ) : isExpanded ? (
                <ChevronDown className="w-4.5 h-4.5 stroke-[2]" />
              ) : (
                <ChevronRight className="w-4.5 h-4.5 stroke-[2]" />
              )}
            </button>
          ) : (
            <span className="w-5" />
          )}

          {/* Rounded-md Checkbox */}
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              toggleSelect(file.path);
            }}
            className="flex items-center justify-center cursor-pointer"
            aria-label={`${t("common.select")} ${file.name}`}
          >
            <div
              className={`w-4.5 h-4.5 border rounded flex items-center justify-center transition-all duration-200 ${
                isSelected
                  ? "bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)] border-transparent"
                  : "bg-[var(--color-bg-secondary)] border-[var(--color-border)] hover:border-[var(--color-border)]"
              }`}
            >
              {isSelected && (
                <Check className="w-3 h-3 text-[var(--color-text-inverse)] stroke-[3.5]" />
              )}
            </div>
          </button>

          {/* Outline-style Icon */}
          <span className="shrink-0">
            {file.is_dir ? (
              isExpanded ? (
                <FolderOpen className="w-5 h-5 text-[var(--color-text-secondary)]" />
              ) : (
                <Folder className="w-5 h-5 text-[var(--color-text-secondary)]" />
              )
            ) : (
              <FileIcon name={file.name} className="w-5 h-5 shrink-0" />
            )}
          </span>

          {/* Name & Size */}
          <span
            className={`text-[12px] truncate flex-grow leading-normal py-0.5 ${
              isSelected ? "text-[var(--color-text-primary)] font-bold" : "text-[var(--color-text-primary)]"
            }`}
          >
            {file.name}
          </span>

          {!file.is_dir && (
            <span className="ui-badge ui-badge-muted text-[10px] font-bold px-2 py-0.5 rounded">
              {formatBytes(file.size)}
            </span>
          )}
        </div>

        {/* Children (Recursion) */}
        {file.is_dir && isExpanded && children.length > 0 && (
          <div className="relative">
            {/* Visual connector left track */}
            <div className="absolute left-6.5 top-0 bottom-4.5 border-l border-[var(--color-border)]"></div>
            {children.map((child) => renderNode(child, depth + 1))}
          </div>
        )}

        {file.is_dir && isExpanded && children.length === 0 && !isLoading && (
          <div className="text-[10px] text-[var(--color-text-muted)] italic py-2.5 pl-14 text-left">
            {t("fileBrowser.emptyDir")}
          </div>
        )}
      </div>
    );
  };

  const toggleTargetExpand = useCallback(
    (folderPath: string) => {
      const nextExpanded = !targetExpandedPaths[folderPath];
      setTargetExpandedPaths((prev) => ({ ...prev, [folderPath]: nextExpanded }));
      if (nextExpanded) void fetchTargetChildren(folderPath);
    },
    [fetchTargetChildren, targetExpandedPaths],
  );

  // Render target directory tree node 1:1 matching FileBrowser.tsx
  const renderTargetNode = (file: CloudFile, depth: number = 0) => {
    const isExpanded = !!targetExpandedPaths[file.path];
    const isSelected = targetDir === file.path;
    const isLoading = !!targetLoadingPaths[file.path];
    const children = targetDirectoryContents[file.path] || [];

    return (
      <div key={file.path} className="select-none font-sans text-xs">
        {/* Row */}
        <div
          className={`flex items-center gap-2.5 py-2 px-3 border-b border-[var(--color-border-light)] hover:bg-[var(--color-bg-tertiary)] transition-colors duration-150 rounded-md ${
            isSelected
              ? "bg-[var(--color-bg-secondary)] font-bold border border-[var(--color-border)] text-[var(--color-text-primary)]"
              : ""
          }`}
          style={{ paddingLeft: `${depth * 16 + 12}px` }}
        >
          {/* Collapse/Expand Arrow */}
          <button
            type="button"
            className="w-4 h-4 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors"
            onClick={(e) => {
              e.stopPropagation();
              toggleTargetExpand(file.path);
            }}
            aria-label={
              isExpanded
                ? t("common.collapse", { name: file.name })
                : t("common.expand", { name: file.name })
            }
          >
            {isLoading ? (
              <RefreshCw className="w-3 h-3 animate-spin text-[var(--color-text-primary)]" />
            ) : isExpanded ? (
              <ChevronDown className="w-3.5 h-3.5" />
            ) : (
              <ChevronRight className="w-3.5 h-3.5" />
            )}
          </button>

          {/* Icon */}
          <span className="text-[var(--color-text-primary)]">
            {isExpanded ? (
              <FolderOpen className="w-4 h-4 text-[var(--color-text-secondary)]" />
            ) : (
              <Folder className="w-4 h-4 text-[var(--color-text-secondary)]" />
            )}
          </span>

          {/* Name */}
          <button
            type="button"
            className={`text-[11.5px] truncate flex-grow leading-normal py-0.5 text-left ${
              isSelected
                ? "text-[var(--color-text-primary)]"
                : "text-[var(--color-text-secondary)]"
            }`}
            onClick={() => setTargetDir(file.path)}
            aria-pressed={isSelected}
          >
            {file.name}
          </button>

          {/* Select Indicator */}
          {isSelected && (
            <Check className="w-3.5 h-3.5 text-[var(--color-text-primary)] stroke-[3]" />
          )}
        </div>

        {/* Children (Recursion) */}
        {isExpanded && (
          <div className="relative">
            <div className="absolute left-[20px] top-0 bottom-3 border-l border-[var(--color-border)]"></div>
            {children.length > 0 ? (
              children.map((child) => renderTargetNode(child, depth + 1))
            ) : isLoading ? null : (
              <div className="text-[10px] text-[var(--color-text-muted)] italic py-2 pl-[42px] text-left">
                {t("fileBrowser.noSubdirs")}
              </div>
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
        tabIndex={-1}
        className="ui-card w-full max-w-5xl p-6 bg-[var(--color-bg-primary)] border-[var(--color-border)] relative max-h-[90vh] overflow-y-auto text-left space-y-6"
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
            title={t("common.close")}
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
                <span className="ui-card inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-mono text-[var(--color-text-primary)] font-bold">
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

        {/* Target Directory Browser Modal 1:1 matching FileBrowser.tsx */}
        {isTargetBrowserOpen && job.target_provider !== "immich" && (
          <div className="fixed inset-0 bg-[var(--color-overlay)] z-[var(--layer-dialog)] flex items-center justify-center p-4">
            <div
              ref={targetDialogRef}
              role="dialog"
              aria-modal="true"
              aria-labelledby={targetDialogTitleId}
              tabIndex={-1}
              className="ui-card max-w-lg w-full max-h-[85vh] flex flex-col overflow-hidden text-left"
            >
              {/* Modal Header */}
              <div className="p-5 border-b border-[var(--color-border-light)] flex items-center justify-between bg-[var(--color-bg-tertiary)]/50">
                <div>
                  <h3
                    id={targetDialogTitleId}
                    className="font-display font-extrabold text-lg text-[var(--color-text-primary)] tracking-tight"
                  >
                    {t("fileBrowser.targetSelectTitle")}
                  </h3>
                  <p className="text-xs text-[var(--color-text-muted)] mt-0.5 uppercase tracking-wider font-mono">
                    {t("fileBrowser.targetSelectSubtitle")}
                  </p>
                </div>
                <button
                  type="button"
                  ref={targetCloseButtonRef}
                  onClick={closeTargetBrowser}
                  className="ui-button-secondary p-1.5 hover:bg-[var(--color-bg-tertiary)]"
                  aria-label={t("paths.close")}
                  title={t("paths.close")}
                >
                  <X className="w-5 h-5" />
                </button>
              </div>

              {/* Modal Content - Directory Tree */}
              <div className="p-5 flex-grow overflow-y-auto min-h-[300px]">
                {targetError && (
                  <div className="ui-alert ui-alert-error mb-4 p-3 text-xs flex gap-2">
                    <AlertTriangle className="w-4 h-4 shrink-0" />
                    <span>{targetError}</span>
                  </div>
                )}

                <div className="border border-[var(--color-border)]/60 rounded-lg bg-[var(--color-bg-tertiary)]/30 p-2 overflow-x-auto max-h-[350px]">
                  {/* Root Directory Node */}
                  <div className="select-none font-sans text-xs">
                    <div
                      className={`flex items-center gap-2.5 py-2 px-3 border border-transparent hover:bg-[var(--color-bg-tertiary)]/50 transition-colors duration-150 rounded-xl ${
                        targetDir === "/"
                          ? "bg-[var(--color-bg-secondary)] font-bold border-[var(--color-border)] text-[var(--color-text-primary)]"
                          : ""
                      }`}
                    >
                      <button
                        type="button"
                        className="w-4 h-4 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors cursor-pointer"
                        onClick={() => toggleTargetExpand("/")}
                        aria-label={
                          targetExpandedPaths["/"]
                            ? t("common.collapse", { name: t("fileBrowser.mainDir") })
                            : t("common.expand", { name: t("fileBrowser.mainDir") })
                        }
                      >
                        {targetLoadingPaths["/"] ? (
                          <RefreshCw className="w-3 h-3 animate-spin text-[var(--color-text-primary)]" />
                        ) : targetExpandedPaths["/"] ? (
                          <ChevronDown className="w-3.5 h-3.5" />
                        ) : (
                          <ChevronRight className="w-3.5 h-3.5" />
                        )}
                      </button>
                      <span className="text-[var(--color-text-primary)]">
                        {targetExpandedPaths["/"] ? (
                          <FolderOpen className="w-4 h-4 text-[var(--color-text-secondary)]" />
                        ) : (
                          <Folder className="w-4 h-4 text-[var(--color-text-secondary)]" />
                        )}
                      </span>
                      <button
                        type="button"
                        className={`text-[11.5px] truncate flex-grow text-left leading-normal py-0.5 ${
                          targetDir === "/"
                            ? "text-[var(--color-text-primary)]"
                            : "text-[var(--color-text-secondary)]"
                        }`}
                        onClick={() => setTargetDir("/")}
                        aria-pressed={targetDir === "/"}
                      >
                        {t("fileBrowser.mainDir")}
                      </button>
                      {targetDir === "/" && (
                        <Check className="w-3.5 h-3.5 text-[var(--color-text-primary)] stroke-[3]" />
                      )}
                    </div>

                    {/* Root Children */}
                    {targetExpandedPaths["/"] && (
                      <div className="relative">
                        <div className="absolute left-[20px] top-0 bottom-3 border-l border-[var(--color-border)]"></div>
                        {targetDirectoryContents["/"] &&
                        targetDirectoryContents["/"].length > 0 ? (
                          targetDirectoryContents["/"].map((child) =>
                            renderTargetNode(child, 1),
                          )
                        ) : targetLoadingPaths["/"] ? null : (
                          <div className="text-[10px] text-[var(--color-text-muted)] italic py-2 pl-[42px] text-left">
                            {t("fileBrowser.noSubdirs")}
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                </div>
              </div>

              {/* Folder creation form */}
              {isCreatingFolder && (
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    void handleCreateTargetFolder(targetDir);
                  }}
                  className="p-4 border-t border-[var(--color-border-light)] bg-[var(--color-bg-tertiary)]/50 flex items-center gap-3 text-left"
                >
                  <div className="flex-grow space-y-1">
                    <label htmlFor="target-folder-name" className="block text-xs font-bold font-mono text-[var(--color-text-muted)] uppercase tracking-wider">
                      {t("fileBrowser.mkdirIn", { path: targetDir })}
                    </label>
                    <input
                      id="target-folder-name"
                      type="text"
                      value={newFolderName}
                      onChange={(e) => setNewFolderName(e.target.value)}
                      placeholder={t("fileBrowser.mkdirPlaceholder")}
                      className="ui-input w-full py-2 px-3 text-xs text-[var(--color-text-primary)]"
                      autoFocus
                    />
                  </div>
                  <div className="flex items-end gap-1.5 pt-5">
                    <button
                      type="submit"
                      disabled={!newFolderName.trim() || creatingFolder}
                      className="ui-button-primary px-3.5 py-2 text-xs font-mono font-bold uppercase hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      {t("common.create")}
                    </button>
                    <button
                      type="button"
                      onClick={() => {
                        setIsCreatingFolder(false);
                        setNewFolderName("");
                      }}
                      className="ui-button-secondary px-3.5 py-2 text-xs font-mono font-bold uppercase hover:bg-[var(--color-bg-tertiary)]"
                    >
                      {t("common.cancel")}
                    </button>
                  </div>
                </form>
              )}

              {/* Modal Footer */}
              <div className="p-4 border-t border-[var(--color-border-light)] flex items-center justify-between bg-[var(--color-bg-tertiary)]/50">
                <div className="text-left max-w-[200px] md:max-w-[240px] space-y-0.5">
                  <p className="text-xs text-[var(--color-text-muted)] font-bold font-mono uppercase tracking-wider">
                    {t("fileBrowser.selectionLabel")}
                  </p>
                  <p className="font-mono text-[11px] text-[var(--color-text-primary)] truncate font-semibold">
                    {targetDir}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={() => setIsCreatingFolder(true)}
                    className="ui-button-secondary px-3.5 py-2 text-[11px] font-mono font-bold uppercase hover:bg-[var(--color-bg-tertiary)] flex items-center gap-1.5"
                    title={t("fileBrowser.newFolderHint")}
                  >
                    <FolderPlus className="w-4 h-4 text-[var(--color-text-primary)]" />
                    <span>{t("fileBrowser.newFolder")}</span>
                  </button>
                  <button
                    type="button"
                    onClick={closeTargetBrowser}
                    className="ui-button-primary px-4 py-2 text-[11px] font-mono font-bold uppercase hover:opacity-90"
                  >
                    {t("common.select")}
                  </button>
                </div>
              </div>
            </div>
          </div>
        )}

        {/* Source File Tree Card 1:1 matching FileBrowser.tsx */}
        <div className="ui-card flex flex-col p-5">
          {/* Header Bar */}
          <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-4 mb-4 gap-4">
            <div className="flex items-center gap-2">
              <span className="font-mono text-xs font-bold uppercase tracking-wider text-[var(--color-text-primary)]">
                {t("fileBrowser.files")} ({pathsToMigrate.length})
              </span>
            </div>

            <div className="flex items-center gap-2 shrink-0">
              <button
                type="button"
                onClick={deselectAll}
                className="ui-button-secondary p-2.5 hover:bg-[var(--color-bg-tertiary)] transition-all cursor-pointer flex items-center gap-1.5"
                title={t("common.deselectAll")}
              >
                <X className="w-4 h-4" />
                <span className="text-[11px] font-mono font-bold uppercase tracking-wider">
                  {t("common.deselectAll")}
                </span>
              </button>

              <button
                type="button"
                onClick={refreshFiles}
                className="ui-button-secondary p-2.5 hover:bg-[var(--color-bg-tertiary)] transition-all cursor-pointer flex items-center gap-1.5"
                title={t("common.refresh")}
              >
                <RefreshCw className={`w-4 h-4 ${loadingPaths["/"] ? "animate-spin" : ""}`} />
              </button>
            </div>
          </div>

          <div className="flex-grow overflow-y-auto rounded-lg max-h-96">
            {directoryContents["/"]?.length > 0 ? (
              directoryContents["/"].map((file) => renderNode(file, 0))
            ) : (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                <Folder className="w-10 h-10 text-[var(--color-text-muted)]" />
                <p className="font-mono text-xs italic text-[var(--color-text-muted)]">
                  {t("fileBrowser.noFiles")}
                </p>
              </div>
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
