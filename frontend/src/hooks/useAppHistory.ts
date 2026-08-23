import { useCallback, useEffect, useState } from 'react';

export type AppStep =
  | 'login'
  | 'history'
  | 'connect'
  | 'select'
  | 'dashboard'
  | 'settings'
  | 'admin'
  | 'reset-password'
  | 'confirm-email'
  | 'syncdetail'
  | 'backupdetail'
  | 'files';

type NavigationState = {
  step: AppStep;
  migrationId: string;
  syncId: string;
  backupId: string;
  profileId: string;
  settingsTab: string;
};

type HistoryEntry = {
  step?: AppStep;
  migration?: string;
  sync?: string;
  backup?: string;
  profile?: string;
  tab?: string;
};

type UseAppHistoryOptions = {
  resetToken: string | null;
  emailChangeToken: string | null;
  hasStoredSession: boolean;
  onLeaveCreationFlow?: () => void;
};

function stateForStep(step: AppStep, id = ''): NavigationState {
  if (step === 'dashboard') {
    return { step, migrationId: id, syncId: '', backupId: '', profileId: '', settingsTab: '' };
  }
  if (step === 'syncdetail') {
    return { step, migrationId: '', syncId: id, backupId: '', profileId: '', settingsTab: '' };
  }
  if (step === 'backupdetail') {
    return { step, migrationId: '', syncId: '', backupId: id, profileId: '', settingsTab: '' };
  }
  if (step === 'files') {
    return { step, migrationId: '', syncId: '', backupId: '', profileId: id, settingsTab: '' };
  }
  if (step === 'settings') {
    return { step, migrationId: '', syncId: '', backupId: '', profileId: '', settingsTab: id || 'account' };
  }
  return { step, migrationId: '', syncId: '', backupId: '', profileId: '', settingsTab: '' };
}

export function initialNavigationFor(
  search: string,
  { resetToken, emailChangeToken, hasStoredSession }: UseAppHistoryOptions,
): NavigationState {
  if (emailChangeToken) return stateForStep('confirm-email');
  if (resetToken) return stateForStep('reset-password');
  if (!hasStoredSession) return stateForStep('login');

  const params = new URLSearchParams(search);
  const migrationId = params.get('migration') ?? '';
  const syncId = params.get('sync') ?? '';
  const backupId = params.get('backup') ?? '';
  const profileId = params.get('profile') ?? '';
  const tab = params.get('tab') ?? '';
  if (params.get('view') === 'files') return stateForStep('files', profileId);
  if (params.get('view') === 'settings') return stateForStep('settings', tab);
  if (migrationId) return stateForStep('dashboard', migrationId);
  if (syncId) return stateForStep('syncdetail', syncId);
  if (backupId) return stateForStep('backupdetail', backupId);
  return stateForStep('history');
}

function navigationFromHistory(entry: HistoryEntry, search: string): NavigationState {
  const params = new URLSearchParams(search);
  if (entry.step === 'dashboard') {
    return stateForStep('dashboard', entry.migration ?? params.get('migration') ?? '');
  }
  if (entry.step === 'syncdetail') {
    return stateForStep('syncdetail', entry.sync ?? params.get('sync') ?? '');
  }
  if (entry.step === 'backupdetail') {
    return stateForStep('backupdetail', entry.backup ?? params.get('backup') ?? '');
  }
  if (entry.step === 'files') {
    return stateForStep('files', entry.profile ?? params.get('profile') ?? '');
  }
  if (entry.step === 'settings') {
    return stateForStep('settings', entry.tab ?? params.get('tab') ?? '');
  }
  return stateForStep(entry.step ?? 'login');
}

