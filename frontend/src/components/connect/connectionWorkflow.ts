// Form workflow for the connection wizard.
//
// The wizard UI owns field state and rendering; everything that turns that
// state into a request lives here behind a small interface:
//   resolveConnection   draft -> credentials/URL that may be sent to the API
//   validateConnection  draft -> locale key of the first problem, or null
//   createConnectionWorkflow({ apiUrl, token })
//                       testSource / connect / saveProfile / listProfiles
// Credentials are only ever resolved at call time and a draft that points at a
// saved profile resolves to empty values, so ad-hoc secrets cannot leak next to
// a profile id.
import { isOAuthProvider, type CloudFile, type MigrationConfig, type ProviderId } from '../../types';
import { apiFetch } from '../../utils/apiClient';
import { createConnectionProfile } from '../../api/profiles';
import { buildFtpUrl, sftpHostKeyFingerprintPattern, type FtpTlsMode } from '../../utils/providerUrls';

export type ConnectionRole = 'source' | 'target';

export interface ConnectionDraft {
  provider: ProviderId;
  /** Saved profile supplying credentials server-side; empty for ad-hoc entry. */
  profileId: string;
  url: string;
  username: string;
  password: string;
  refreshToken: string;
  tokenExpiresIn: number;
  oauthUser: string;
  smb: { host: string; port: string; share: string; domain: string };
  s3: { endpoint: string; region: string; bucket: string };
  sftp: { host: string; port: string; hostKey: string; authMode: 'password' | 'key'; privateKey: string };
  ftp: { host: string; port: string; tlsMode: FtpTlsMode };
}

export interface ResolvedConnection {
  url: string;
  username: string;
  password: string;
}

export interface SavedProfileSummary {
  id: string;
  name: string;
  provider: string;
}

export type WorkflowFailure =
  | { kind: 'validation'; messageKey: string }
  | { kind: 'api'; errorCode?: string }
  | { kind: 'network' };

export type TestSourceResult = { ok: true } | { ok: false; failure: WorkflowFailure };
export type ConnectResult =
  | { ok: true; config: MigrationConfig; files: CloudFile[] }
  | { ok: false; failure: WorkflowFailure };

export interface ConnectionWorkflow {
  testSource(source: ConnectionDraft): Promise<TestSourceResult>;
  connect(source: ConnectionDraft, target: ConnectionDraft): Promise<ConnectResult>;
  saveProfile(draft: ConnectionDraft, name: string): Promise<boolean>;
  defaultProfileName(draft: ConnectionDraft): string;
  listProfiles(): Promise<SavedProfileSummary[]>;
}

export function createConnectionDraft(): ConnectionDraft {
  return {
    provider: 'nextcloud',
    profileId: '',
    url: '',
    username: '',
    password: '',
    refreshToken: '',
    tokenExpiresIn: 0,
    oauthUser: '',
    smb: { host: '', port: '445', share: '', domain: '' },
    s3: { endpoint: '', region: 'us-east-1', bucket: '' },
    sftp: { host: '', port: '22', hostKey: '', authMode: 'password', privateKey: '' },
    ftp: { host: '', port: '21', tlsMode: 'explicit' },
  };
}

const FIXED_ENDPOINT_PROVIDERS: readonly string[] = ['magentacloud', 'koofr', 'local', 'mega'];

function resolveUrl(draft: ConnectionDraft): string {
  const { provider } = draft;
  if (provider === 'smb') {
    const { host, port, share, domain } = draft.smb;
    return `smb://${host}:${port}/${share.replace(/^\//, '')}${domain ? '?domain=' + encodeURIComponent(domain) : ''}`;
  }
  if (provider === 's3') {
    const { bucket, region, endpoint } = draft.s3;
    return `s3://${bucket}?region=${encodeURIComponent(region)}${endpoint ? '&endpoint=' + encodeURIComponent(endpoint) : ''}`;
  }
  if (provider === 'sftp') {
    return `sftp://${draft.sftp.host}:${draft.sftp.port}?host_key=${encodeURIComponent(draft.sftp.hostKey.trim())}`;
  }
  if (provider === 'ftp') return buildFtpUrl(draft.ftp.host, draft.ftp.port, draft.ftp.tlsMode);
  if (FIXED_ENDPOINT_PROVIDERS.includes(provider)) return '';
  if (isOAuthProvider(provider)) return provider === 'onedrive' ? 'oauth://onedrive' : `https://api.${provider}.com`;
  return draft.url;
}

