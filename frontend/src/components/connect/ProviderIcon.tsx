import React from 'react';
import {
  SiNextcloud,
  SiDropbox,
  SiGoogledrive,
  SiDeutschetelekom,
  SiImmich,
  SiSeafile,
} from 'react-icons/si';
import { TbBrandAws, TbBrandOnedrive } from 'react-icons/tb';
import {
  CircleStackIcon,
  CloudIcon,
  CommandLineIcon,
  GlobeAltIcon,
  LockClosedIcon,
  ServerStackIcon,
} from '@heroicons/react/24/outline';

interface ProviderIconProps {
  provider: string;
  className?: string;
}

export const ProviderIcon: React.FC<ProviderIconProps> = ({ provider, className = 'w-6 h-6' }) => {
  const p = provider.toLowerCase();
  const iconCls = `${className} text-[var(--color-text-secondary)]`;

  switch (p) {
    case 'nextcloud':
      return <SiNextcloud className={iconCls} aria-hidden="true" />;

    case 'seafile':
      return <SiSeafile className={iconCls} aria-hidden="true" />;

    case 'opencloud':
      return (
        <svg className={iconCls} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
          <path d="M19.35 10.04C18.67 6.59 15.64 4 12 4 9.11 4 6.6 5.64 5.35 8.04 2.34 8.36 0 10.91 0 14c0 3.31 2.69 6 6 6h13c2.76 0 5-2.24 5-5 0-2.64-2.05-4.78-4.65-4.96z" />
        </svg>
      );

    case 'dropbox':
      return <SiDropbox className={iconCls} aria-hidden="true" />;

    case 'google':
      return <SiGoogledrive className={iconCls} aria-hidden="true" />;

    case 'onedrive':
      return <TbBrandOnedrive className={iconCls} aria-hidden="true" />;

    case 's3':
      return <TbBrandAws className={iconCls} aria-hidden="true" />;

    case 'smb':
      return <ServerStackIcon className={iconCls} aria-hidden="true" />;

    case 'sftp':
      return <CommandLineIcon className={iconCls} aria-hidden="true" />;

    case 'ftp':
      return <LockClosedIcon className={iconCls} aria-hidden="true" />;

    case 'immich':
      return <SiImmich className={iconCls} aria-hidden="true" />;

    case 'magentacloud':
      return <SiDeutschetelekom className={iconCls} aria-hidden="true" />;

    case 'koofr':
      return <CloudIcon className={iconCls} aria-hidden="true" />;

    case 'hidrive':
      return <CloudIcon className={iconCls} aria-hidden="true" />;

    case 'webdav':
      return <GlobeAltIcon className={iconCls} aria-hidden="true" />;

    case 'mega':
      return <LockClosedIcon className={iconCls} aria-hidden="true" />;

    case 'local':
      return <CircleStackIcon className={iconCls} aria-hidden="true" />;

    default:
      return <CircleStackIcon className={iconCls} aria-hidden="true" />;
  }
};
