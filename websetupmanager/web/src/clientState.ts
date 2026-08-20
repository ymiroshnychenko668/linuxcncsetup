const CLIENT_ID_KEY = 'web-setup-manager.client-id.v1'
const CLIENT_ID_PATTERN = /^[A-Za-z0-9._:-]{1,128}$/

function randomClientID(): string {
  if (typeof crypto.randomUUID === 'function') return `web:${crypto.randomUUID()}`
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return `web:${Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')}`
}

export function stableClientID(): string {
  try {
    const stored = window.localStorage.getItem(CLIENT_ID_KEY)
    if (stored && CLIENT_ID_PATTERN.test(stored)) return stored
    const created = randomClientID()
    window.localStorage.setItem(CLIENT_ID_KEY, created)
    return created
  } catch {
    return randomClientID()
  }
}
