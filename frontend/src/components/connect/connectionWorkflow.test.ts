import { beforeEach, describe, expect, it, vi } from 'vitest';
import { apiFetch } from '../../utils/apiClient';
import {
  createConnectionDraft,
  createConnectionWorkflow,
  resolveConnection,
  validateConnection,
  type ConnectionDraft,
} from './connectionWorkflow';

vi.mock('../../utils/apiClient', async () => {
  const actual = await vi.importActual<typeof import('../../utils/apiClient')>('../../utils/apiClient');
  return { ...actual, apiFetch: vi.fn() };
});

const VALID_SFTP_KEY = 'SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU';

function draft(overrides: Partial<ConnectionDraft> = {}): ConnectionDraft {
  return { ...createConnectionDraft(), ...overrides };
}

describe('resolveConnection', () => {
  it('passes URL and credentials through for WebDAV-style providers', () => {
    expect(resolveConnection(draft({ provider: 'nextcloud', url: 'https://nc.example.com', username: 'alice', password: 'pw' })))
      .toEqual({ url: 'https://nc.example.com', username: 'alice', password: 'pw' });
  });

  it('sends nothing but the profile id when a saved profile is selected', () => {
    expect(resolveConnection(draft({ provider: 'nextcloud', profileId: 'prof-1', url: 'https://leak', username: 'leak', password: 'leak' })))
      .toEqual({ url: '', username: '', password: '' });
  });

  it('builds provider URLs from structured fields', () => {
    expect(resolveConnection(draft({ provider: 'smb', smb: { host: 'nas', port: '445', share: '/data', domain: 'WG' }, username: 'u', password: 'p' })).url)
      .toBe('smb://nas:445/data?domain=WG');
    expect(resolveConnection(draft({ provider: 's3', s3: { bucket: 'b', region: 'eu-1', endpoint: 'https://s3.example.com' } })).url)
      .toBe('s3://b?region=eu-1&endpoint=https%3A%2F%2Fs3.example.com');
    expect(resolveConnection(draft({ provider: 'sftp', sftp: { host: 'h', port: '22', hostKey: ` ${VALID_SFTP_KEY} `, authMode: 'password', privateKey: '' } })).url)
      .toBe(`sftp://h:22?host_key=${encodeURIComponent(VALID_SFTP_KEY)}`);
    expect(resolveConnection(draft({ provider: 'ftp', ftp: { host: 'ftp.example.com', port: '990', tlsMode: 'implicit' } })).url)
      .toBe('ftps://ftp.example.com:990');
  });

  it('uses the SSH private key as the secret in SFTP key mode', () => {
    const resolved = resolveConnection(draft({
      provider: 'sftp', username: 'root', password: 'ignored',
      sftp: { host: 'h', port: '22', hostKey: VALID_SFTP_KEY, authMode: 'key', privateKey: 'PEM' },
    }));
    expect(resolved.password).toBe('PEM');
  });

  it('resolves OAuth providers to their API endpoint and account name', () => {
    expect(resolveConnection(draft({ provider: 'dropbox', password: 'tok', oauthUser: 'me@dropbox.test' })))
      .toEqual({ url: 'https://api.dropbox.com', username: 'me@dropbox.test', password: 'tok' });
    expect(resolveConnection(draft({ provider: 'box', password: 'tok', oauthUser: 'ada@box.test' })))
      .toEqual({ url: 'https://api.box.com', username: 'ada@box.test', password: 'tok' });
    expect(resolveConnection(draft({ provider: 'onedrive', password: 'tok' })))
      .toEqual({ url: 'oauth://onedrive', username: 'onedrive', password: 'tok' });
  });

  it('omits URL for providers with a fixed endpoint and credentials for local storage', () => {
    expect(resolveConnection(draft({ provider: 'koofr', username: 'u', password: 'p' })).url).toBe('');
    expect(resolveConnection(draft({ provider: 'local', username: 'u', password: 'p' })))
      .toEqual({ url: '', username: '', password: '' });
  });
});

