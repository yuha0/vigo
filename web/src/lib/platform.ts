export function isIOS(nav: Pick<Navigator, "userAgent" | "maxTouchPoints"> = navigator): boolean {
  if (/iPhone|iPad|iPod/.test(nav.userAgent)) return true;
  // iPadOS reports a desktop Mac user agent; touch support gives it away.
  return /Macintosh/.test(nav.userAgent) && nav.maxTouchPoints > 1;
}

export function haptic(pattern: number | number[] = 8) {
  if (typeof navigator !== "undefined" && "vibrate" in navigator) {
    try {
      navigator.vibrate(pattern);
    } catch {
      // Some browsers throw when vibration is blocked by user settings.
    }
  }
}

export function isStandalone(): boolean {
  return (
    window.matchMedia?.("(display-mode: standalone)").matches ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true
  );
}
