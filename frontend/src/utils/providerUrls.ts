export const sftpHostKeyFingerprintPattern = /^SHA256:[A-Za-z0-9+/]{43}$/;

export interface SmbUrlParams {
  host: string;
  port: string;
  share: string;
  domain: string;
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
  if (!host || !share) return '';
  const p = port || '445';
  const query = domain ? `?domain=${encodeURIComponent(domain)}` : '';
  return `smb://${host}:${p}/${share.replace(/^\//, '')}${query}`;
}

export interface S3UrlParams {
  bucket: string;
  region: string;
  endpoint: string;
  insecure: boolean;
}

export function parseS3Url(urlStr: string): S3UrlParams {
  let bucket = '', region = 'us-east-1', endpoint = '', insecure = false;
  if (!urlStr) return { bucket, region, endpoint, insecure };
  try {
    const u = new URL(urlStr);
    bucket = u.hostname || '';
    region = u.searchParams.get('region') || 'us-east-1';
    endpoint = u.searchParams.get('endpoint') || '';
    insecure = u.searchParams.get('insecure') === 'true';
  } catch {
    /* ignore malformed URLs */
  }
  return { bucket, region, endpoint, insecure };
}

export function buildS3Url(bucket: string, region: string, endpoint: string, insecure: boolean): string {
  if (!bucket) return '';
  const reg = region || 'us-east-1';
  const epPart = endpoint ? `&endpoint=${encodeURIComponent(endpoint)}` : '';
  const insecPart = insecure ? '&insecure=true' : '';
  return `s3://${bucket}?region=${encodeURIComponent(reg)}${epPart}${insecPart}`;
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
  if (!host) return '';
  const p = port || '22';
  const hkPart = hostKey.trim() ? `?host_key=${encodeURIComponent(hostKey.trim())}` : '';
  return `sftp://${host}:${p}${hkPart}`;
}