describe('validateConnection', () => {
  it('accepts a saved profile without inspecting ad-hoc fields', () => {
    expect(validateConnection('source', draft({ provider: 'sftp', profileId: 'p1' }))).toBeNull();
  });

  it('reports missing generic fields', () => {
    expect(validateConnection('source', draft({ provider: 'nextcloud', url: 'https://x', username: 'u' }))).toBe('connect.errors.missingFields');
    expect(validateConnection('target', draft({ provider: 'nextcloud', url: 'https://x', username: 'u', password: 'p' }))).toBeNull();
  });

  it('does not require a username for Immich or any credentials for local storage', () => {
    expect(validateConnection('source', draft({ provider: 'immich', url: 'https://i', password: 'key' }))).toBeNull();
    expect(validateConnection('target', draft({ provider: 'local' }))).toBeNull();
  });

  it('enforces SFTP host, pinned host key and key material per role', () => {
    const sftp = (patch: Partial<ConnectionDraft['sftp']>) => draft({
      provider: 'sftp', username: 'u', password: 'p',
      sftp: { host: 'h', port: '22', hostKey: VALID_SFTP_KEY, authMode: 'password', privateKey: '', ...patch },
    });
    expect(validateConnection('source', sftp({ host: ' ' }))).toBe('connect.errors.sourceSftpHost');
    expect(validateConnection('target', sftp({ hostKey: 'SHA256:short' }))).toBe('connect.errors.targetSftpHostKey');
    expect(validateConnection('source', sftp({ authMode: 'key', privateKey: ' ' }))).toBe('connect.errors.sourceSftpKey');
    expect(validateConnection('target', sftp({}))).toBeNull();
  });

  it('enforces FTPS host and port, SMB share and S3 bucket per role', () => {
    expect(validateConnection('source', draft({ provider: 'ftp', username: 'u', password: 'p' }))).toBe('connect.errors.sourceFtpHost');
    expect(validateConnection('target', draft({ provider: 'ftp', username: 'u', password: 'p', ftp: { host: 'h', port: '99999', tlsMode: 'explicit' } }))).toBe('connect.errors.ftpPort');
    expect(validateConnection('target', draft({ provider: 'smb', username: 'u', password: 'p', smb: { host: 'h', port: '445', share: ' ', domain: '' } }))).toBe('connect.errors.targetSmb');
    expect(validateConnection('source', draft({ provider: 's3', username: 'u', password: 'p', s3: { bucket: '', region: 'us-east-1', endpoint: '' } }))).toBe('connect.errors.sourceS3');
  });
});