export function resolveConnection(draft: ConnectionDraft): ResolvedConnection {
  if (draft.profileId !== '') return { url: '', username: '', password: '' };
  const { provider } = draft;
  const local = provider === 'local';
  return {
    url: resolveUrl(draft),
    username: local ? '' : isOAuthProvider(provider) ? (draft.oauthUser || provider) : draft.username,
    password: local ? '' : provider === 'sftp' && draft.sftp.authMode === 'key' ? draft.sftp.privateKey : draft.password,
  };
}

// Validation runs in phases, mirroring the order in which the wizard has always
// reported the first problem: SFTP/FTP fields, then SMB/S3 fields, then generic
// missing credentials. validateConnectionPair applies each phase to the source
// and then the target before moving to the next phase.
type ValidationPhase = (role: ConnectionRole, draft: ConnectionDraft) => string | null;

const sftpFtpError: ValidationPhase = (role, draft) => {
  const { provider } = draft;
  if (provider === 'sftp') {
    if (!draft.sftp.host.trim()) return `connect.errors.${role}SftpHost`;
    if (!sftpHostKeyFingerprintPattern.test(draft.sftp.hostKey.trim())) return `connect.errors.${role}SftpHostKey`;
    if (draft.sftp.authMode === 'key' && !draft.sftp.privateKey.trim()) return `connect.errors.${role}SftpKey`;
  } else if (provider === 'ftp') {
    if (!draft.ftp.host.trim()) return `connect.errors.${role}FtpHost`;
    if (!resolveUrl(draft)) return 'connect.errors.ftpPort';
  }
  return null;
};

const smbS3Error: ValidationPhase = (role, draft) => {
  const { provider } = draft;
  if (provider === 'smb') {
    if (!draft.smb.host.trim() || !draft.smb.share.trim()) return `connect.errors.${role}Smb`;
  } else if (provider === 's3') {
    if (!draft.s3.bucket.trim() || !draft.s3.region.trim()) return `connect.errors.${role}S3`;
  }
  return null;
};

const missingFieldsError: ValidationPhase = (_role, draft) => {
  const { provider } = draft;
  const resolved = resolveConnection(draft);
  const urlRequired = !FIXED_ENDPOINT_PROVIDERS.includes(provider);
  if (
    (urlRequired && !resolved.url) ||
    (provider !== 'local' && provider !== 'immich' && !resolved.username) ||
    (provider !== 'local' && !resolved.password)
  ) {
    return 'connect.errors.missingFields';
  }
  return null;
};

const VALIDATION_PHASES: readonly ValidationPhase[] = [sftpFtpError, smbS3Error, missingFieldsError];

/** Runs one phase against a side; a saved profile supplies credentials server-side. */
function phaseError(phase: ValidationPhase, role: ConnectionRole, draft: ConnectionDraft): string | null {
  return draft.profileId !== '' ? null : phase(role, draft);
}

/** Locale key of the first problem with this side of the connection, or null. */
export function validateConnection(role: ConnectionRole, draft: ConnectionDraft): string | null {
  for (const phase of VALIDATION_PHASES) {
    const error = phaseError(phase, role, draft);
    if (error) return error;
  }
  return null;
}

/** Locale key of the first problem across both sides, or null. */
export function validateConnectionPair(source: ConnectionDraft, target: ConnectionDraft): string | null {
  for (const phase of VALIDATION_PHASES) {
    const error = phaseError(phase, 'source', source) ?? phaseError(phase, 'target', target);
    if (error) return error;
  }
  return null;
}

async function errorCodeOf(response: Response): Promise<string | undefined> {
  const body = await response.json().catch(() => ({} as { error_code?: string }));
  return body.error_code;
}

