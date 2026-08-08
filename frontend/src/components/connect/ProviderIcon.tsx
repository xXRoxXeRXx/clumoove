import React from 'react';
import {
  SiNextcloud,
  SiDropbox,
  SiGoogledrive,
  SiDeutschetelekom,
  SiImmich,
} from 'react-icons/si';
import {
  TbBrandOnedrive,
  TbBrandAws,
  TbServer2,
  TbTerminal2,
  TbShieldLock,
  TbCloud,
  TbWorld,
} from 'react-icons/tb';
import { FaHardDrive } from 'react-icons/fa6';

interface ProviderIconProps {
  provider: string;
  className?: string;
}

export const ProviderIcon: React.FC<ProviderIconProps> = ({ provider, className = 'w-6 h-6' }) => {
  const p = provider.toLowerCase();
  const iconCls = `${className} text-[var(--color-text-secondary)]`;

  switch (p) {
    case 'nextcloud':
      return <SiNextcloud className={iconCls} />;

    case 'opencloud':
      return (
        <svg className={iconCls} viewBox="0 0 24 24" fill="currentColor">
          <path d="M19.35 10.04C18.67 6.59 15.64 4 12 4 9.11 4 6.6 5.64 5.35 8.04 2.34 8.36 0 10.91 0 14c0 3.31 2.69 6 6 6h13c2.76 0 5-2.24 5-5 0-2.64-2.05-4.78-4.65-4.96z" />
        </svg>
      );

    case 'dropbox':
      return <SiDropbox className={iconCls} />;

    case 'google':
      return <SiGoogledrive className={iconCls} />;

    case 'onedrive':
      return <TbBrandOnedrive className={iconCls} />;

    case 's3':
      return <TbBrandAws className={iconCls} />;

    case 'smb':
      return <TbServer2 className={iconCls} />;

    case 'sftp':
      return <TbTerminal2 className={iconCls} />;

    case 'ftp':
      return <TbShieldLock className={iconCls} />;

    case 'immich':
      return <SiImmich className={iconCls} />;

    case 'magentacloud':
      return <SiDeutschetelekom className={iconCls} />;

    case 'hidrive':
      return <TbCloud className={iconCls} />;

    case 'webdav':
      return <TbWorld className={iconCls} />;

    case 'local':
      return <FaHardDrive className={iconCls} />;

    default:
      return <FaHardDrive className={iconCls} />;
  }
};