describe('connection workflow', () => {
  const workflow = createConnectionWorkflow({ apiUrl: 'https://api.example.test', token: 'jwt' });
  const nextcloud = (patch: Partial<ConnectionDraft> = {}) =>
    draft({ provider: 'nextcloud', url: 'https://nc.example.com', username: 'alice', password: 'pw', ...patch });

  beforeEach(() => {
    vi.mocked(apiFetch).mockReset();
  });

  function respond(body: unknown, init?: ResponseInit) {
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify(body), init));
  }
  function lastCall() {
    const [url, init] = vi.mocked(apiFetch).mock.calls.at(-1)!;
    return { url: String(url), init: init!, body: JSON.parse(String(init?.body ?? 'null')) };
  }

  describe('testSource', () => {
    it('posts the resolved source to the test endpoint with bearer auth', async () => {
      respond({ success: true });
      expect(await workflow.testSource(nextcloud({ refreshToken: 'r', tokenExpiresIn: 60 }))).toEqual({ ok: true });
      const call = lastCall();
      expect(call.url).toBe('https://api.example.test/api/migration/connect/test');
      expect(new Headers(call.init.headers).get('Authorization')).toBe('Bearer jwt');
      expect(call.body).toEqual({
        source_url: 'https://nc.example.com', source_username: 'alice', source_password: 'pw',
        source_refresh_token: 'r', source_token_expires_in: 60, source_provider: 'nextcloud',
        source_profile_id: '', role: 'source',
      });
    });

    it('does not call the API when validation fails', async () => {
      const result = await workflow.testSource(nextcloud({ password: '' }));
      expect(result).toEqual({ ok: false, failure: { kind: 'validation', messageKey: 'connect.errors.missingFields' } });
      expect(apiFetch).not.toHaveBeenCalled();
    });

    it('surfaces the machine-readable error code from a failed probe', async () => {
      respond({ success: false, error_code: 'SOURCE_CONNECTION_FAILED' });
      expect(await workflow.testSource(nextcloud())).toEqual({ ok: false, failure: { kind: 'api', errorCode: 'SOURCE_CONNECTION_FAILED' } });
    });

    it('reports network failures without leaking the raw error', async () => {
      vi.mocked(apiFetch).mockRejectedValueOnce(new Error('boom'));
      expect(await workflow.testSource(nextcloud())).toEqual({ ok: false, failure: { kind: 'network' } });
    });
  });

  describe('connect', () => {
    it('posts both sides and returns the config and files on success', async () => {
      respond({ success: true, files: [{ name: 'a' }] });
      const result = await workflow.connect(
        nextcloud(),
        draft({ provider: 'webdav', url: 'https://wd.example.com', username: 'bob', password: 'pw2' }),
      );
      expect(result).toEqual({
        ok: true,
        files: [{ name: 'a' }],
        config: {
          source_url: 'https://nc.example.com', source_username: 'alice', source_password: 'pw',
          source_refresh_token: '', source_token_expires_in: 0,
          target_url: 'https://wd.example.com', target_username: 'bob', target_password: 'pw2',
          target_refresh_token: '', target_token_expires_in: 0,
          source_provider: 'nextcloud', target_provider: 'webdav',
          source_profile_id: '', target_profile_id: '',
        },
      });
      const call = lastCall();
      expect(call.url).toBe('https://api.example.test/api/migration/connect');
      expect(call.body).toMatchObject({ source_picker_session_id: '', source_profile_id: '', target_profile_id: '' });
    });

    it('never sends ad-hoc credentials for a side that uses a saved profile', async () => {
      respond({ success: true });
      await workflow.connect(nextcloud({ profileId: 'prof-src', password: 'leak' }), nextcloud({ profileId: 'prof-dst', password: 'leak' }));
      expect(lastCall().body).toMatchObject({
        source_profile_id: 'prof-src', source_url: '', source_username: '', source_password: '',
        target_profile_id: 'prof-dst', target_url: '', target_username: '', target_password: '',
      });
    });

    it('validates both sides before any request', async () => {
      const result = await workflow.connect(nextcloud(), nextcloud({ password: '' }));
      expect(result).toEqual({ ok: false, failure: { kind: 'validation', messageKey: 'connect.errors.missingFields' } });
      expect(apiFetch).not.toHaveBeenCalled();
    });

    describe('first reported validation error', () => {
      const badSftp = (patch: Partial<ConnectionDraft> = {}) => draft({
        provider: 'sftp', username: 'u', password: 'p',
        sftp: { host: '', port: '22', hostKey: VALID_SFTP_KEY, authMode: 'password', privateKey: '' }, ...patch,
      });
      const badSmb = draft({ provider: 'smb', username: 'u', password: 'p', smb: { host: '', port: '445', share: '', domain: '' } });
      const badS3 = draft({ provider: 's3', username: 'u', password: 'p', s3: { bucket: '', region: 'us-east-1', endpoint: '' } });
      const validationKey = async (source: ConnectionDraft, target: ConnectionDraft) => {
        const result = await workflow.connect(source, target);
        if (!('failure' in result)) return null;
        const { failure } = result;
        return failure.kind === 'validation' ? failure.messageKey : failure.kind;
      };

      it('reports a target SFTP/FTP problem before a source SMB or S3 problem', async () => {
        expect(await validationKey(badSmb, badSftp())).toBe('connect.errors.targetSftpHost');
        expect(await validationKey(badS3, draft({ provider: 'ftp', username: 'u', password: 'p' }))).toBe('connect.errors.targetFtpHost');
      });

      it('reports a source SMB problem before a target S3 problem and source S3 before target SMB', async () => {
        expect(await validationKey(badSmb, badS3)).toBe('connect.errors.sourceSmb');
        expect(await validationKey(badS3, badSmb)).toBe('connect.errors.sourceS3');
      });

      it('reports structural problems on either side before missing generic fields', async () => {
        expect(await validationKey(nextcloud({ password: '' }), badSmb)).toBe('connect.errors.targetSmb');
        expect(await validationKey(nextcloud({ password: '' }), nextcloud())).toBe('connect.errors.missingFields');
      });

      it('still reports the source SFTP/FTP problem first', async () => {
        expect(await validationKey(badSftp(), badSftp())).toBe('connect.errors.sourceSftpHost');
      });
    });

    it('maps HTTP and logical failures to error codes', async () => {
      respond({ error_code: 'TARGET_CONNECTION_FAILED' }, { status: 400 });
      expect(await workflow.connect(nextcloud(), nextcloud())).toEqual({ ok: false, failure: { kind: 'api', errorCode: 'TARGET_CONNECTION_FAILED' } });
      respond({ success: false, error_code: 'SOURCE_CONNECTION_FAILED' });
      expect(await workflow.connect(nextcloud(), nextcloud())).toEqual({ ok: false, failure: { kind: 'api', errorCode: 'SOURCE_CONNECTION_FAILED' } });
    });

    it('reports network failures as a typed failure', async () => {
      vi.mocked(apiFetch).mockRejectedValueOnce(new Error('boom'));
      expect(await workflow.connect(nextcloud(), nextcloud())).toEqual({ ok: false, failure: { kind: 'network' } });
    });
  });

  describe('saveProfile', () => {
    it('stores a password profile with a trimmed name and no OAuth fields', async () => {
      respond({}, { status: 201 });
      expect(await workflow.saveProfile(nextcloud(), '  Work  ')).toBe(true);
      const call = lastCall();
      expect(call.url).toBe('https://api.example.test/api/profiles');
      expect(call.init.method).toBe('POST');
      expect(call.body).toEqual({ name: 'Work', provider: 'nextcloud', url: 'https://nc.example.com', username: 'alice', password: 'pw' });
    });

    it('stores OAuth profiles with the refresh token instead of the access token', async () => {
      respond({});
      await workflow.saveProfile(draft({ provider: 'dropbox', password: 'access', refreshToken: 'refresh', oauthUser: 'me' }), 'Dropbox');
      expect(lastCall().body).toEqual({
        name: 'Dropbox', provider: 'dropbox', url: 'https://api.dropbox.com', username: 'me',
        refresh_token: 'refresh', oauth_user: 'me',
      });
    });

    it('refuses a blank name and reports failed saves without throwing', async () => {
      expect(await workflow.saveProfile(nextcloud(), '   ')).toBe(false);
      expect(apiFetch).not.toHaveBeenCalled();
      respond({}, { status: 500 });
      expect(await workflow.saveProfile(nextcloud(), 'x')).toBe(false);
      vi.mocked(apiFetch).mockRejectedValueOnce(new Error('down'));
      expect(await workflow.saveProfile(nextcloud(), 'x')).toBe(false);
    });

    it('derives the default profile name from the URL or provider', () => {
      expect(workflow.defaultProfileName(nextcloud())).toBe('https://nc.example.com');
      expect(workflow.defaultProfileName(draft({ provider: 'mega', username: 'u', password: 'p' }))).toBe('mega');
    });
  });

  describe('listProfiles', () => {
    it('returns saved profiles and degrades to an empty list on failure', async () => {
      respond({ profiles: [{ id: '1', name: 'A', provider: 'nextcloud' }] });
      expect(await workflow.listProfiles()).toEqual([{ id: '1', name: 'A', provider: 'nextcloud' }]);
      vi.mocked(apiFetch).mockRejectedValueOnce(new Error('down'));
      expect(await workflow.listProfiles()).toEqual([]);
    });
  });
});
