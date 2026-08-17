import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import { initialNavigationFor, useAppHistory } from './useAppHistory';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

type HistoryOptions = {
  resetToken: string | null;
  emailChangeToken: string | null;
  hasStoredSession: boolean;
};

function HistoryHarness({ options }: { options: HistoryOptions }) {
  const { step, migrationId, syncId, profileId, settingsTab } = useAppHistory(options);
  return <output data-migration={migrationId} data-profile={profileId} data-settings-tab={settingsTab} data-step={step} data-sync={syncId} />;
}

describe('useAppHistory', () => {
  let container: HTMLDivElement;
  let root: Root;
  const originalPath = `${window.location.pathname}${window.location.search}`;

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    window.history.replaceState({}, '', originalPath);
  });

  it('preserves a stored-session sync deep link while seeding browser history', async () => {
    window.history.replaceState({}, '', '/?sync=sync-123');
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);

    await act(async () => {
      root.render(<HistoryHarness options={{ resetToken: null, emailChangeToken: null, hasStoredSession: true }} />);
      await Promise.resolve();
    });

    expect(container.querySelector('output')?.dataset).toMatchObject({ step: 'syncdetail', sync: 'sync-123' });
    expect(new URLSearchParams(window.location.search).get('sync')).toBe('sync-123');
    expect(new URLSearchParams(window.location.search).get('migration')).toBeNull();
  });

  it('keeps reset and email-confirmation routes ahead of stored deep links', () => {
    expect(initialNavigationFor('?sync=sync-123', {
      resetToken: 'reset-token',
      emailChangeToken: 'email-token',
      hasStoredSession: true,
    })).toMatchObject({ step: 'confirm-email', migrationId: '', syncId: '' });
  });

  it('restores the file manager and its selected profile from the URL', () => {
    expect(initialNavigationFor('?view=files&profile=profile-123', {
      resetToken: null,
      emailChangeToken: null,
      hasStoredSession: true,
    })).toMatchObject({ step: 'files', migrationId: '', syncId: '', profileId: 'profile-123' });
  });

  it('restores the settings view and specific tab from the URL', () => {
    expect(initialNavigationFor('?view=settings&tab=connections', {
      resetToken: null,
      emailChangeToken: null,
      hasStoredSession: true,
    })).toMatchObject({ step: 'settings', migrationId: '', syncId: '', profileId: '', settingsTab: 'connections' });
  });
});
