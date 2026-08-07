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
