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
        <svg className={className} viewBox="0 0 24 24" fill="none">
          <circle cx="12" cy="12" r="4" fill="#0082C9" />
          <circle cx="12" cy="12" r="2.2" fill="var(--color-bg-primary, #FFFFFF)" />
          <circle cx="4.5" cy="12" r="2.5" fill="#0082C9" />
          <circle cx="19.5" cy="12" r="2.5" fill="#0082C9" />
          <path d="M4.5 9.5A2.5 2.5 0 0 1 12 8a2.5 2.5 0 0 1 7.5 1.5M4.5 14.5A2.5 2.5 0 0 0 12 16a2.5 2.5 0 0 0 7.5-1.5" stroke="#0082C9" strokeWidth="1.8" fill="none" strokeLinecap="round" />
        </svg>
      );

    case 'dropbox':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="#0061FF">
          <path d="M6 2L0 6l6 4 6-4-6-4zm12 0l-6 4 6 4 6-4-6-4zM0 14l6 4 6-4-6-4-6 4zm18-4l-6 4 6 4 6-4-6-4zM6 19l6 4 6-4-6-4-6 4z" />
        </svg>
      );

    case 'google':
      return (
        <svg className={className} viewBox="0 0 87.3 78">
          <path d="M6.6 66.85l3.85 6.65c.8 1.4 1.95 2.5 3.3 3.3l13.75-23.8H0c0 1.55.4 3.1 1.2 4.5l5.4 9.35z" fill="#0066DA"/>
          <path d="M43.65 25L29.9 1.2c-1.35.8-2.5 1.9-3.3 3.3l-25.4 44c-.8 1.4-1.2 2.95-1.2 4.5h27.5L43.65 25z" fill="#00AC47"/>
          <path d="M73.55 76.8c1.35-.8 2.5-1.9 3.3-3.3l1.6-2.75 7.65-13.25c.8-1.4 1.2-2.95 1.2-4.5H59.8l5.85 10.15 7.9 13.65z" fill="#EA4335"/>
          <path d="M43.65 25L57.4 1.2C56.05.4 54.5 0 52.9 0H34.4c-1.6 0-3.15.4-4.5 1.2L43.65 25z" fill="#00832D"/>
          <path d="M59.8 53H27.5L13.75 76.8c1.35.8 2.9 1.2 4.5 1.2h50.8c1.6 0 3.15-.4 4.5-1.2L59.8 53z" fill="#2684FC"/>
          <path d="M73.4 22.5L50.8 1.2C49.45.4 47.9 0 46.3 0L43.65 25l29.75 28h13.9c0-1.55-.4-3.1-1.2-4.5l-12.7-26z" fill="#FFBA00"/>
        </svg>
      );

    case 'onedrive':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="#0078D4">
          <path d="M19.35 10.04C18.67 6.59 15.64 4 12 4 9.11 4 6.6 5.64 5.35 8.04 2.34 8.36 0 10.91 0 14c0 3.31 2.69 6 6 6h13c2.76 0 5-2.24 5-5 0-2.64-2.05-4.78-4.65-4.96z" />
        </svg>
      );

    case 's3':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none">
          <path d="M12 2C6.48 2 2 3.79 2 6v12c0 2.21 4.48 4 10 4s10-1.79 10-4V6c0-2.21-4.48-4-10-4z" fill="#FF9900" fillOpacity="0.2" stroke="#FF9900" strokeWidth="2" />
          <ellipse cx="12" cy="6" rx="10" ry="4" stroke="#FF9900" strokeWidth="2" fill="#FF9900" fillOpacity="0.4" />
          <path d="M2 12c0 2.21 4.48 4 10 4s10-1.79 10-4" stroke="#FF9900" strokeWidth="2" />
        </svg>
      );

    case 'smb':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="#10B981" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <rect x="2" y="4" width="20" height="6" rx="2" fill="#10B981" fillOpacity="0.15" />
          <rect x="2" y="14" width="20" height="6" rx="2" fill="#10B981" fillOpacity="0.15" />
          <circle cx="6" cy="7" r="1" fill="#10B981" />
          <circle cx="6" cy="17" r="1" fill="#10B981" />
          <line x1="12" y1="10" x2="12" y2="14" />
        </svg>
      );

    case 'sftp':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none">
          <rect x="2" y="3" width="20" height="18" rx="3" fill="#6366F1" fillOpacity="0.15" stroke="#6366F1" strokeWidth="2" />
          <path d="M7 8l4 4-4 4M13 16h4" stroke="#6366F1" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );

    case 'ftp':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="#06B6D4" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M4 17l6-6-6-6M12 19h8" />
          <rect x="13" y="5" width="8" height="8" rx="2" fill="#06B6D4" fillOpacity="0.2" />
          <path d="M15 7v-1a2 2 0 0 1 4 0v1" stroke="#06B6D4" strokeWidth="1.5" />
        </svg>
      );

    case 'immich':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none">
          <path d="M12 2L4 7v10l8 5 8-5V7l-8-5z" fill="#3B82F6" fillOpacity="0.2" stroke="#3B82F6" strokeWidth="2" strokeLinejoin="round" />
          <path d="M12 6l5 3v6l-5 3-5-3V9l5-3z" fill="#3B82F6" stroke="#3B82F6" strokeWidth="1.5" strokeLinejoin="round" />
        </svg>
      );

    case 'magentacloud':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="#E20074">
          <path d="M0 4h24v4H14v12h-4V8H0V4zm2 14h3v3H2v-3zm17 0h3v3h-3v-3z" />
        </svg>
      );

    case 'hidrive':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none">
          <rect x="2" y="4" width="20" height="16" rx="3" fill="#E60000" fillOpacity="0.1" stroke="#E60000" strokeWidth="2" />
          <path d="M7 8v8M17 8v8M7 12h10" stroke="#E60000" strokeWidth="2.5" strokeLinecap="round" />
        </svg>
      );

    case 'webdav':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="#14B8A6" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <circle cx="12" cy="12" r="9" fill="#14B8A6" fillOpacity="0.1" />
          <line x1="3.6" y1="9" x2="20.4" y2="9" />
          <line x1="3.6" y1="15" x2="20.4" y2="15" />
          <path d="M11.5 3a13 13 0 0 0 0 18M12.5 3a13 13 0 0 1 0 18" />
        </svg>
      );

    case 'local':
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="#64748B" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <rect x="3" y="5" width="18" height="14" rx="3" fill="#64748B" fillOpacity="0.15" />
          <line x1="7" y1="12" x2="7.01" y2="12" strokeWidth="3" />
          <line x1="11" y1="12" x2="11.01" y2="12" strokeWidth="3" />
          <line x1="16" y1="12" x2="17" y2="12" strokeWidth="2" />
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