export function createConnectionWorkflow({ apiUrl, token }: { apiUrl: string; token: string }): ConnectionWorkflow {
  const jsonHeaders = { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
  const fail = (failure: WorkflowFailure): { ok: false; failure: WorkflowFailure } => ({ ok: false, failure });
  const validation = (messageKey: string) => fail({ kind: 'validation', messageKey });
  const apiFailure = (errorCode?: string) => fail({ kind: 'api', errorCode });
  const networkFailure = () => fail({ kind: 'network' });
  const sideFields = (role: ConnectionRole, draft: ConnectionDraft, resolved: ResolvedConnection) => ({
    [`${role}_url`]: resolved.url,
    [`${role}_username`]: resolved.username,
    [`${role}_password`]: resolved.password,
    [`${role}_refresh_token`]: draft.refreshToken,
    [`${role}_token_expires_in`]: draft.tokenExpiresIn,
    [`${role}_provider`]: draft.provider,
  });

  return {
    async testSource(source) {
      const invalid = validateConnection('source', source);
      if (invalid) return validation(invalid);
      try {
        const response = await apiFetch(`${apiUrl}/api/migration/connect/test`, {
          method: 'POST',
          headers: jsonHeaders,
          body: JSON.stringify({
            ...sideFields('source', source, resolveConnection(source)),
            source_profile_id: source.profileId,
            role: 'source',
          }),
        });
        const body = await response.json().catch(() => ({} as { success?: boolean; error_code?: string }));
        if (!response.ok || !body.success) return apiFailure(body.error_code);
        return { ok: true };
      } catch {
        return networkFailure();
      }
    },

    async connect(source, target) {
      const invalid = validateConnectionPair(source, target);
      if (invalid) return validation(invalid);

      const resolvedSource = resolveConnection(source);
      const resolvedTarget = resolveConnection(target);
      try {
        const response = await apiFetch(`${apiUrl}/api/migration/connect`, {
          method: 'POST',
          headers: jsonHeaders,
          body: JSON.stringify({
            ...sideFields('source', source, resolvedSource),
            ...sideFields('target', target, resolvedTarget),
            source_picker_session_id: '',
            source_profile_id: source.profileId,
            target_profile_id: target.profileId,
          }),
        });
        if (!response.ok) return apiFailure(await errorCodeOf(response));

        const data = await response.json() as { success: boolean; files?: CloudFile[]; error_code?: string };
        if (!data.success) return apiFailure(data.error_code);
        return {
          ok: true,
          files: data.files || [],
          config: {
            source_url: resolvedSource.url,
            source_username: resolvedSource.username,
            source_password: resolvedSource.password,
            source_refresh_token: source.refreshToken,
            source_token_expires_in: source.tokenExpiresIn,
            target_url: resolvedTarget.url,
            target_username: resolvedTarget.username,
            target_password: resolvedTarget.password,
            target_refresh_token: target.refreshToken,
            target_token_expires_in: target.tokenExpiresIn,
            source_provider: source.provider,
            target_provider: target.provider,
            source_profile_id: source.profileId,
            target_profile_id: target.profileId,
          },
        };
      } catch {
        return networkFailure();
      }
    },

    async saveProfile(draft, name) {
      const trimmed = name.trim();
      if (!trimmed) return false;
      const oauth = isOAuthProvider(draft.provider);
      const oauthUser = draft.oauthUser || draft.provider;
      const payload: Record<string, unknown> = {
        name: trimmed,
        provider: draft.provider,
        url: resolveConnection(draft).url,
        username: oauth ? oauthUser : draft.username,
      };
      if (!oauth && draft.password) payload.password = draft.password;
      if (oauth && draft.refreshToken) {
        payload.refresh_token = draft.refreshToken;
        payload.oauth_user = oauthUser;
      }
      try {
        return (await createConnectionProfile({ apiUrl, token, payload })).ok;
      } catch {
        return false;
      }
    },

    defaultProfileName: (draft) => resolveConnection(draft).url || draft.provider,

    async listProfiles() {
      try {
        const res = await apiFetch(`${apiUrl}/api/profiles`, { headers: { Authorization: `Bearer ${token}` } });
        const data = await res.json().catch(() => ({})) as { profiles?: SavedProfileSummary[] };
        return data.profiles ?? [];
      } catch {
        return [];
      }
    },
  };
}
