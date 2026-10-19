import { useEffect, useState } from "react";

// A minimal path router: the app has two top-level pages (/ and /hedge), so
// history.pushState plus a popstate listener is all it needs.

const listeners = new Set<() => void>();

export function navigate(path: string) {
  if (location.pathname === path) return;
  history.pushState(null, "", path);
  for (const listener of listeners) listener();
}

export function usePath(): string {
  const [path, setPath] = useState(() => location.pathname);
  useEffect(() => {
    const update = () => setPath(location.pathname);
    listeners.add(update);
    window.addEventListener("popstate", update);
    return () => {
      listeners.delete(update);
      window.removeEventListener("popstate", update);
    };
  }, []);
  return path;
}
