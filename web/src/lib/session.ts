import { Code, ConnectError, type Interceptor } from "@connectrpc/connect";

type Listener = () => void;
const listeners = new Set<Listener>();

/** Subscribes to "the session is gone": any call answered unauthenticated. */
export function onSessionLost(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function notifySessionLost() {
  for (const fn of listeners) fn();
}

export function isUnauthenticated(err: unknown): boolean {
  return err != null && ConnectError.from(err).code === Code.Unauthenticated;
}

const AUTH_SERVICE = "vigo.core.v1.AuthService";

/**
 * Reports unauthenticated answers from every service except AuthService,
 * where it just means a wrong password. Streams report their own errors, since
 * they surface while iterating rather than here.
 */
export const sessionInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (err) {
    if (req.service.typeName !== AUTH_SERVICE && isUnauthenticated(err)) notifySessionLost();
    throw err;
  }
};

/** Image caches the service worker keeps; dropped on sign-out so a signed-out device can't show them. */
export const imageCaches = ["thumbnails", "library-immutable"];

export async function clearImageCaches() {
  if (typeof caches === "undefined") return;
  await Promise.all(imageCaches.map((name) => caches.delete(name).catch(() => false)));
}
