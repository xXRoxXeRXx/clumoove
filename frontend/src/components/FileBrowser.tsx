import React, {
  useState,
  useMemo,
  useEffect,
  useCallback,
  useId,
  useRef,
} from "react";
import {
  ArrowLeftIcon as ArrowLeft,
  ArrowPathIcon as RefreshCw,
  BookOpenIcon as BookOpen,
  CalendarDaysIcon as Calendar,
  CheckIcon as Check,
  ChevronDownIcon as ChevronDown,
  ChevronRightIcon as ChevronRight,
  ExclamationTriangleIcon as AlertTriangle,
  FileIcon,
  FolderIcon as Folder,
  FolderOpenIcon as FolderOpen,
  FolderPlusIcon as FolderPlus,
  PlayIcon as Play,
  XMarkIcon as X,
} from "./icons";
import type { CloudFile, MigrationConfig } from "../types";
import { useTranslation } from "react-i18next";
import { useFormat } from "../utils/format";
import { useApiError } from "../utils/apiError";
import { apiFetch } from "../utils/apiClient";
import { logger } from "../utils/logger";
import { SelectedPathsViewer } from "./SelectedPathsViewer";
import { Button } from "./Button";
import { useFocusTrap } from "../hooks/useFocusTrap";
import { SyncOptionsForm } from "./SyncOptionsForm";
import { BackupOptionsForm } from "./BackupOptionsForm";

interface FileBrowserProps {
  initialFiles: CloudFile[];
  credentials: MigrationConfig;
  apiUrl: string;
  onBack: () => void;
  onStartSuccess: (id: string, isSync?: boolean, isBackup?: boolean) => void;
  token: string;
}

