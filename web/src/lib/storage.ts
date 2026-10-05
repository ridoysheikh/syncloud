// Per-viewer UI preferences only (collapsed nav, drawer state). Storage can be
// unavailable (private mode, blocked site data), so every access is guarded.

export function readPref<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(`syncloud.${key}`);
    return raw === null ? fallback : (JSON.parse(raw) as T);
  } catch {
    return fallback;
  }
}

export function writePref(key: string, value: unknown) {
  try {
    localStorage.setItem(`syncloud.${key}`, JSON.stringify(value));
  } catch {
    // ignore
  }
}