function writeHistory(nextStep: AppStep, id: string, replace: boolean): void {
  const url = new URL(window.location.href);
  const state: HistoryEntry = { step: nextStep };
  url.searchParams.delete('migration');
  url.searchParams.delete('sync');
  url.searchParams.delete('backup');
  url.searchParams.delete('view');
  url.searchParams.delete('profile');
  url.searchParams.delete('tab');

  if (nextStep === 'dashboard' && id) {
    url.searchParams.set('migration', id);
    state.migration = id;
  } else if (nextStep === 'syncdetail' && id) {
    url.searchParams.set('sync', id);
    state.sync = id;
  } else if (nextStep === 'backupdetail' && id) {
    url.searchParams.set('backup', id);
    state.backup = id;
  } else if (nextStep === 'files') {
    url.searchParams.set('view', 'files');
    if (id) {
      url.searchParams.set('profile', id);
      state.profile = id;
    }
  } else if (nextStep === 'settings') {
    if (id) {
      state.tab = id;
    }
  }

  if (replace) {
    window.history.replaceState(state, '', url.toString());
  } else {
    window.history.pushState(state, '', url.toString());
  }
}

function leavesCreationFlow(step: AppStep): boolean {
  return step !== 'dashboard' && step !== 'select';
}

export function useAppHistory(options: UseAppHistoryOptions) {
  const [initialOptions] = useState<UseAppHistoryOptions>(() => options);
  const [initialNavigation] = useState<NavigationState>(() => (
    initialNavigationFor(window.location.search, initialOptions)
  ));
  const [navigation, setNavigation] = useState<NavigationState>(initialNavigation);

  const applyHistory = useCallback((nextStep: AppStep, id: string, replace: boolean) => {
    const nextNavigation = stateForStep(nextStep, id);
    if (leavesCreationFlow(nextStep)) initialOptions.onLeaveCreationFlow?.();
    writeHistory(nextStep, id, replace);
    setNavigation(nextNavigation);
  }, [initialOptions]);

  // Seed the existing entry so browser history and React state start from one
  // source of truth. This preserves authenticated migration, sync, and backup deep links
  // until silent session validation confirms them.
  useEffect(() => {
    const { step, migrationId, syncId, backupId, profileId, settingsTab } = initialNavigation;
    writeHistory(step, migrationId || syncId || backupId || profileId || settingsTab, true);
  }, [initialNavigation]);

  useEffect(() => {
    const onPopState = (event: PopStateEvent) => {
      const entry = event.state as HistoryEntry | null;
      if (entry?.step) {
        const nextNavigation = navigationFromHistory(entry, window.location.search);
        if (leavesCreationFlow(nextNavigation.step)) initialOptions.onLeaveCreationFlow?.();
        setNavigation(nextNavigation);
        return;
      }
      const nextNavigation = initialNavigationFor(window.location.search, initialOptions);
      if (leavesCreationFlow(nextNavigation.step)) initialOptions.onLeaveCreationFlow?.();
      setNavigation(nextNavigation);
    };

    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  }, [initialOptions]);

  const replaceNav = useCallback(
    (nextStep: AppStep, id = '') => applyHistory(nextStep, id, true),
    [applyHistory],
  );

  const navigate = useCallback(
    (nextStep: AppStep, id?: string) => {
      const activeId = nextStep === 'dashboard'
        ? navigation.migrationId
        : nextStep === 'syncdetail'
        ? navigation.syncId
        : nextStep === 'backupdetail'
        ? navigation.backupId
        : nextStep === 'files'
        ? navigation.profileId
        : nextStep === 'settings'
        ? (id ?? navigation.settingsTab)
        : '';
      applyHistory(nextStep, id ?? activeId, false);
    },
    [applyHistory, navigation.backupId, navigation.migrationId, navigation.profileId, navigation.settingsTab, navigation.syncId],
  );

  const goToOverview = useCallback(() => {
    replaceNav('history');
  }, [replaceNav]);

  const goBack = useCallback(() => {
    window.history.back();
  }, []);

  return {
    ...navigation,
    initialMigrationId: initialNavigation.migrationId,
    initialSyncId: initialNavigation.syncId,
    initialBackupId: initialNavigation.backupId,
    initialProfileId: initialNavigation.profileId,
    replaceNav,
    navigate,
    goToOverview,
    goBack,
  };
}

