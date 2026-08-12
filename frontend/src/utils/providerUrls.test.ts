import { describe, it, expect } from 'vitest';
import {
  parseSmbUrl,
  buildSmbUrl,
  parseS3Url,
  buildS3Url,
  parseSftpUrl,
  buildSftpUrl,
  sftpHostKeyFingerprintPattern,
  parseFtpUrl,
  buildFtpUrl,
} from './providerUrls';

describe('providerUrls', () => {
  describe('SMB URL handling', () => {
    it('parses valid SMB URLs correctly', () => {
      const parsed = parseSmbUrl('smb://192.168.1.10:445/myshare?domain=WORKGROUP');
      expect(parsed).toEqual({
        host: '192.168.1.10',
        port: '445',
        share: 'myshare',
        domain: 'WORKGROUP',
      });
    });

    it('handles SMB URLs without explicit port or domain', () => {
      const parsed = parseSmbUrl('smb://nas.local/share');
      expect(parsed).toEqual({
        host: 'nas.local',
        port: '445',
        share: 'share',
        domain: '',
      });
    });

    it('returns default empty values on malformed inputs', () => {
      expect(parseSmbUrl('')).toEqual({ host: '', port: '445', share: '', domain: '' });
      expect(parseSmbUrl('invalid-url')).toEqual({ host: '', port: '445', share: '', domain: '' });
    });

    it('builds SMB URLs correctly', () => {
      expect(buildSmbUrl('192.168.1.10', '445', 'myshare', 'WORKGROUP')).toBe(
        'smb://192.168.1.10:445/myshare?domain=WORKGROUP'
      );
      expect(buildSmbUrl('192.168.1.10', '', 'myshare', '')).toBe(
        'smb://192.168.1.10:445/myshare'
      );
      expect(buildSmbUrl('', '445', 'myshare', '')).toBe('');
      expect(buildSmbUrl('host', '445', '', '')).toBe('');
      expect(buildSmbUrl('host', '445', '../share', '')).toBe('');
      expect(buildSmbUrl('host', '0', 'myshare', '')).toBe('');
      expect(buildSmbUrl('user@host', '445', 'myshare', '')).toBe('');
    });
  });

  describe('S3 URL handling', () => {
    it('parses valid S3 URLs correctly', () => {
      const parsed = parseS3Url(
        's3://mybucket?region=us-west-2&endpoint=https%3A%2F%2Fminio.local%3A9000'
      );
      expect(parsed).toEqual({
        bucket: 'mybucket',
        region: 'us-west-2',
        endpoint: 'https://minio.local:9000',
      });
    });

    it('handles basic S3 URLs with default region', () => {
      const parsed = parseS3Url('s3://mybucket');
      expect(parsed).toEqual({
        bucket: 'mybucket',
        region: 'us-east-1',
        endpoint: '',
      });
    });

    it('returns default values on malformed S3 URLs', () => {
      expect(parseS3Url('')).toEqual({ bucket: '', region: 'us-east-1', endpoint: '' });
      expect(parseS3Url('not-a-url')).toEqual({ bucket: '', region: 'us-east-1', endpoint: '' });
    });

    it('builds S3 URLs correctly', () => {
      expect(buildS3Url('mybucket', 'eu-central-1', 'https://s3.example.com')).toBe(
        's3://mybucket?region=eu-central-1&endpoint=https%3A%2F%2Fs3.example.com'
      );
      expect(buildS3Url('mybucket', '', '')).toBe('s3://mybucket?region=us-east-1');
      expect(buildS3Url('', 'us-east-1', '')).toBe('');
      expect(buildS3Url('mybucket', 'us-east-1', 'http://s3.example.com')).toBe('');
      expect(buildS3Url('mybucket', 'us-east-1', 'https://user:pass@s3.example.com')).toBe('');
      expect(buildS3Url('mybucket', 'us-east-1', 'not-a-url')).toBe('');
      expect(buildS3Url('mybucket', 'us-east-1', 'https://169.254.169.254')).toBe('');
      expect(buildS3Url('mybucket', 'us-east-1', 'https://[::1]')).toBe('');
    });
  });

  describe('SFTP URL handling', () => {
    it('parses valid SFTP URLs correctly', () => {
      const key = 'SHA256:abc123def456ghi789jkl012mno345pqr678stu901v';
      const parsed = parseSftpUrl(`sftp://sftp.example.com:2222?host_key=${encodeURIComponent(key)}`);
      expect(parsed).toEqual({
        host: 'sftp.example.com',
        port: '2222',
        hostKey: key,
      });
    });

    it('handles SFTP URLs with default port', () => {
      const parsed = parseSftpUrl('sftp://sftp.example.com');
      expect(parsed).toEqual({
        host: 'sftp.example.com',
        port: '22',
        hostKey: '',
      });
    });

    it('returns default values on malformed SFTP URLs', () => {
      expect(parseSftpUrl('')).toEqual({ host: '', port: '22', hostKey: '' });
      expect(parseSftpUrl('invalid')).toEqual({ host: '', port: '22', hostKey: '' });
    });

    it('builds SFTP URLs correctly', () => {
      const key = 'SHA256:abc123def456ghi789jkl012mno345pqr678stu901v';
      expect(buildSftpUrl('sftp.example.com', '22', key)).toBe(
        `sftp://sftp.example.com:22?host_key=${encodeURIComponent(key)}`
      );
      expect(buildSftpUrl('sftp.example.com', '', '')).toBe('sftp://sftp.example.com:22');
      expect(buildSftpUrl('', '22', key)).toBe('');
      expect(buildSftpUrl('user@sftp.example.com', '22', key)).toBe('');
      expect(buildSftpUrl('sftp.example.com', '0', key)).toBe('');
    });
  });

  describe('SFTP Host Key Fingerprint pattern', () => {
    it('validates correct SHA256 base64 fingerprints', () => {
      const valid = 'SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU';
      expect(sftpHostKeyFingerprintPattern.test(valid)).toBe(true);
    });

    it('rejects invalid or malformed fingerprints', () => {
      expect(sftpHostKeyFingerprintPattern.test('MD5:12345')).toBe(false);
      expect(sftpHostKeyFingerprintPattern.test('SHA256:short')).toBe(false);
      expect(sftpHostKeyFingerprintPattern.test('SHA256:too-long-fingerprint-string-that-exceeds-forty-three-chars-1234567890')).toBe(false);
    });
  });

  describe('FTPS URL handling', () => {
    it('round-trips explicit and implicit FTPS URLs', () => {
      expect(parseFtpUrl('ftp://ftp.example.com:2121?tls=explicit')).toEqual({
        host: 'ftp.example.com', port: '2121', tlsMode: 'explicit',
      });
      expect(parseFtpUrl('ftps://ftp.example.com:1990')).toEqual({
        host: 'ftp.example.com', port: '1990', tlsMode: 'implicit',
      });
      expect(buildFtpUrl('ftp.example.com', '2121', 'explicit')).toBe('ftp://ftp.example.com:2121?tls=explicit');
      expect(buildFtpUrl('ftp.example.com', '1990', 'implicit')).toBe('ftps://ftp.example.com:1990');
    });

    it('uses the FTPS default ports', () => {
      expect(parseFtpUrl('ftp://ftp.example.com?tls=explicit')).toEqual({ host: 'ftp.example.com', port: '21', tlsMode: 'explicit' });
      expect(parseFtpUrl('ftps://ftp.example.com')).toEqual({ host: 'ftp.example.com', port: '990', tlsMode: 'implicit' });
      expect(buildFtpUrl('ftp.example.com', '', 'explicit')).toBe('ftp://ftp.example.com:21?tls=explicit');
      expect(buildFtpUrl('ftp.example.com', '', 'implicit')).toBe('ftps://ftp.example.com:990');
    });

    it('rejects plaintext FTP and invalid FTPS variants', () => {
      const empty = { host: '', port: '21', tlsMode: 'explicit' };
      expect(parseFtpUrl('ftp://ftp.example.com')).toEqual(empty);
      expect(parseFtpUrl('ftp://user:pass@ftp.example.com?tls=explicit')).toEqual(empty);
      expect(parseFtpUrl('ftp://ftp.example.com?tls=implicit')).toEqual(empty);
      expect(parseFtpUrl('ftps://ftp.example.com?tls=explicit')).toEqual(empty);
      expect(parseFtpUrl('ftp://ftp.example.com:0?tls=explicit')).toEqual(empty);
      expect(buildFtpUrl('ftp.example.com', '0', 'explicit')).toBe('');
      expect(buildFtpUrl('user@ftp.example.com', '21', 'explicit')).toBe('');
    });
  });
});
