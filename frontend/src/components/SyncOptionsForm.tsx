import React from "react";
import {
  CheckIcon as Check,
  FolderIcon as Folder,
  FolderOpenIcon as FolderOpen,
  InformationCircleIcon as Info,
  ExclamationTriangleIcon as AlertTriangle,
  CalendarDaysIcon as Calendar,
} from "./icons";
import { useTranslation } from "react-i18next";
import {
  BANDWIDTH_OPTIONS,
  valueToBandwidthIndex,
  bandwidthIndexToValue,
  getBandwidthLabel,
} from "../utils/bandwidth";

export interface SyncOptionsFormProps {
  effectiveJobType: "migration" | "sync";
  direction?: "one_way" | "two_way";
  setDirection?: (dir: "one_way" | "two_way") => void;
  intervalMinutes?: number;
  setIntervalMinutes?: (minutes: number) => void;
  deletePropagation?: boolean;
  setDeletePropagation?: (deleteProp: boolean) => void;
  conflictStrategy: string;
  setConflictStrategy: (strategy: string) => void;
  threads?: number;
  setThreads?: (threads: number) => void;
  bandwidthLimit: number;
  setBandwidthLimit: (limit: number) => void;
  enableScheduling?: boolean;
  setEnableScheduling?: (enable: boolean) => void;
  scheduledTime?: string;
  setScheduledTime?: (time: string) => void;
  minScheduledTime?: string;
  isImmichTarget: boolean;
  targetDir?: string;
  openTargetBrowser?: () => void;
  error?: string | null;
  hideTargetDirConfig?: boolean;
}

