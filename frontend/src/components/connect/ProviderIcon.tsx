import React from 'react';
import {
  SiNextcloud,
  SiDropbox,
  SiGoogledrive,
  SiDeutschetelekom,
  SiImmich,
  SiMega,
  SiOwncloud,
  SiSeafile,
} from 'react-icons/si';
import { BiLogoAws } from 'react-icons/bi';
import { FiHardDrive } from 'react-icons/fi';
import { ImOnedrive } from 'react-icons/im';
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
  const iconCls = `${className}${className.includes('text-') ? '' : ' text-[var(--color-text-secondary)]'}`;

  switch (p) {
    case 'nextcloud':
      return <SiNextcloud className={iconCls} aria-hidden="true" />;

    case 'seafile':
      return <SiSeafile className={iconCls} aria-hidden="true" />;

    case 'opencloud':
      return <SiOwncloud className={iconCls} aria-hidden="true" />;

    case 'dropbox':
      return <SiDropbox className={iconCls} aria-hidden="true" />;

    case 'google':
      return <SiGoogledrive className={iconCls} aria-hidden="true" />;

    case 'onedrive':
      return <ImOnedrive className={iconCls} aria-hidden="true" />;

    case 's3':
      return <BiLogoAws className={iconCls} aria-hidden="true" />;

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
      return <SiMega className={iconCls} aria-hidden="true" />;

    case 'local':
      return <FiHardDrive className={iconCls} aria-hidden="true" />;

    default:
      return <CircleStackIcon className={iconCls} aria-hidden="true" />;
  }
};
