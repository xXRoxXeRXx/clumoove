import React from 'react';

interface ProviderIconProps {
  provider: string;
  className?: string;
}

export const ProviderIcon: React.FC<ProviderIconProps> = ({ provider, className = 'w-6 h-6' }) => {
  const p = provider.toLowerCase();

  switch (p) {
    case 'nextcloud':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="currentColor">
          <circle cx="12" cy="12" r="3.5" fill="#0082C9" />
          <circle cx="5" cy="12" r="2.5" fill="#0082C9" />
          <circle cx="19" cy="12" r="2.5" fill="#0082C9" />
          <path d="M5 9.5a2.5 2.5 0 0 1 0 5M19 9.5a2.5 2.5 0 0 0 0 5" stroke="#0082C9" strokeWidth="1.5" fill="none" />
        </svg>
      );

    case 'dropbox':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="#0061FF">
          <path d="M6 3.5L12 7.5L6 11.5L0 7.5L6 3.5ZM18 3.5L24 7.5L18 11.5L12 7.5L18 3.5ZM0 15.5L6 11.5L12 15.5L6 19.5L0 15.5ZM24 15.5L18 11.5L12 15.5L18 19.5L24 15.5ZM6 20.5L12 16.5L18 20.5L12 24.5L6 20.5Z" />
        </svg>
      );

    case 'google':
      return (
        <svg className={className} viewBox="0 0 24 24">
          <path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z" fill="#4285F4" />
          <path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#34A853" />
          <path d="M5.84 14.1c-.22-.66-.35-1.36-.35-2.1s.13-1.44.35-2.1V7.06H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.94l2.85-2.22.81-.62z" fill="#FBBC05" />
          <path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.06l3.66 2.84c.87-2.6 3.3-4.52 6.16-4.52z" fill="#EA4335" />
        </svg>
      );

    case 'onedrive':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="#0078D4">
          <path d="M19.35 10.04C18.67 6.59 15.64 4 12 4 9.11 4 6.6 5.64 5.35 8.04 2.34 8.36 0 10.91 0 14c0 3.31 2.69 6 6 6h13c2.76 0 5-2.24 5-5 0-2.64-2.05-4.78-4.65-4.96zM19 18H6c-2.21 0-4-1.79-4-4 0-2.05 1.53-3.76 3.56-3.97l1.07-.11.5-.95C8.08 7.14 9.94 6 12 6c2.62 0 4.88 1.86 5.39 4.43l.3 1.5 1.53.11c1.56.1 2.78 1.41 2.78 2.96 0 1.65-1.35 3-3 3z" />
        </svg>
      );

    case 's3':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none">
          <rect x="3" y="4" width="18" height="16" rx="3" fill="#E57373" fillOpacity="0.15" stroke="#E53935" strokeWidth="2" />
          <path d="M7 8h10M7 12h10M7 16h6" stroke="#E53935" strokeWidth="2" strokeLinecap="round" />
          <circle cx="17" cy="16" r="2" fill="#E53935" />
        </svg>
      );

    case 'smb':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="#10B981" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <rect x="2" y="3" width="20" height="6" rx="1" />
          <rect x="2" y="15" width="20" height="6" rx="1" />
          <line x1="12" y1="9" x2="12" y2="15" />
          <line x1="6" y1="9" x2="6" y2="15" />
          <line x1="18" y1="9" x2="18" y2="15" />
        </svg>
      );

    case 'sftp':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none">
          <rect x="2" y="4" width="20" height="16" rx="2" fill="#6366F1" fillOpacity="0.15" stroke="#6366F1" strokeWidth="2" />
          <path d="M7 9l3 3-3 3M13 15h4" stroke="#6366F1" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );

    case 'ftp':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="#06B6D4" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M4 17l6-6-6-6M12 19h8" />
          <circle cx="16" cy="9" r="3" fill="#06B6D4" fillOpacity="0.2" />
        </svg>
      );

    case 'immich':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none">
          <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5" stroke="#3B82F6" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );

    case 'magentacloud':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="#E20074">
          <path d="M3 7h4v2H5v10h2v2H3V7zm14 0h4v14h-4v-2h2V9h-2V7zM9 9h6v2H9V9zm0 4h6v2H9v-2zm0 4h6v2H9v-2z" />
        </svg>
      );

    case 'hidrive':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none">
          <circle cx="12" cy="12" r="9" fill="#E60000" fillOpacity="0.15" stroke="#E60000" strokeWidth="2" />
          <path d="M8 8v8M16 8v8M8 12h8" stroke="#E60000" strokeWidth="2.5" strokeLinecap="round" />
        </svg>
      );

    case 'webdav':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="#14B8A6" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <circle cx="12" cy="12" r="10" />
          <line x1="2" y1="12" x2="22" y2="12" />
          <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
        </svg>
      );

    case 'local':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="#64748B" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <rect x="2" y="6" width="20" height="12" rx="2" />
          <path d="M6 12h.01M10 12h.01" />
          <line x1="16" y1="12" x2="18" y2="12" />
        </svg>
      );

    default:
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
        </svg>
      );
  }
};
