/**
 * Account avatar placeholder via ui-avatars.com (user requirement): initials
 * on the violet nav-pill fill (#8140DC) + white bold text, so the avatar
 * echoes the header's active-state color. Name is form-urlencoded the way
 * the service expects (spaces as `+`).
 */
export function avatarUrl(name: string): string {
  const trimmed = name.trim() || '?'
  const params = new URLSearchParams({
    name: trimmed,
    background: '8140DC',
    color: 'FFFFFF',
    size: '128',
    bold: 'true',
  })
  return `https://ui-avatars.com/api/?${params.toString()}`
}
