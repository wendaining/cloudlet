const maxURLBytes = 4096;

export function browserNavigationURL(input: unknown): string {
  const value = typeof input === 'string' ? input.trim() : '';
  if (!value) throw new Error('请输入导航地址');
  if (new TextEncoder().encode(value).length > maxURLBytes) {
    throw new Error('导航地址最多 4096 个字节');
  }
  const authority = value.match(/^https?:\/\/([^/?#]+)/iu)?.[1];
  if (
    !authority ||
    authority.includes('@') ||
    /[\\\s]/u.test(value) ||
    !URL.canParse(value)
  ) {
    throw new Error('请输入不带凭据的 HTTP 或 HTTPS 地址');
  }
  return value;
}
