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

  switch (p) {
    case 'nextcloud':
      return <SiNextcloud className={`${className} text-[#0082C9]`} />;

    case 'dropbox':
      return <SiDropbox className={`${className} text-[#0061FF]`} />;

    case 'google':
      return <SiGoogledrive className={`${className} text-[#4285F4]`} />;

    case 'onedrive':
      return <TbBrandOnedrive className={`${className} text-[#0078D4]`} />;

    case 's3':
      return <TbBrandAws className={`${className} text-[#FF9900]`} />;

    case 'smb':
      return <TbServer2 className={`${className} text-[#10B981]`} />;

    case 'sftp':
      return <TbTerminal2 className={`${className} text-[#6366F1]`} />;

    case 'ftp':
      return <TbShieldLock className={`${className} text-[#06B6D4]`} />;

    case 'immich':
      return <SiImmich className={`${className} text-[#3B82F6]`} />;

    case 'magentacloud':
      return <SiDeutschetelekom className={`${className} text-[#E20074]`} />;

    case 'hidrive':
      return <TbCloud className={`${className} text-[#E60000]`} />;

    case 'webdav':
      return <TbWorld className={`${className} text-[#14B8A6]`} />;

    case 'local':
      return <FaHardDrive className={`${className} text-[#64748B]`} />;

    default:
      return <FaHardDrive className={className} />;
  }
};