// toLocalInputValue formats a Date as a local-time datetime-local string
// (YYYY-MM-DDTHH:MM) without UTC conversion. datetime-local inputs expect the
// value in the user's local timezone, so using toISOString() (which is UTC)
// would shift the minimum by the timezone offset.
const toLocalInputValue = (date: Date): string => {
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}`
  );
};

// sortEntries returns a new array with folders first, then files, each group
// sorted alphabetically (case-insensitive). Used for files, calendars and
// contacts so the selection lists are consistently ordered.
const sortEntries = (entries: CloudFile[]): CloudFile[] => {
  return [...entries].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
    return a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
  });
};

const isOneDrivePersonalVault = (file: CloudFile) =>
  file.metadata?.custom_props?.onedrive_special_folder === "vault";

const isAbortError = (error: unknown) =>
  error instanceof DOMException && error.name === "AbortError";

interface SourceTreeRowProps {
  file: CloudFile;
  depth: number;
  isExpanded: boolean;
  isSelected: boolean;
  isLoading: boolean;
  isPersonalVault: boolean;
  isFocused: boolean;
  formattedSize: string;
  expandLabel: string;
  collapseLabel: string;
  selectLabel: string;
  unavailableLabel: string;
  treeItemRefs: React.MutableRefObject<Map<string, HTMLDivElement>>;
  onExpand: (path: string) => void;
  onSelect: (path: string) => void;
  onFocus: (path: string) => void;
}

const SourceTreeRow = React.memo(function SourceTreeRow({
  file,
  depth,
  isExpanded,
  isSelected,
  isLoading,
  isPersonalVault,
  isFocused,
  formattedSize,
  expandLabel,
  collapseLabel,
  selectLabel,
  unavailableLabel,
  treeItemRefs,
  onExpand,
  onSelect,
  onFocus,
}: SourceTreeRowProps) {
  return (
    <div
      ref={(element) => {
        if (element) treeItemRefs.current.set(file.path, element);
        else treeItemRefs.current.delete(file.path);
      }}
      role="treeitem"
      aria-level={depth + 1}
      aria-expanded={file.is_dir ? isExpanded : undefined}
      aria-selected={isSelected}
      tabIndex={isFocused ? 0 : -1}
      onFocus={() => onFocus(file.path)}
      className={`flex items-center gap-3 py-3.5 px-4 border-b border-[var(--color-border-light)] hover:bg-[var(--color-bg-tertiary)] transition-colors duration-150 ${
        isSelected ? "bg-[var(--color-bg-tertiary)] font-semibold" : ""
      }`}
      style={{ paddingLeft: `${depth * 20 + 16}px` }}
    >
      {file.is_dir ? (
        <button
          type="button"
          tabIndex={-1}
          className="w-5 h-5 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors"
          onClick={() => !isPersonalVault && onExpand(file.path)}
          disabled={isPersonalVault}
          aria-label={isExpanded ? collapseLabel : expandLabel}
        >
          {isLoading ? <RefreshCw className="w-3.5 h-3.5 animate-spin text-[var(--color-text-primary)]" /> : isExpanded ? <ChevronDown className="w-4 h-4 stroke-[2]" /> : <ChevronRight className="w-4 h-4 stroke-[2]" />}
        </button>
      ) : <span className="w-5" />}

      <button
        type="button"
        tabIndex={-1}
        onClick={(event) => {
          event.stopPropagation();
          if (!isPersonalVault) onSelect(file.path);
        }}
        disabled={isPersonalVault}
        className="flex items-center justify-center"
        aria-label={isPersonalVault ? unavailableLabel : selectLabel}
        title={isPersonalVault ? unavailableLabel : undefined}
      >
        <span className={`w-4 h-4 border rounded flex items-center justify-center transition-all duration-200 ${isSelected ? "bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)] border-transparent" : "bg-[var(--color-bg-secondary)] border-[var(--color-border)] hover:border-[var(--color-border)]"}`}>
          {isSelected && <Check className="w-3 h-3 text-[var(--color-text-inverse)] stroke-[3.5]" />}
        </span>
      </button>

      <span className="shrink-0">
        {file.is_dir ? (isExpanded ? <FolderOpen className="w-5 h-5 text-[var(--color-text-secondary)]" /> : <Folder className="w-5 h-5 text-[var(--color-text-secondary)]" />) : <FileIcon name={file.name} className="w-5 h-5 shrink-0" />}
      </span>
      <span className={`text-[12px] truncate flex-grow leading-normal py-0.5 ${isPersonalVault ? "text-[var(--color-text-muted)]" : isSelected ? "text-[var(--color-text-primary)] font-bold" : "text-[var(--color-text-primary)]"}`}>
        {file.name}
        {isPersonalVault && <span className="ml-2 text-[10px]">{unavailableLabel}</span>}
      </span>
      {!file.is_dir && <span className="ui-badge ui-badge-muted text-[10px] font-bold px-2 py-0.5 rounded">{formattedSize}</span>}
    </div>
  );
});

export const FileBrowser: React.FC<FileBrowserProps> = ({
  initialFiles,
  credentials,
  apiUrl,
  onBack,
  onStartSuccess,
  token,
}) => {
  const { t } = useTranslation();
  const { formatBytes } = useFormat();
  const translateApiError = useApiError();
  const isImmichSource = credentials.source_provider === "immich";
  const isImmichTarget = credentials.target_provider === "immich";
  const hasImmichEndpoint = isImmichSource || isImmichTarget;
  const backupAvailable = !hasImmichEndpoint
    && Boolean(credentials.source_profile_id)
    && Boolean(credentials.target_profile_id);

  const supportsCalendars = useMemo(() => {
    const src = credentials.source_provider || "nextcloud";
    const tgt = credentials.target_provider || "nextcloud";
    return (
      (src === "nextcloud" || src === "google") &&
      (tgt === "nextcloud" || tgt === "google")
    );
  }, [credentials.source_provider, credentials.target_provider]);

  const supportsContacts = useMemo(() => {
    const src = credentials.source_provider || "nextcloud";
    const tgt = credentials.target_provider || "nextcloud";
    return (
      (src === "nextcloud" || src === "google") &&
      (tgt === "nextcloud" || tgt === "google")
    );
  }, [credentials.source_provider, credentials.target_provider]);

  const [activeTab, setActiveTab] = useState<
    "files" | "calendars" | "contacts"
  >("files");
  const [calendars, setCalendars] = useState<CloudFile[]>([]);
  const [contacts, setContacts] = useState<CloudFile[]>([]);
  const [loadingCalendars, setLoadingCalendars] = useState(false);
  const [loadingContacts, setLoadingContacts] = useState(false);
  const [selectedCalendars, setSelectedCalendars] = useState<
    Record<string, boolean>
  >({});
  const [selectedContacts, setSelectedContacts] = useState<
    Record<string, boolean>
  >({});

  const [expandedPaths, setExpandedPaths] = useState<Record<string, boolean>>(
    {},
  );
  const [directoryContents, setDirectoryContents] = useState<
    Record<string, CloudFile[]>
  >(() => ({
    "/": sortEntries(initialFiles),
  }));
  // All files/folders are selected by default. Pre-populate the top-level
  // entries so the selection checkboxes render checked on first paint.
  const [selectedPaths, setSelectedPaths] = useState<Record<string, boolean>>(
    () => {
      return initialFiles.reduce(
        (acc, f) => {
          acc[f.path] = !isOneDrivePersonalVault(f);
          return acc;
        },
        {} as Record<string, boolean>,
      );
    },
  );
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({});
  const [conflictStrategy, setConflictStrategy] = useState("SKIP");
  const [threads, setThreads] = useState<number>(8);
  const [targetDir, setTargetDir] = useState("/");
  const [isTargetBrowserOpen, setIsTargetBrowserOpen] = useState(false);
  const [targetExpandedPaths, setTargetExpandedPaths] = useState<
    Record<string, boolean>
  >({});
  const [targetDirectoryContents, setTargetDirectoryContents] = useState<
    Record<string, CloudFile[]>
  >({});
  const [targetLoadingPaths, setTargetLoadingPaths] = useState<
    Record<string, boolean>
  >({});
  const [targetError, setTargetError] = useState<string | null>(null);
  const [isCreatingFolder, setIsCreatingFolder] = useState(false);
  const [newFolderName, setNewFolderName] = useState("");
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const targetDialogRef = useRef<HTMLDivElement>(null);
  const targetCloseButtonRef = useRef<HTMLButtonElement>(null);
  const targetDialogTitleId = useId();
  const sourcePanelId = useId();
  const calendarsPanelId = useId();
  const contactsPanelId = useId();
  const loadingPathsRef = useRef(loadingPaths);
  const directoryContentsRef = useRef(directoryContents);
  const targetLoadingPathsRef = useRef(targetLoadingPaths);
  const targetDirectoryContentsRef = useRef(targetDirectoryContents);
  const loadingCalendarsRef = useRef(loadingCalendars);
  const calendarsRef = useRef(calendars);
  const loadingContactsRef = useRef(loadingContacts);
  const contactsRef = useRef(contacts);
  const hasFetchedCalendarsRef = useRef(false);
  const hasFetchedContactsRef = useRef(false);
  const sourceTreeItemRefs = useRef(new Map<string, HTMLDivElement>());
  const targetTreeItemRefs = useRef(new Map<string, HTMLDivElement>());
  const requestControllersRef = useRef(new Set<AbortController>());
  const [focusedSourcePath, setFocusedSourcePath] = useState<string | null>(null);
  const [focusedTargetPath, setFocusedTargetPath] = useState<string | null>(null);

  useEffect(() => {
    loadingPathsRef.current = loadingPaths;
    directoryContentsRef.current = directoryContents;
    targetLoadingPathsRef.current = targetLoadingPaths;
    targetDirectoryContentsRef.current = targetDirectoryContents;
    loadingCalendarsRef.current = loadingCalendars;
    calendarsRef.current = calendars;
    loadingContactsRef.current = loadingContacts;
    contactsRef.current = contacts;
  }, [loadingPaths, directoryContents, targetLoadingPaths, targetDirectoryContents, loadingCalendars, calendars, loadingContacts, contacts]);

  const createRequestController = useCallback(() => {
    const controller = new AbortController();
    requestControllersRef.current.add(controller);
    return controller;
  }, []);

  const releaseRequestController = useCallback((controller: AbortController) => {
    requestControllersRef.current.delete(controller);
  }, []);

  useEffect(() => () => {
    requestControllersRef.current.forEach((controller) => controller.abort());
    requestControllersRef.current.clear();
  }, []);

  // Job type
  const [jobType, setJobType] = useState<"migration" | "sync" | "backup">("migration");
  const [direction, setDirection] = useState<"one_way" | "two_way">("one_way");
  const [intervalMinutes, setIntervalMinutes] = useState<number>(15);
  const [deletePropagation, setDeletePropagation] = useState<boolean>(false);
  // A profile change can introduce Immich after sync was selected. Deriving the
  // active mode keeps the UI and request path migration-only without a stateful
  // effect that would cause an unnecessary render.
  const effectiveJobType = hasImmichEndpoint ? "migration" : jobType;
  const [backupCronExpression, setBackupCronExpression] = useState("0 2 * * *");
  const [backupTimezone, setBackupTimezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  const [backupRetentionCount, setBackupRetentionCount] = useState(7);

  // Scheduling state
  const [enableScheduling, setEnableScheduling] = useState(false);
  const [scheduledTime, setScheduledTime] = useState("");
  const [bandwidthLimit, setBandwidthLimit] = useState(0);

  const closeTargetBrowser = useCallback(() => {
    setIsTargetBrowserOpen(false);
    setIsCreatingFolder(false);
    setNewFolderName("");
  }, []);

  useFocusTrap(
    targetDialogRef,
    targetCloseButtonRef,
    closeTargetBrowser,
    isTargetBrowserOpen,
  );

  const pathsToMigrate = useMemo(
    () => Object.keys(selectedPaths).filter((p) => selectedPaths[p]),
    [selectedPaths],
  );

  const syncSelectedPaths = useMemo(() => {
    const rootItems = directoryContents["/"] || sortEntries(initialFiles);
    const selectableRootItems = rootItems.filter(
      (item) => !isOneDrivePersonalVault(item),
    );
    const allRootSelected =
      selectableRootItems.length > 0 &&
      selectableRootItems.every((item) => selectedPaths[item.path]);
    return allRootSelected ? [] : pathsToMigrate;
  }, [directoryContents, initialFiles, selectedPaths, pathsToMigrate]);

  const backupSelectedPaths = useMemo(() => pathsToMigrate.filter((candidate) => !pathsToMigrate.some(
    (other) => other !== candidate && candidate.startsWith(`${other}/`),
  )), [pathsToMigrate]);

  // Minimum selectable start time: now + 1 minute, formatted in the user's
  // local timezone (datetime-local inputs expect local time, not UTC).
  // Computed once via a useState lazy initializer to keep render pure
  // (no Date.now() called during render).
  const [minScheduledTime] = useState(() =>
    toLocalInputValue(new Date(Date.now() + 60000)),
  );

  const fetchTargetChildren = useCallback(async (folderPath: string, depth = 0, force = false) => {
    if (!force && targetLoadingPathsRef.current[folderPath]) return;
    if (!force && targetDirectoryContentsRef.current[folderPath]) return;

    const controller = createRequestController();
    setTargetLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
    setTargetError(null);
    try {
      const response = await apiFetch(`${apiUrl}/api/migration/target/browse`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          target_url: credentials.target_url,
          target_username: credentials.target_username,
          target_password: credentials.target_password,
          target_provider: credentials.target_provider,
          target_profile_id: credentials.target_profile_id,
          path: folderPath,
        }),
        signal: controller.signal,
      });

      if (!response.ok) {
        const b = await response
          .json()
          .catch(() => ({}) as { error_code?: string });
        throw new Error(
          b.error_code
            ? translateApiError(b.error_code)
            : t("fileBrowser.errors.loadTarget"),
        );
      }

      const data = await response.json();
      if (controller.signal.aborted) return;
      if (data.success) {
        const foldersOnly = sortEntries(
          (data.files || data.items || []).filter((f: CloudFile) => f.is_dir),
        );
        setTargetDirectoryContents((prev) => {
          const next = { ...prev, [folderPath]: foldersOnly };
          targetDirectoryContentsRef.current = next;
          return next;
        });
        // Only the first folder level is loaded directly. Deeper levels are
        // loaded on demand when the user expands a folder.
        if (depth < 1) {
          setTargetExpandedPaths((prev) => ({ ...prev, [folderPath]: true }));
        }
      } else {
        setTargetError(
          data.error_code
            ? translateApiError(data.error_code)
            : t("fileBrowser.errors.loadTarget"),
        );
      }
    } catch (err) {
      if (controller.signal.aborted || isAbortError(err)) return;
      logger.error("Failed to load target directory", err);
      setTargetError(
        err instanceof Error ? err.message : t("fileBrowser.errors.loadTarget"),
      );
    } finally {
      releaseRequestController(controller);
      if (!controller.signal.aborted) {
        setTargetLoadingPaths((prev) => ({ ...prev, [folderPath]: false }));
      }
    }
  }, [apiUrl, createRequestController, credentials, releaseRequestController, t, token, translateApiError]);

  const handleCreateTargetFolder = async (parentPath: string) => {
    const trimmedName = newFolderName.trim();
    if (!trimmedName) return;

    // Client-side defense-in-depth against path traversal. Strip path
    // separators and any ".." segments; the backend remains authoritative.
    const safeName = trimmedName
      .split("/")
      .join("")
      .split("\\")
      .join("")
      .split("..")
      .join("")
      .trim();
    if (!safeName) {
      setTargetError(t("fileBrowser.errors.invalidFolderName"));
      return;
    }

    const fullNewPath =
      parentPath === "/" ? `/${safeName}` : `${parentPath}/${safeName}`;

    const controller = createRequestController();
    setTargetLoadingPaths((prev) => ({ ...prev, [parentPath]: true }));
    setTargetError(null);
    try {
      const response = await apiFetch(`${apiUrl}/api/migration/target/mkdir`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          target_url: credentials.target_url,
          target_username: credentials.target_username,
          target_password: credentials.target_password,
          target_provider: credentials.target_provider,
          target_profile_id: credentials.target_profile_id,
          path: fullNewPath,
        }),
        signal: controller.signal,
      });

      if (!response.ok) {
        const b = await response
          .json()
          .catch(() => ({}) as { error_code?: string });
        throw new Error(
          b.error_code
            ? translateApiError(b.error_code)
            : t("fileBrowser.errors.createFolder"),
        );
      }

      const data = await response.json();
      if (controller.signal.aborted) return;
      if (data.success) {
        setNewFolderName("");
        setIsCreatingFolder(false);

        setTargetDir(fullNewPath);
        setTargetExpandedPaths((prev) => ({ ...prev, [parentPath]: true }));

        setTargetDirectoryContents((prev) => {
          const next = { ...prev };
          delete next[parentPath];
          targetDirectoryContentsRef.current = next;
          return next;
        });
        await fetchTargetChildren(parentPath, 0, true);
      } else {
        setTargetError(
          data.error_code
            ? translateApiError(data.error_code)
            : t("fileBrowser.errors.createFolder"),
        );
      }
    } catch (err) {
      if (controller.signal.aborted || isAbortError(err)) return;
      logger.error("Failed to create target directory", err);
      setTargetError(
        err instanceof Error
          ? err.message
          : t("fileBrowser.errors.createFolder"),
      );
    } finally {
      releaseRequestController(controller);
      if (!controller.signal.aborted) {
        setTargetLoadingPaths((prev) => ({ ...prev, [parentPath]: false }));
      }
    }
  };

  const openTargetBrowser = useCallback(() => {
    setIsTargetBrowserOpen(true);
    setTargetExpandedPaths((prev) => ({ ...prev, "/": true }));
    void fetchTargetChildren("/", 0, true);
  }, [fetchTargetChildren]);

  const fetchCalendars = useCallback(
    async (force?: boolean) => {
      if (!force && (calendarsRef.current.length > 0 || loadingCalendarsRef.current)) return;
      const controller = createRequestController();
      setLoadingCalendars(true);
      try {
        const response = await apiFetch(`${apiUrl}/api/migration/browse`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({
            source_url: credentials.source_url,
            source_username: credentials.source_username,
            source_password: credentials.source_password,
            source_provider: credentials.source_provider,
            source_profile_id: credentials.source_profile_id,
            resource_type: "calendars",
          }),
          signal: controller.signal,
        });
        if (!response.ok) {
          const b = await response
            .json()
            .catch(() => ({}) as { error_code?: string });
          throw new Error(
            b.error_code
              ? translateApiError(b.error_code)
              : t("fileBrowser.errors.loadCalendars"),
          );
        }
        const data = await response.json();
        if (controller.signal.aborted) return;
        if (data.success) {
          const items = sortEntries(data.items || []);
          setCalendars(items);
          setSelectedCalendars((prev) => {
            const next = { ...prev };
            for (const c of items) {
              if (next[c.path] === undefined) next[c.path] = true;
            }
            return next;
          });
        }
      } catch (err) {
        if (controller.signal.aborted || isAbortError(err)) return;
        logger.error("Failed to load calendars", err);
      } finally {
        releaseRequestController(controller);
        if (!controller.signal.aborted) setLoadingCalendars(false);
      }
    },
    [
      apiUrl,
      createRequestController,
      credentials,
      releaseRequestController,
      t,
      token,
      translateApiError,
    ],
  );

  const fetchContacts = useCallback(
    async (force?: boolean) => {
      if (!force && (contactsRef.current.length > 0 || loadingContactsRef.current)) return;
      const controller = createRequestController();
      setLoadingContacts(true);
      try {
        const response = await apiFetch(`${apiUrl}/api/migration/browse`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({
            source_url: credentials.source_url,
            source_username: credentials.source_username,
            source_password: credentials.source_password,
            source_provider: credentials.source_provider,
            source_profile_id: credentials.source_profile_id,
            resource_type: "contacts",
          }),
          signal: controller.signal,
        });
        if (!response.ok) {
          const b = await response
            .json()
            .catch(() => ({}) as { error_code?: string });
          throw new Error(
            b.error_code
              ? translateApiError(b.error_code)
              : t("fileBrowser.errors.loadContacts"),
          );
        }
        const data = await response.json();
        if (controller.signal.aborted) return;
        if (data.success) {
          const items = sortEntries(data.items || []);
          setContacts(items);
          setSelectedContacts((prev) => {
            const next = { ...prev };
            for (const c of items) {
              if (next[c.path] === undefined) next[c.path] = true;
            }
            return next;
          });
        }
      } catch (err) {
        if (controller.signal.aborted || isAbortError(err)) return;
        logger.error("Failed to load contacts", err);
      } finally {
        releaseRequestController(controller);
        if (!controller.signal.aborted) setLoadingContacts(false);
      }
    },
    [
      apiUrl,
      createRequestController,
      credentials,
      releaseRequestController,
      t,
      token,
      translateApiError,
    ],
  );

  const effectiveActiveTab = useMemo(() => {
    if (effectiveJobType === "backup") return "files";
    if (activeTab === "calendars" && !supportsCalendars) return "files";
    if (activeTab === "contacts" && !supportsContacts) return "files";
    return activeTab;
  }, [activeTab, effectiveJobType, supportsCalendars, supportsContacts]);

  useEffect(() => {
    const timer = setTimeout(() => {
      if (effectiveJobType !== "backup" && supportsCalendars && !hasFetchedCalendarsRef.current) {
        hasFetchedCalendarsRef.current = true;
        void fetchCalendars();
      } else if (!supportsCalendars) {
        setSelectedCalendars({});
      }
      if (effectiveJobType !== "backup" && supportsContacts && !hasFetchedContactsRef.current) {
        hasFetchedContactsRef.current = true;
        void fetchContacts();
      } else if (!supportsContacts) {
        setSelectedContacts({});
      }
    }, 0);
    return () => clearTimeout(timer);
  }, [effectiveJobType, supportsCalendars, supportsContacts, fetchCalendars, fetchContacts]);

  const handleTabChange = (tab: "files" | "calendars" | "contacts") => {
    setActiveTab(tab);
    if (tab === "calendars") fetchCalendars();
    if (tab === "contacts") fetchContacts();
  };

  const handleTabListKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const tabs: Array<"files" | "calendars" | "contacts"> = ["files"];
    if (effectiveJobType !== "backup" && supportsCalendars) tabs.push("calendars");
    if (effectiveJobType !== "backup" && supportsContacts) tabs.push("contacts");
    const currentIndex = tabs.indexOf(effectiveActiveTab);
    if (event.key === "ArrowRight" || event.key === "ArrowLeft") {
      event.preventDefault();
      const direction = event.key === "ArrowRight" ? 1 : -1;
      const nextTab = tabs[(currentIndex + direction + tabs.length) % tabs.length];
      handleTabChange(nextTab);
      requestAnimationFrame(() => document.getElementById(`${nextTab}-tab`)?.focus());
    }
  };

  const fetchChildren = useCallback(
    async (folderPath: string, force?: boolean) => {
      if (loadingPathsRef.current[folderPath]) return;
      if (!force && directoryContentsRef.current[folderPath]) return;

      const controller = createRequestController();
      setLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
      try {
        const response = await apiFetch(`${apiUrl}/api/migration/browse`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({
            source_url: credentials.source_url,
            source_username: credentials.source_username,
            source_password: credentials.source_password,
            source_provider: credentials.source_provider,
            source_profile_id: credentials.source_profile_id,
            resource_type: "files",
            path: folderPath,
          }),
          signal: controller.signal,
        });

        if (!response.ok) {
          const b = await response
            .json()
            .catch(() => ({}) as { error_code?: string });
          throw new Error(
            b.error_code
              ? translateApiError(b.error_code)
              : t("fileBrowser.errors.loadDir"),
          );
        }

        const data = await response.json();
        if (controller.signal.aborted) return;
        if (data.success) {
          const items = sortEntries(data.items || data.files || []);
          setDirectoryContents((prev) => ({ ...prev, [folderPath]: items }));
          setSelectedPaths((prev) => {
            const next = { ...prev };
            for (const child of items) {
              if (next[child.path] === undefined) {
                next[child.path] = !isOneDrivePersonalVault(child);
              }
            }
            return next;
          });
        } else {
          setError(
            data.error_code
              ? translateApiError(data.error_code)
              : t("fileBrowser.errors.loadDir"),
          );
        }
      } catch (err) {
        if (controller.signal.aborted || isAbortError(err)) return;
        logger.error("Failed to load source directory", err);
        setError(
          err instanceof Error ? err.message : t("fileBrowser.errors.loadDir"),
        );
      } finally {
        releaseRequestController(controller);
        if (!controller.signal.aborted) {
          setLoadingPaths((prev) => ({ ...prev, [folderPath]: false }));
        }
      }
    },
    [
      apiUrl,
      createRequestController,
      token,
      credentials,
      releaseRequestController,
      translateApiError,
      t,
    ],
  );

  const refreshFiles = async () => {
    setDirectoryContents({});
    setExpandedPaths({});
    await fetchChildren("/", true);
  };

  const toggleExpand = useCallback((folderPath: string) => {
    setExpandedPaths((prev) => ({ ...prev, [folderPath]: !prev[folderPath] }));
    void fetchChildren(folderPath);
  }, [fetchChildren]);

  const toggleSelect = useCallback((filePath: string) => {
    setSelectedPaths((prev) => ({ ...prev, [filePath]: !prev[filePath] }));
  }, []);

  const deselectAll = () => {
    setSelectedPaths({});
    setSelectedCalendars({});
    setSelectedContacts({});
  };

  const sourceVisibleNodes = useMemo(() => {
    const nodes: Array<{ file: CloudFile; depth: number }> = [];
    const addChildren = (entries: CloudFile[], depth: number) => {
      entries.forEach((file) => {
        nodes.push({ file, depth });
        if (file.is_dir && expandedPaths[file.path]) {
          addChildren(directoryContents[file.path] || [], depth + 1);
        }
      });
    };
    addChildren(directoryContents["/"] || [], 0);
    return nodes;
  }, [directoryContents, expandedPaths]);

  const resolvedFocusedSourcePath = focusedSourcePath && sourceVisibleNodes.some(({ file }) => file.path === focusedSourcePath)
    ? focusedSourcePath
    : sourceVisibleNodes[0]?.file.path ?? null;

  const focusSourceTreeItem = (path: string) => {
    setFocusedSourcePath(path);
    requestAnimationFrame(() => sourceTreeItemRefs.current.get(path)?.focus());
  };

  const handleSourceTreeKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const currentIndex = sourceVisibleNodes.findIndex(({ file }) => file.path === resolvedFocusedSourcePath);
    if (currentIndex < 0) return;
    const current = sourceVisibleNodes[currentIndex];
    const move = (index: number) => focusSourceTreeItem(sourceVisibleNodes[index].file.path);

    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (currentIndex < sourceVisibleNodes.length - 1) move(currentIndex + 1);
        break;
      case "ArrowUp":
        event.preventDefault();
        if (currentIndex > 0) move(currentIndex - 1);
        break;
      case "ArrowRight":
        event.preventDefault();
        if (current.file.is_dir && !expandedPaths[current.file.path] && !isOneDrivePersonalVault(current.file)) {
          toggleExpand(current.file.path);
        } else if (current.file.is_dir && sourceVisibleNodes[currentIndex + 1]?.depth === current.depth + 1) {
          move(currentIndex + 1);
        }
        break;
      case "ArrowLeft": {
        event.preventDefault();
        if (current.file.is_dir && expandedPaths[current.file.path]) {
          toggleExpand(current.file.path);
          break;
        }
        for (let index = currentIndex - 1; index >= 0; index -= 1) {
          if (sourceVisibleNodes[index].depth < current.depth) {
            move(index);
            break;
          }
        }
        break;
      }
      case "Home":
        event.preventDefault();
        move(0);
        break;
      case "End":
        event.preventDefault();
        move(sourceVisibleNodes.length - 1);
        break;
      case "Enter":
      case " ":
        event.preventDefault();
        if (!isOneDrivePersonalVault(current.file)) toggleSelect(current.file.path);
        break;
    }
  };

  const toggleTargetExpand = (folderPath: string) => {
    const nextExpanded = !targetExpandedPaths[folderPath];
    setTargetExpandedPaths((prev) => ({ ...prev, [folderPath]: nextExpanded }));
    if (nextExpanded) void fetchTargetChildren(folderPath);
  };

  const targetVisibleNodes = useMemo(() => {
    const nodes: Array<{ path: string; name: string; depth: number }> = [{
      path: "/",
      name: t("fileBrowser.mainDir"),
      depth: 0,
    }];
    const addChildren = (entries: CloudFile[], depth: number) => {
      entries.forEach((file) => {
        nodes.push({ path: file.path, name: file.name, depth });
        if (targetExpandedPaths[file.path]) addChildren(targetDirectoryContents[file.path] || [], depth + 1);
      });
    };
    if (targetExpandedPaths["/"]) addChildren(targetDirectoryContents["/"] || [], 1);
    return nodes;
  }, [t, targetDirectoryContents, targetExpandedPaths]);

  const resolvedFocusedTargetPath = isTargetBrowserOpen && focusedTargetPath && targetVisibleNodes.some((node) => node.path === focusedTargetPath)
    ? focusedTargetPath
    : "/";

  const focusTargetTreeItem = (path: string) => {
    setFocusedTargetPath(path);
    requestAnimationFrame(() => targetTreeItemRefs.current.get(path)?.focus());
  };

  const handleTargetTreeKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const currentIndex = targetVisibleNodes.findIndex((node) => node.path === resolvedFocusedTargetPath);
    if (currentIndex < 0) return;
    const current = targetVisibleNodes[currentIndex];
    const move = (index: number) => focusTargetTreeItem(targetVisibleNodes[index].path);
    const isExpanded = !!targetExpandedPaths[current.path];

    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (currentIndex < targetVisibleNodes.length - 1) move(currentIndex + 1);
        break;
      case "ArrowUp":
        event.preventDefault();
        if (currentIndex > 0) move(currentIndex - 1);
        break;
      case "ArrowRight":
        event.preventDefault();
        if (!isExpanded) toggleTargetExpand(current.path);
        else if (targetVisibleNodes[currentIndex + 1]?.depth === current.depth + 1) move(currentIndex + 1);
        break;
      case "ArrowLeft": {
        event.preventDefault();
        if (isExpanded) {
          toggleTargetExpand(current.path);
          break;
        }
        for (let index = currentIndex - 1; index >= 0; index -= 1) {
          if (targetVisibleNodes[index].depth < current.depth) {
            move(index);
            break;
          }
        }
        break;
      }
      case "Home":
        event.preventDefault();
        move(0);
        break;
      case "End":
        event.preventDefault();
        move(targetVisibleNodes.length - 1);
        break;
      case "Enter":
      case " ":
        event.preventDefault();
        setTargetDir(current.path);
        break;
    }
  };

  const handleStartMigration = async () => {
    const calendarsToMigrate = effectiveJobType !== "backup" && supportsCalendars
      ? Object.keys(selectedCalendars).filter((p) => selectedCalendars[p])
      : [];
    const contactsToMigrate = effectiveJobType !== "backup" && supportsContacts
      ? Object.keys(selectedContacts).filter((p) => selectedContacts[p])
      : [];

    if (
      (effectiveJobType === "backup" ? backupSelectedPaths.length === 0 : pathsToMigrate.length === 0) &&
      calendarsToMigrate.length === 0 &&
      contactsToMigrate.length === 0
    ) {
      setError(t("fileBrowser.errors.selectOne"));
      return;
    }

    if (effectiveJobType === "backup") {
      if (!backupAvailable || backupRetentionCount < 1 || backupRetentionCount > 365 || threads < 1 || threads > 16) {
        setError(t("backup.invalidOptions"));
        return;
      }
    } else if (effectiveJobType === "sync") {
      if (pathsToMigrate.length === 0) {
        setError(t("fileBrowser.errors.selectOne"));
        return;
      }
    } else {
      // Validate scheduled time if scheduling is enabled
      if (enableScheduling && scheduledTime) {
        const scheduledDate = new Date(scheduledTime);
        if (scheduledDate <= new Date()) {
          setError(t("fileBrowser.errors.futureTime"));
          return;
        }
      }
    }

    setStarting(true);
    setError(null);
    const controller = createRequestController();

    try {
      if (effectiveJobType === "backup") {
        const response = await apiFetch(`${apiUrl}/api/backup`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({
            source_profile_id: credentials.source_profile_id,
            target_profile_id: credentials.target_profile_id,
            selected_paths: backupSelectedPaths,
            target_dir: targetDir,
            cron_expression: backupCronExpression,
            timezone: backupTimezone,
            retention_count: backupRetentionCount,
            threads,
          }),
          signal: controller.signal,
        });
        if (!response.ok) {
          const body = await response.json().catch(() => ({})) as { error_code?: string };
          throw new Error(body.error_code ? translateApiError(body.error_code) : t("backup.createFailed"));
        }
        const data = await response.json() as { id?: string };
        if (!controller.signal.aborted && data.id) onStartSuccess(data.id, false, true);
        else if (!controller.signal.aborted) setError(t("backup.createFailed"));
      } else if (effectiveJobType === "sync") {
        const response = await apiFetch(`${apiUrl}/api/sync`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({
            source_profile_id: credentials.source_profile_id,
            target_profile_id: credentials.target_profile_id,
            source_url: credentials.source_url,
            source_username: credentials.source_username,
            source_password: credentials.source_password,
            source_refresh_token: credentials.source_refresh_token,
            target_url: credentials.target_url,
            target_username: credentials.target_username,
            target_password: credentials.target_password,
            target_refresh_token: credentials.target_refresh_token,
            source_provider: credentials.source_provider,
            target_provider: credentials.target_provider,
            direction: direction,
            conflict_strategy: conflictStrategy,
            delete_propagation: deletePropagation,
            interval_minutes: intervalMinutes,
            threads: threads,
            bandwidth_limit_mbps: bandwidthLimit,
            target_dir: targetDir,
            selected_paths: syncSelectedPaths,
          }),
          signal: controller.signal,
        });

        if (!response.ok) {
          const b = await response
            .json()
            .catch(() => ({}) as { error_code?: string });
          throw new Error(
            b.error_code
              ? translateApiError(b.error_code)
              : t("sync.createFailed"),
          );
        }

        const data = (await response.json()) as { id?: string; success?: boolean; error_code?: string };
        if (controller.signal.aborted) return;
        if (data.success === false) {
          setError(data.error_code ? translateApiError(data.error_code) : t("sync.createFailed"));
          return;
        }
        if (data.id) {
          // Trigger first pass immediately
          const startResponse = await apiFetch(
            `${apiUrl}/api/sync/${data.id}/start`,
            {
              method: "POST",
              headers: { Authorization: `Bearer ${token}` },
              signal: controller.signal,
            },
          );
          if (!startResponse.ok) {
            const body = await startResponse
              .json()
              .catch(() => ({}) as { error_code?: string });
            throw new Error(
              body.error_code
                ? translateApiError(body.error_code)
                : t("sync.startFailed"),
            );
          }
          onStartSuccess(data.id, true);
        } else {
          setError(t("sync.createFailed"));
        }
      } else {
        const requestBody: Record<string, unknown> = {
          ...credentials,
          conflict_strategy: conflictStrategy,
          paths: pathsToMigrate,
          calendars: calendarsToMigrate,
          contacts: contactsToMigrate,
          target_dir: targetDir,
          threads: threads,
          bandwidth_limit_mbps: bandwidthLimit,
        };

        // Add scheduled_time if scheduling is enabled
        if (enableScheduling && scheduledTime) {
          requestBody.scheduled_time = new Date(scheduledTime).toISOString();
        }

        const response = await apiFetch(`${apiUrl}/api/migration/start`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify(requestBody),
          signal: controller.signal,
        });

        if (!response.ok) {
          const b = await response
            .json()
            .catch(() => ({}) as { error_code?: string });
          throw new Error(
            b.error_code
              ? translateApiError(b.error_code)
              : t("fileBrowser.errors.startFailed"),
          );
        }

        const data = await response.json();
        if (controller.signal.aborted) return;
        if (data.success && data.migration_id) {
          onStartSuccess(data.migration_id, false);
        } else {
          setError(
            data.error_code
              ? translateApiError(data.error_code)
              : t("fileBrowser.errors.startError"),
          );
        }
      }
    } catch (err) {
      if (controller.signal.aborted || isAbortError(err)) return;
      setError(
        err instanceof Error
          ? err.message
          : t("fileBrowser.errors.networkError"),
      );
    } finally {
      releaseRequestController(controller);
      if (!controller.signal.aborted) setStarting(false);
    }
  };

  const renderNode = (file: CloudFile, depth: number = 0) => {
    const isExpanded = !!expandedPaths[file.path];
    const isSelected = !!selectedPaths[file.path];
    const isLoading = !!loadingPaths[file.path];
    const children = directoryContents[file.path] || [];
    const isPersonalVault = isOneDrivePersonalVault(file);

    return (
      <div key={file.path} className="select-none font-sans text-xs">
        <SourceTreeRow
          file={file}
          depth={depth}
          isExpanded={isExpanded}
          isSelected={isSelected}
          isLoading={isLoading}
          isPersonalVault={isPersonalVault}
          isFocused={resolvedFocusedSourcePath === file.path}
          formattedSize={formatBytes(file.size)}
          expandLabel={t("common.expand", { name: file.name })}
          collapseLabel={t("common.collapse", { name: file.name })}
          selectLabel={`${t("common.select")} ${file.name}`}
          unavailableLabel={t("fileBrowser.personalVaultUnavailable")}
          treeItemRefs={sourceTreeItemRefs}
          onExpand={toggleExpand}
          onSelect={toggleSelect}
          onFocus={setFocusedSourcePath}
        />

        {/* Children (Recursion) */}
        {file.is_dir && isExpanded && children.length > 0 && (
          <div role="group" className="relative">
            {/* Visual connector left track */}
            <div className="absolute left-6.5 top-0 bottom-4.5 border-l border-[var(--color-border)]"></div>
            {children.map((child) => renderNode(child, depth + 1))}
          </div>
        )}

        {file.is_dir && isExpanded && children.length === 0 && !isLoading && (
          <div className="text-[10px] text-[var(--color-text-muted)] italic py-2.5 pl-14">
            {t("fileBrowser.emptyDir")}
          </div>
        )}
      </div>
    );
  };

  // Render target directory tree node recursively
  const renderTargetNode = (file: CloudFile, depth: number = 0) => {
    const isExpanded = !!targetExpandedPaths[file.path];
    const isSelected = targetDir === file.path;
    const isLoading = !!targetLoadingPaths[file.path];
    const children = targetDirectoryContents[file.path] || [];

    return (
      <div key={file.path} className="select-none font-sans text-xs">
        {/* Row */}
        <div
          ref={(element) => {
            if (element) targetTreeItemRefs.current.set(file.path, element);
            else targetTreeItemRefs.current.delete(file.path);
          }}
          role="treeitem"
          aria-level={depth + 1}
          aria-expanded={isExpanded}
          aria-selected={isSelected}
          tabIndex={resolvedFocusedTargetPath === file.path ? 0 : -1}
          onFocus={() => setFocusedTargetPath(file.path)}
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
            tabIndex={-1}
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
            tabIndex={-1}
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
          <div role="group" className="relative">
            <div className="absolute left-[20px] top-0 bottom-3 border-l border-[var(--color-border)]"></div>
            {children.length > 0 ? (
              children.map((child) => renderTargetNode(child, depth + 1))
            ) : isLoading ? null : (
              <div className="text-[10px] text-[var(--color-text-muted)] italic py-2 pl-[42px]">
                {t("fileBrowser.noSubdirs")}
              </div>
            )}
          </div>
        )}
      </div>
    );
  };
  return (
    <div className="w-full max-w-5xl mx-auto py-2 text-left space-y-6">
      {/* Wizard header */}
      <div className="flex items-center justify-between border-b border-[var(--color-border)]/50 pb-4">
        {onBack ? (
          <Button type="button" onClick={onBack}>
            <ArrowLeft className="w-4 h-4" />
            <span>{t("common.back")}</span>
          </Button>
        ) : (
          <span />
        )}
        <h1 className="font-display text-xl font-semibold leading-none text-[var(--color-text-primary)]">
          {t("fileBrowser.wizardStep")}
        </h1>
      </div>

      {/* Source & Target Connection Cards Grid */}
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
            <div className="font-extrabold text-sm text-[var(--color-text-primary)] capitalize flex items-center gap-2">
              <span>{credentials.source_provider || t("common.unspecified")}</span>
            </div>
            <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">
              {credentials.source_url || t("migrations.oauth")}
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
            {!isImmichTarget && (
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
              {credentials.target_provider || t("common.unspecified")}
            </div>
            <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">
              {credentials.target_url || t("migrations.oauth")}
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

      {/* Settings Strip */}
      <div className="ui-card">
        {/* Mode selector (left) + start button (right) */}
        <div className="flex flex-col justify-between gap-3 border-b border-[var(--color-border-light)] px-5 py-3 sm:flex-row sm:items-center sm:px-6">
          {/* Job Mode Selector */}
          <div className="w-full text-xs sm:w-auto">
            <div className="flex border-b border-[var(--color-border-light)]">
              <button
                type="button"
                onClick={() => setJobType("migration")}
                className={`px-3 py-2 text-sm font-medium transition-colors ${
                  effectiveJobType === "migration"
                    ? "border-b-2 border-[var(--color-text-primary)] text-[var(--color-text-primary)]"
                    : "text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                }`}
              >
                {t("sync.modeMigration")}
              </button>
              {!hasImmichEndpoint && (
                <button
                  type="button"
                  onClick={() => setJobType("sync")}
                  className={`px-3 py-2 text-sm font-medium transition-colors ${
                    effectiveJobType === "sync"
                      ? "border-b-2 border-[var(--color-text-primary)] text-[var(--color-text-primary)]"
                      : "text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                  }`}
                >
                  {t("sync.modeSync")}
                </button>
              )}
              {backupAvailable && (
                <button
                  type="button"
                  onClick={() => setJobType("backup")}
                  className={`px-3 py-2 text-sm font-medium transition-colors ${
                    effectiveJobType === "backup"
                      ? "border-b-2 border-[var(--color-text-primary)] text-[var(--color-text-primary)]"
                      : "text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                  }`}
                >
                  {t("backup.mode")}
                </button>
              )}
            </div>
          </div>

          {/* Sticky start button */}
          <button
            type="button"
            onClick={handleStartMigration}
            disabled={starting}
            className="ui-button-primary inline-flex w-full shrink-0 items-center justify-center gap-2 whitespace-nowrap px-4 py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50 sm:w-auto"
          >
            {starting ? (
              <>
                <RefreshCw className="w-4 h-4 animate-spin" />
                <span>{t("fileBrowser.indexing")}</span>
              </>
            ) : (
              <>
                <Play className="w-4 h-4 fill-current stroke-[2.5]" />
                <span>{effectiveJobType === "backup" ? t("backup.create") : t("fileBrowser.startTransfer")}</span>
              </>
            )}
          </button>
        </div>

        {/* Settings body */}
        <div className="p-5 sm:p-6">
          {effectiveJobType === "backup" ? (
            <BackupOptionsForm
              cronExpression={backupCronExpression}
              setCronExpression={setBackupCronExpression}
              timezone={backupTimezone}
              setTimezone={setBackupTimezone}
              retentionCount={backupRetentionCount}
              setRetentionCount={setBackupRetentionCount}
              threads={threads}
              setThreads={setThreads}
              error={error}
            />
          ) : (
            <SyncOptionsForm
              effectiveJobType={effectiveJobType}
              direction={direction}
              setDirection={setDirection}
              intervalMinutes={intervalMinutes}
              setIntervalMinutes={setIntervalMinutes}
              deletePropagation={deletePropagation}
              setDeletePropagation={setDeletePropagation}
              conflictStrategy={conflictStrategy}
              setConflictStrategy={setConflictStrategy}
              threads={threads}
              setThreads={setThreads}
              bandwidthLimit={bandwidthLimit}
              setBandwidthLimit={setBandwidthLimit}
              enableScheduling={enableScheduling}
              setEnableScheduling={setEnableScheduling}
              scheduledTime={scheduledTime}
              setScheduledTime={setScheduledTime}
              minScheduledTime={minScheduledTime}
              isImmichTarget={isImmichTarget}
              targetDir={targetDir}
              openTargetBrowser={openTargetBrowser}
              error={error}
            />
          )}
        </div>
      </div>

      {/* Ledger Browser Tree Card — full width */}
      <div className="ui-card flex flex-col p-5">
        {/* Tab Switcher */}
        <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-4 mb-4 gap-4">
          <div role="tablist" aria-label={t("fileBrowser.title")} onKeyDown={handleTabListKeyDown} className="flex bg-[var(--color-bg-tertiary)]/80 border border-[var(--color-border)]/20 p-1 rounded-lg flex-grow max-w-md">
            <button
              id="files-tab"
              type="button"
              role="tab"
              aria-selected={effectiveActiveTab === "files"}
              aria-controls={sourcePanelId}
              tabIndex={effectiveActiveTab === "files" ? 0 : -1}
              onClick={() => handleTabChange("files")}
              className={`flex-1 py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold uppercase tracking-wider transition-all duration-300 cursor-pointer focus:outline-none ${
                effectiveActiveTab === "files"
                  ? "bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)]"
                  : "text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]"
              }`}
            >
              {t("fileBrowser.files")} ({pathsToMigrate.length})
            </button>
            {effectiveJobType !== "backup" && supportsCalendars && (
              <button
                id="calendars-tab"
                type="button"
                role="tab"
                aria-selected={effectiveActiveTab === "calendars"}
                aria-controls={calendarsPanelId}
                tabIndex={effectiveActiveTab === "calendars" ? 0 : -1}
                onClick={() => handleTabChange("calendars")}
                className={`flex-1 py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold uppercase tracking-wider transition-all duration-300 cursor-pointer focus:outline-none ${
                  effectiveActiveTab === "calendars"
                    ? "bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)]"
                    : "text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]"
                }`}
              >
                {t("fileBrowser.calendars")} (
                {Object.values(selectedCalendars).filter(Boolean).length})
              </button>
            )}
            {effectiveJobType !== "backup" && supportsContacts && (
              <button
                id="contacts-tab"
                type="button"
                role="tab"
                aria-selected={effectiveActiveTab === "contacts"}
                aria-controls={contactsPanelId}
                tabIndex={effectiveActiveTab === "contacts" ? 0 : -1}
                onClick={() => handleTabChange("contacts")}
                className={`flex-1 py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold uppercase tracking-wider transition-all duration-300 cursor-pointer focus:outline-none ${
                  effectiveActiveTab === "contacts"
                    ? "bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)]"
                    : "text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]"
                }`}
              >
                {t("fileBrowser.contacts")} (
                {Object.values(selectedContacts).filter(Boolean).length})
              </button>
            )}
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

            {(() => {
              const isRefreshing =
                effectiveActiveTab === "files"
                  ? !!loadingPaths["/"]
                  : effectiveActiveTab === "calendars"
                    ? loadingCalendars
                    : loadingContacts;

              const handleRefresh = () => {
                if (effectiveActiveTab === "files") {
                  void refreshFiles();
                } else if (effectiveActiveTab === "calendars") {
                  void fetchCalendars(true);
                } else {
                  void fetchContacts(true);
                }
              };

              return (
                <button
                  type="button"
                  onClick={handleRefresh}
                  disabled={isRefreshing}
                  className="ui-button-secondary p-2.5 hover:bg-[var(--color-bg-tertiary)] disabled:opacity-50"
                  title={t("common.refresh")}
                  aria-label={t("common.refresh")}
                >
                  <RefreshCw
                    className={`w-4 h-4 ${isRefreshing ? "animate-spin" : ""}`}
                  />
                </button>
              );
            })()}
          </div>
        </div>

        <div
          id={effectiveActiveTab === "files" ? sourcePanelId : effectiveActiveTab === "calendars" ? calendarsPanelId : contactsPanelId}
          role="tabpanel"
          aria-labelledby={`${effectiveActiveTab}-tab`}
          tabIndex={0}
          className="flex-grow overflow-y-auto rounded-lg"
        >
          {effectiveActiveTab === "files" &&
            (directoryContents["/"]?.length > 0 ? (
              <div role="tree" aria-label={t("fileBrowser.files")} onKeyDown={handleSourceTreeKeyDown}>
                {directoryContents["/"].map((file) => renderNode(file, 0))}
              </div>
            ) : (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                <Folder className="w-10 h-10 text-[var(--color-text-muted)]" />
                <p className="font-mono text-xs italic text-[var(--color-text-muted)]">
                  {t("fileBrowser.noFiles")}
                </p>
              </div>
            ))}

          {effectiveActiveTab === "calendars" &&
            (loadingCalendars ? (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-3">
                <RefreshCw className="w-8 h-8 text-[var(--color-text-primary)] animate-spin" />
                <p className="font-mono text-xs italic">
                  {t("fileBrowser.loadingCalendars")}
                </p>
              </div>
            ) : calendars.length > 0 ? (
              <div className="space-y-2">
                {calendars.map((cal) => (
                  <button
                    type="button"
                    key={cal.path}
                    aria-pressed={!!selectedCalendars[cal.path]}
                    aria-label={`${t("common.select")} ${cal.name}`}
                    className={`flex w-full items-center gap-3.5 py-3 px-4 border rounded-lg cursor-pointer transition-colors duration-200 text-left ${
                      selectedCalendars[cal.path]
                        ? "bg-[var(--color-bg-tertiary)] border-[var(--color-text-primary)] font-semibold"
                        : "bg-[var(--color-bg-secondary)]/50 border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]/50 hover:border-[var(--color-border)]"
                    }`}
                    onClick={() =>
                      setSelectedCalendars((prev) => ({
                        ...prev,
                        [cal.path]: !prev[cal.path],
                      }))
                    }
                  >
                    <span className="flex items-center justify-center" aria-hidden="true">
                      <div
                        className={`w-5 h-5 border rounded-lg flex items-center justify-center transition-all duration-200 ${
                          selectedCalendars[cal.path]
                            ? "bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)] border-transparent"
                            : "bg-[var(--color-bg-secondary)] border-[var(--color-border)]"
                        }`}
                      >
                        {selectedCalendars[cal.path] && (
                          <Check className="w-3.5 h-3.5 text-[var(--color-text-inverse)] stroke-[3.5]" />
                        )}
                      </div>
                    </span>
                    <Calendar className="w-5 h-5 text-[var(--color-text-primary)]" />
                    <span className="text-[12px] text-[var(--color-text-secondary)] flex-grow text-left">
                      {cal.name}
                    </span>
                  </button>
                ))}
              </div>
            ) : (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                <Calendar className="w-10 h-10 text-[var(--color-text-muted)]" />
                <p className="font-mono text-xs italic">
                  {t("fileBrowser.noCalendars")}
                </p>
              </div>
            ))}

          {effectiveActiveTab === "contacts" &&
            (loadingContacts ? (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-3">
                <RefreshCw className="w-8 h-8 text-[var(--color-text-primary)] animate-spin" />
                <p className="font-mono text-[10px] italic">
                  {t("fileBrowser.loadingContacts")}
                </p>
              </div>
            ) : contacts.length > 0 ? (
              <div className="space-y-2">
                {contacts.map((addr) => (
                  <button
                    type="button"
                    key={addr.path}
                    aria-pressed={!!selectedContacts[addr.path]}
                    aria-label={`${t("common.select")} ${addr.name}`}
                    className={`flex w-full items-center gap-3.5 py-3 px-4 border rounded-lg cursor-pointer transition-colors duration-200 text-left ${
                      selectedContacts[addr.path]
                        ? "bg-[var(--color-bg-tertiary)] border-[var(--color-text-primary)] font-semibold"
                        : "bg-[var(--color-bg-secondary)]/50 border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]/50 hover:border-[var(--color-border)]"
                    }`}
                    onClick={() =>
                      setSelectedContacts((prev) => ({
                        ...prev,
                        [addr.path]: !prev[addr.path],
                      }))
                    }
                  >
                    <span className="flex items-center justify-center" aria-hidden="true">
                      <div
                        className={`w-5 h-5 border rounded-lg flex items-center justify-center transition-all duration-200 ${
                          selectedContacts[addr.path]
                            ? "bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)] border-transparent"
                            : "bg-[var(--color-bg-secondary)] border-[var(--color-border)]"
                        }`}
                      >
                        {selectedContacts[addr.path] && (
                          <Check className="w-3.5 h-3.5 text-[var(--color-text-inverse)] stroke-[3.5]" />
                        )}
                      </div>
                    </span>
                    <BookOpen className="w-5 h-5 text-[var(--color-text-primary)]" />
                    <span className="text-[12px] text-[var(--color-text-secondary)] flex-grow text-left">
                      {addr.name}
                    </span>
                  </button>
                ))}
              </div>
            ) : (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                <BookOpen className="w-10 h-10 text-[var(--color-text-muted)]" />
                <p className="font-mono text-xs italic">
                  {t("fileBrowser.noContacts")}
                </p>
              </div>
            ))}
        </div>
      </div>

      {/* Target Directory Browser Modal.
          Immich targets are library-only (targetDir fixed to "/"), so this modal
          is unreachable for them — the open button is also guarded by
          !isImmichTarget. This second guard is defense-in-depth: keep it so the
          modal can never render an album/library picker for Immich. */}
      {isTargetBrowserOpen && !isImmichTarget && (
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
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => void fetchTargetChildren("/", 0, true)}
                  className="ui-button-secondary p-1.5 hover:bg-[var(--color-bg-tertiary)]"
                  aria-label={t("common.refresh")}
                  title={t("common.refresh")}
                >
                  <RefreshCw className="w-4 h-4" />
                </button>
                <button
                  type="button"
                  ref={targetCloseButtonRef}
                  onClick={closeTargetBrowser}
                  className="ui-button-secondary p-1.5 hover:bg-[var(--color-bg-tertiary)]"
                  aria-label={t("paths.close")}
                >
                  <X className="w-5 h-5" />
                </button>
              </div>
            </div>

            {/* Modal Content - Directory Tree */}
            <div className="p-5 flex-grow overflow-y-auto min-h-[300px]">
              {targetError && (
                <div className="ui-alert ui-alert-error mb-4 p-3 text-xs flex gap-2">
                  <AlertTriangle className="w-4 h-4 shrink-0" />
                  <span>{targetError}</span>
                </div>
              )}

              <div role="tree" aria-label={t("fileBrowser.targetSelectTitle")} onKeyDown={handleTargetTreeKeyDown} className="border border-[var(--color-border)]/60 rounded-lg bg-[var(--color-bg-tertiary)]/30 p-2 overflow-x-auto max-h-[350px]">
                {/* Root Directory Node */}
                <div className="select-none font-sans text-xs">
                  <div
                    ref={(element) => {
                      if (element) targetTreeItemRefs.current.set("/", element);
                      else targetTreeItemRefs.current.delete("/");
                    }}
                    role="treeitem"
                    aria-level={1}
                    aria-expanded={!!targetExpandedPaths["/"]}
                    aria-selected={targetDir === "/"}
                    tabIndex={resolvedFocusedTargetPath === "/" ? 0 : -1}
                    onFocus={() => setFocusedTargetPath("/")}
                    className={`flex items-center gap-2.5 py-2 px-3 border border-transparent hover:bg-[var(--color-bg-tertiary)]/50 transition-colors duration-150 rounded-xl ${
                      targetDir === "/"
                        ? "bg-[var(--color-bg-secondary)] font-bold border-[var(--color-border)] text-[var(--color-text-primary)]"
                        : ""
                    }`}
                  >
                    <button
                      type="button"
                      tabIndex={-1}
                      className="w-4 h-4 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors cursor-pointer"
                      onClick={() => {
                        const isExpanded = !!targetExpandedPaths["/"];
                        setTargetExpandedPaths((prev) => ({
                          ...prev,
                          "/": !isExpanded,
                        }));
                        if (!isExpanded) fetchTargetChildren("/");
                      }}
                      aria-label={
                        targetExpandedPaths["/"]
                          ? t("common.collapse", {
                              name: t("fileBrowser.mainDir"),
                            })
                          : t("common.expand", {
                              name: t("fileBrowser.mainDir"),
                            })
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
                      {/* Icon */}
                      <span className="shrink-0">
                        {targetExpandedPaths["/"] ? (
                          <FolderOpen className="w-4 h-4 text-[var(--color-text-secondary)]" />
                        ) : (
                          <Folder className="w-4 h-4 text-[var(--color-text-secondary)]" />
                        )}
                      </span>
                    </span>
                    <button
                      type="button"
                      tabIndex={-1}
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
                    <div role="group" className="relative">
                      {/* Tree visual line */}
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
                  handleCreateTargetFolder(targetDir);
                }}
                className="p-4 border-t border-[var(--color-border-light)] bg-[var(--color-bg-tertiary)]/50 flex items-center gap-3 text-left"
              >
                <div className="flex-grow space-y-1">
                  <label className="block text-xs font-bold font-mono text-[var(--color-text-muted)] uppercase tracking-wider">
                    {t("fileBrowser.mkdirIn", { path: targetDir })}
                  </label>
                  <input
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
                    disabled={!newFolderName.trim()}
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
    </div>
  );
};
