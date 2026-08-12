export const sftpHostKeyFingerprintPattern = /^SHA256:[A-Za-z0-9+/]{43}$/;

export interface SmbUrlParams {
  host: string;
  port: string;
  share: string;
  domain: string;
}

function isValidPort(port: string): boolean {
  const value = Number(port);
  return Number.isInteger(value) && value >= 1 && value <= 65535;
}

function formatUrlHost(host: string): string {
  return host.includes(':') && !host.startsWith('[') ? '[' + host + ']' : host;
}

function isValidSmbHost(host: string): boolean {
  return Boolean(host) && !/[\s@/?#]/.test(host);
}

function isValidSmbShare(share: string): boolean {
  return Boolean(share)
    && share !== '.'
    && share !== '..'
    && !share.includes('/')
    && !share.includes('\\');
}

// The backend also resolves and validates every egress address. These checks
// reject the always-forbidden literal hosts before a profile reaches it.
function isAlwaysBlockedS3EndpointHost(host: string): boolean {
  const normalized = host.replace(/^\[|\]$/g, '').toLowerCase();
  if (normalized === 'localhost' || normalized.endsWith('.localhost') || normalized === '::1' || normalized.startsWith('fe80:')) {
    return true;
  }
  const ipv4 = normalized.split('.').map(Number);
  return ipv4.length === 4
    && ipv4.every((part) => Number.isInteger(part) && part >= 0 && part <= 255)
    && (ipv4[0] === 127 || (ipv4[0] === 169 && ipv4[1] === 254));
}

export function parseSmbUrl(urlStr: string): SmbUrlParams {
  let host = '', port = '445', share = '', domain = '';
  if (!urlStr) return { host, port, share, domain };
  try {
    const u = new URL(urlStr);
    host = u.hostname || '';
    port = u.port || '445';
    share = u.pathname.replace(/^\//, '') || '';
    domain = u.searchParams.get('domain') || '';
  } catch {
    /* ignore malformed URLs */
  }
  return { host, port, share, domain };
}

export function buildSmbUrl(host: string, port: string, share: string, domain: string): string {
  const trimmedHost = host.trim();
  const normalizedShare = share.trim().replace(/^[/\\]+/, '');
  const p = port || '445';
  if (!isValidSmbHost(trimmedHost) || !isValidSmbShare(normalizedShare) || !isValidPort(p)) return '';
  const query = domain ? `?domain=${encodeURIComponent(domain)}` : '';
  return `smb://${formatUrlHost(trimmedHost)}:${p}/${normalizedShare}${query}`;
}

export interface S3UrlParams {
  bucket: string;
  region: string;
  endpoint: string;
}

export function parseS3Url(urlStr: string): S3UrlParams {
  let bucket = '', region = 'us-east-1', endpoint = '';
  if (!urlStr) return { bucket, region, endpoint };
  try {
    const u = new URL(urlStr);
    bucket = u.hostname || '';
    region = u.searchParams.get('region') || 'us-east-1';
    endpoint = u.searchParams.get('endpoint') || '';
  } catch {
    /* ignore malformed URLs */
  }
  return { bucket, region, endpoint };
}

export function buildS3Url(bucket: string, region: string, endpoint: string): string {
  if (!bucket.trim()) return '';
  const normalizedEndpoint = endpoint.trim();
  if (normalizedEndpoint) {
    try {
      const endpointUrl = new URL(normalizedEndpoint);
      if (endpointUrl.protocol !== 'https:' || !endpointUrl.hostname || endpointUrl.username || endpointUrl.password || endpointUrl.hash || isAlwaysBlockedS3EndpointHost(endpointUrl.hostname)) {
        return '';
      }
    } catch {
      return '';
    }
  }
  const reg = region || 'us-east-1';
  const epPart = normalizedEndpoint ? `&endpoint=${encodeURIComponent(normalizedEndpoint)}` : '';
  return `s3://${bucket.trim()}?region=${encodeURIComponent(reg)}${epPart}`;
}

export interface SftpUrlParams {
  host: string;
  port: string;
  hostKey: string;
}

export function parseSftpUrl(urlStr: string): SftpUrlParams {
  let host = '', port = '22', hostKey = '';
  if (!urlStr) return { host, port, hostKey };
  try {
    const u = new URL(urlStr);
    host = u.hostname || '';
    port = u.port || '22';
    hostKey = u.searchParams.get('host_key') || '';
  } catch {
    /* ignore malformed URLs */
  }
  return { host, port, hostKey };
}

export function buildSftpUrl(host: string, port: string, hostKey: string): string {
  const trimmedHost = host.trim();
  if (!trimmedHost || /[\s@/?#]/.test(trimmedHost) || !isValidPort(port || '22')) return '';
  const p = port || '22';
  const hkPart = hostKey.trim() ? `?host_key=${encodeURIComponent(hostKey.trim())}` : '';
  return `sftp://${formatUrlHost(trimmedHost)}:${p}${hkPart}`;
}

export type FtpTlsMode = 'explicit' | 'implicit';

export interface FtpUrlParams {
  host: string;
  port: string;
  tlsMode: FtpTlsMode;
}

const defaultFtpUrlParams: FtpUrlParams = { host: '', port: '21', tlsMode: 'explicit' };

// Only FTPS URLs are accepted. Plain FTP must use explicit TLS.
export function parseFtpUrl(urlStr: string): FtpUrlParams {
  if (!urlStr) return defaultFtpUrlParams;
  try {
    const u = new URL(urlStr);
    if (!u.hostname || !isValidPort(u.port || (u.protocol === 'ftps:' ? '990' : '21')) || u.username || u.password || u.hash || (u.pathname !== '' && u.pathname !== '/')) return defaultFtpUrlParams;

    if (u.protocol === 'ftp:' && u.search === '?tls=explicit') {
      return { host: u.hostname, port: u.port || '21', tlsMode: 'explicit' };
    }
    if (u.protocol === 'ftps:' && !u.search) {
      return { host: u.hostname, port: u.port || '990', tlsMode: 'implicit' };
    }
  } catch {
    // Ignore malformed profile URLs and use safe explicit-FTPS defaults.
  }
  return defaultFtpUrlParams;
}

export function buildFtpUrl(host: string, port: string, tlsMode: FtpTlsMode): string {
  const trimmedHost = host.trim();
  if (!trimmedHost || /[\s@/?#]/.test(trimmedHost) || !isValidPort(port || (tlsMode === 'explicit' ? '21' : '990'))) return '';
  const finalPort = port || (tlsMode === 'explicit' ? '21' : '990');
  const formattedHost = formatUrlHost(trimmedHost);
  return tlsMode === 'explicit'
    ? `ftp://${formattedHost}:${finalPort}?tls=explicit`
    : `ftps://${formattedHost}:${finalPort}`;
}
