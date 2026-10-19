// One-time move of browser state saved under the old "bolter." prefix to
// "keeluni.", so chart drawings, chosen accounts, the hedge pair and other
// preferences survive the rename. Runs before the app reads anything.

const OLD = "bolter.";
const NEW = "keeluni.";

export function migrateStorage(store: Storage | undefined = globalThis.localStorage): void {
  if (!store) return;
  try {
    const keys: string[] = [];
    for (let i = 0; i < store.length; i++) {
      const key = store.key(i);
      if (key?.startsWith(OLD)) keys.push(key);
    }
    for (const key of keys) {
      const target = NEW + key.slice(OLD.length);
      const value = store.getItem(key);
      if (value !== null && store.getItem(target) === null) store.setItem(target, value);
      store.removeItem(key);
    }
  } catch {
    // Storage disabled or full: nothing to carry over.
  }
}
