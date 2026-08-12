const safeAvatarDataUrl = /^data:image\/(?:png|jpeg|webp|gif);base64,[a-z0-9+/]+=*$/i;

export function safeAvatarUrl(value: string | undefined): string | undefined {
  if (!value) return undefined;
  if (safeAvatarDataUrl.test(value)) return value;

  try {
    return new URL(value).protocol === 'https:' ? value : undefined;
  } catch {
    return undefined;
  }
}