export const SyncOptionsForm: React.FC<SyncOptionsFormProps> = ({
  effectiveJobType,
  direction = "one_way",
  setDirection,
  intervalMinutes = 15,
  setIntervalMinutes,
  deletePropagation = false,
  setDeletePropagation,
  conflictStrategy,
  setConflictStrategy,
  threads = 4,
  setThreads,
  bandwidthLimit,
  setBandwidthLimit,
  enableScheduling = false,
  setEnableScheduling,
  scheduledTime = "",
  setScheduledTime,
  minScheduledTime,
  isImmichTarget,
  targetDir = "/",
  openTargetBrowser,
  error,
  hideTargetDirConfig = false,
}) => {
  const { t } = useTranslation();

  return (
    <div className="space-y-6">
      {/* Sync-only options: direction, interval, delete propagation */}
      {effectiveJobType === "sync" && (
        <div className="ui-alert ui-alert-info space-y-4 p-4 text-xs">
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
            {/* Direction */}
            {setDirection && (
              <div className="space-y-2">
                <label className="block text-[10px] font-bold text-[var(--color-text-primary)] uppercase tracking-widest font-mono">
                  {t("sync.direction")}
                </label>
                <div className="grid grid-cols-2 gap-2">
                  <button
                    type="button"
                    onClick={() => setDirection("one_way")}
                    aria-pressed={direction === "one_way"}
                    className={`py-2 px-2.5 text-[11px] font-bold font-mono transition-all cursor-pointer ${
                      direction === "one_way"
                        ? "ui-button-primary"
                        : "ui-button-secondary"
                    }`}
                  >
                    {t("sync.oneWay")} (→)
                  </button>
                  <button
                    type="button"
                    onClick={() => setDirection("two_way")}
                    aria-pressed={direction === "two_way"}
                    className={`py-2 px-2.5 text-[11px] font-bold font-mono transition-all cursor-pointer ${
                      direction === "two_way"
                        ? "ui-button-primary"
                        : "ui-button-secondary"
                    }`}
                  >
                    {t("sync.twoWay")} (↔)
                  </button>
                </div>
              </div>
            )}

            {/* Interval */}
            {setIntervalMinutes && (
              <div className="space-y-1">
                <label className="block text-[10px] font-bold text-[var(--color-text-primary)] uppercase tracking-widest font-mono">
                  {t("sync.interval")}
                </label>
                <select
                  value={intervalMinutes}
                  onChange={(e) =>
                    setIntervalMinutes(parseInt(e.target.value, 10))
                  }
                  className="ui-select w-full py-2 px-3 text-xs font-mono"
                >
                  <option value={5}>5 {t("sync.minutes")}</option>
                  <option value={15}>15 {t("sync.minutes")}</option>
                  <option value={30}>30 {t("sync.minutes")}</option>
                  <option value={60}>1 {t("sync.hour")}</option>
                  <option value={360}>6 {t("sync.hours")}</option>
                  <option value={1440}>24 {t("sync.hours")}</option>
                </select>
              </div>
            )}

            {/* Delete propagation */}
            {setDeletePropagation && (
              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  id="deletePropagation"
                  checked={deletePropagation}
                  onChange={(e) => setDeletePropagation(e.target.checked)}
                  className="rounded accent-[var(--color-text-primary)] cursor-pointer"
                />
                <div className="flex flex-col">
                  <label
                    htmlFor="deletePropagation"
                    className="text-[11px] font-bold text-[var(--color-text-primary)] cursor-pointer"
                  >
                    {t("sync.deletePropagation")}
                  </label>
                  <span className="text-[10px] text-[var(--color-text-secondary)]">
                    {t("sync.deletePropagationHelp")}
                  </span>
                </div>
              </div>
            )}
          </div>
        </div>
      )}

      {/* Common + mode-specific settings grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-6 items-start">
        {/* Target folder summary (optional if hideTargetDirConfig is false) */}
        {!hideTargetDirConfig && (
          <div className="space-y-2 text-xs md:col-span-2 xl:col-span-1">
            <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
              {t("fileBrowser.targetDir")}
            </label>
            <div className="flex items-center gap-2">
              <span className="flex-grow flex items-center gap-2 px-3 py-2.5 bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-xl font-mono text-xs text-[var(--color-text-secondary)] truncate">
                <Folder className="w-3.5 h-3.5 text-[var(--color-text-secondary)] shrink-0" />
                <span className="truncate">{targetDir || "/"}</span>
              </span>
              {!isImmichTarget && openTargetBrowser && (
                <button
                  type="button"
                  onClick={openTargetBrowser}
                  className="ui-button-secondary py-2 px-3 text-xs flex items-center gap-1.5 shrink-0"
                >
                  <FolderOpen className="w-3.5 h-3.5" />
                  <span>{t("fileBrowser.selectFolder")}</span>
                </button>
              )}
            </div>
            <p className="text-xs text-[var(--color-text-muted)] leading-relaxed font-sans">
              {t("fileBrowser.targetCopied")}
            </p>
          </div>
        )}

        {/* Conflict Strategy */}
        {isImmichTarget ? (
          <div className="ui-alert ui-alert-info p-3.5 text-xs font-mono flex items-center gap-2 xl:col-span-2">
            <Info className="w-4 h-4 shrink-0" />
            <span>{t("fileBrowser.immichDuplicateDetection")}</span>
          </div>
        ) : effectiveJobType === "sync" && direction === "one_way" ? (
          <div className="ui-alert ui-alert-info self-center p-3.5 text-xs font-mono flex items-center gap-2 xl:col-span-2">
            <Info className="w-4 h-4 shrink-0" />
            <span>{t("sync.oneWayConflictNote")}</span>
          </div>
        ) : (
          <div className="space-y-3 text-xs xl:col-span-2">
            <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
              {t("fileBrowser.conflictHandling")}
            </label>
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              {/* OVERWRITE card */}
              <button
                type="button"
                onClick={() => setConflictStrategy("OVERWRITE")}
                aria-pressed={conflictStrategy === "OVERWRITE"}
                className={`w-full text-left p-3.5 rounded-lg border transition-all duration-200 cursor-pointer ${
                  conflictStrategy === "OVERWRITE"
                    ? "bg-[var(--color-bg-tertiary)]/50 border-[var(--color-text-primary)] text-[var(--color-text-primary)] font-bold"
                    : "bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]/30"
                }`}
              >
                <div className="flex items-center justify-between text-xs font-semibold">
                  <span className="font-display">
                    {effectiveJobType === "sync"
                      ? t("sync.conflictSourceWins")
                      : t("fileBrowser.overwrite")}
                  </span>
                  {conflictStrategy === "OVERWRITE" && (
                    <Check className="w-4 h-4 text-[var(--color-text-primary)] stroke-[3]" />
                  )}
                </div>
                <p
                  className={`text-xs mt-1 leading-normal font-normal ${conflictStrategy === "OVERWRITE" ? "text-[var(--color-text-secondary)]" : "text-[var(--color-text-muted)]"}`}
                >
                  {t("fileBrowser.overwriteDesc")}
                </p>
              </button>

              {/* RENAME card */}
              <button
                type="button"
                onClick={() => setConflictStrategy("RENAME")}
                aria-pressed={conflictStrategy === "RENAME"}
                className={`w-full text-left p-3.5 rounded-lg border transition-all duration-200 cursor-pointer ${
                  conflictStrategy === "RENAME"
                    ? "bg-[var(--color-bg-tertiary)]/50 border-[var(--color-text-primary)] text-[var(--color-text-primary)] font-bold"
                    : "bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]/30"
                }`}
              >
                <div className="flex items-center justify-between text-xs font-semibold">
                  <span className="font-display">
                    {effectiveJobType === "sync"
                      ? t("sync.conflictKeepBoth")
                      : t("fileBrowser.rename")}
                  </span>
                  {conflictStrategy === "RENAME" && (
                    <Check className="w-4 h-4 text-[var(--color-text-primary)] stroke-[3]" />
                  )}
                </div>
                <p
                  className={`text-xs mt-1 leading-normal font-normal ${conflictStrategy === "RENAME" ? "text-[var(--color-text-secondary)]" : "text-[var(--color-text-muted)]"}`}
                >
                  {t("fileBrowser.renameDesc")}
                </p>
              </button>

              {/* SKIP card */}
              <button
                type="button"
                onClick={() => setConflictStrategy("SKIP")}
                aria-pressed={conflictStrategy === "SKIP"}
                className={`w-full text-left p-3.5 rounded-lg border transition-all duration-200 cursor-pointer ${
                  conflictStrategy === "SKIP"
                    ? "bg-[var(--color-bg-tertiary)]/50 border-[var(--color-text-primary)] text-[var(--color-text-primary)] font-bold"
                    : "bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]/30"
                }`}
              >
                <div className="flex items-center justify-between text-xs font-semibold">
                  <span className="font-display">
                    {effectiveJobType === "sync"
                      ? t("sync.conflictSkip")
                      : t("fileBrowser.skip")}
                  </span>
                  {conflictStrategy === "SKIP" && (
                    <Check className="w-4 h-4 text-[var(--color-text-primary)] stroke-[3]" />
                  )}
                </div>
                <p
                  className={`text-xs mt-1 leading-normal font-normal ${conflictStrategy === "SKIP" ? "text-[var(--color-text-secondary)]" : "text-[var(--color-text-muted)]"}`}
                >
                  {t("fileBrowser.skipDesc")}
                </p>
              </button>
            </div>
          </div>
        )}

        {/* Thread count selector (optional) */}
        {setThreads && (
          <div className="space-y-3 text-xs">
            <label htmlFor="sync-threads" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">
              {t("fileBrowser.threads")}
            </label>
            <div className="flex items-center gap-4">
              <input
                id="sync-threads"
                type="range"
                min="1"
                max={16}
                value={threads}
                onChange={(e) => setThreads(parseInt(e.target.value, 10))}
                className="flex-grow accent-[var(--color-text-primary)] cursor-pointer"
              />
              <span
                className={`font-mono text-xs font-bold px-2.5 py-1 rounded-lg min-w-[32px] text-center transition-colors ${
                  threads > 8
                    ? "bg-[var(--color-warning-bg)] text-[var(--color-text-primary)]"
                    : "bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)]"
                }`}
              >
                {threads}
              </span>
            </div>
            <p className="text-xs text-[var(--color-text-muted)] leading-relaxed font-sans">
              {threads > 8 ? (
                <span className="text-[var(--color-text-primary)] font-semibold">
                  {t("fileBrowser.threadsHighWarn")}
                </span>
              ) : (
                t("fileBrowser.threadsHint")
              )}
            </p>
          </div>
        )}

        {/* Bandwidth limit */}
        <div className="space-y-3 text-xs">
          <label htmlFor="sync-bandwidth" className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-3">
            {t("fileBrowser.bandwidth")}
          </label>
          <div className="flex items-center gap-4">
            <input
              id="sync-bandwidth"
              type="range"
              min="0"
              max={BANDWIDTH_OPTIONS.length - 1}
              step="1"
              value={valueToBandwidthIndex(bandwidthLimit)}
              onChange={(e) => {
                const idx = parseInt(e.target.value, 10);
                setBandwidthLimit(bandwidthIndexToValue(idx));
              }}
              className="flex-grow accent-[var(--color-text-primary)] cursor-pointer"
            />
            <span className="font-mono text-xs font-bold px-2.5 py-1 rounded-lg min-w-[70px] text-center bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)]">
              {getBandwidthLabel(bandwidthLimit, t("dashboard.unlimited"))}
            </span>
          </div>
          <p className="text-xs text-[var(--color-text-muted)] mt-2 leading-relaxed font-sans">
            {bandwidthLimit === 0
              ? t("fileBrowser.bandwidthUnlimited")
              : t("fileBrowser.bandwidthHint", {
                  limit: getBandwidthLabel(
                    bandwidthLimit,
                    t("dashboard.unlimited"),
                  ),
                })}
          </p>
        </div>
      </div>

      {/* Migration-only scheduling */}
      {effectiveJobType === "migration" && setEnableScheduling && (
        <div className="space-y-3 text-xs pt-5 border-t border-[var(--color-border-light)]">
          <label className="flex items-center gap-3 cursor-pointer group">
            <input
              type="checkbox"
              checked={enableScheduling}
              onChange={(e) => setEnableScheduling(e.target.checked)}
              className="w-4 h-4 rounded border-[var(--color-border)] accent-[var(--color-text-primary)] cursor-pointer"
            />
            <div className="flex items-center gap-2">
              <Calendar className="w-4 h-4 text-[var(--color-text-muted)] group-hover:text-[var(--color-text-primary)] transition-colors" />
              <span className="text-xs font-semibold text-[var(--color-text-primary)]">
                {t("fileBrowser.schedule")}
              </span>
            </div>
          </label>

          {enableScheduling && setScheduledTime && (
            <div className="mt-3 sm:max-w-sm">
              <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">
                {t("fileBrowser.scheduleTime")}
              </label>
              <input
                type="datetime-local"
                value={scheduledTime}
                onChange={(e) => setScheduledTime(e.target.value)}
                min={minScheduledTime}
                className="ui-input w-full py-2.5 px-4 text-sm transition-all font-sans"
              />
              <p className="text-xs text-[var(--color-text-muted)] mt-2 leading-relaxed font-sans">
                {t("fileBrowser.scheduleHint")}
              </p>
            </div>
          )}
        </div>
      )}

      {error && (
        <div role="alert" className="ui-alert ui-alert-error p-4 text-[11px] font-semibold leading-normal flex gap-2 text-left">
          <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
          <span>{error}</span>
        </div>
      )}
    </div>
  );
};
