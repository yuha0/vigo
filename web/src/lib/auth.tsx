import { useQueryClient } from "@tanstack/react-query";
import { createContext, use, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import { AuthService } from "@/gen/vigo/core/v1/auth_pb";
import { useClient } from "@/lib/backend";
import { clearImageCaches, onSessionLost } from "@/lib/session";

export type AuthStatus =
  | { kind: "checking" }
  | { kind: "setup" }
  | { kind: "login"; expired: boolean }
  | { kind: "ready" }
  | { kind: "unreachable"; error: unknown };

type AuthContextValue = {
  status: AuthStatus;
  minPasswordLength: number;
  /** Called once Login or SetupPassword succeeded. */
  signedIn(): void;
  logout(): Promise<void>;
  /** Re-reads the auth state, e.g. after an ambiguous unauthenticated answer. */
  recheck(): Promise<void>;
  /** Starts over after vigo couldn't be reached. */
  retry(): void;
};

const AuthContext = createContext<AuthContextValue | null>(null);

export function useAuth(): AuthContextValue {
  const v = use(AuthContext);
  if (!v) throw new Error("useAuth outside AuthProvider");
  return v;
}

/**
 * Owns the sign-in state. Nothing that needs a session is mounted until it is
 * "ready", and losing the session (any unauthenticated answer) drops back to
 * the login screen with every cached query cleared.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const client = useClient(AuthService);
  const qc = useQueryClient();
  const [status, setStatus] = useState<AuthStatus>({ kind: "checking" });
  const [minPasswordLength, setMin] = useState(8);
  const statusRef = useRef(status);
  useEffect(() => {
    statusRef.current = status;
  }, [status]);

  const check = useCallback(
    (expired = false) =>
      client.getAuthState({}).then(
        (s) => {
          if (s.minPasswordLength > 0) setMin(s.minPasswordLength);
          setStatus(!s.passwordSet ? { kind: "setup" } : s.authenticated ? { kind: "ready" } : { kind: "login", expired });
        },
        (error: unknown) => setStatus({ kind: "unreachable", error }),
      ),
    [client],
  );

  const drop = useCallback(() => {
    void qc.cancelQueries();
    qc.clear();
    void clearImageCaches();
  }, [qc]);

  useEffect(() => {
    void check();
    return onSessionLost(() => {
      if (statusRef.current.kind !== "ready") return;
      drop();
      setStatus({ kind: "login", expired: true });
      void check(true);
    });
  }, [check, drop]);

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      minPasswordLength,
      signedIn: () => {
        qc.clear();
        setStatus({ kind: "ready" });
      },
      logout: async () => {
        try {
          await client.logout({});
        } finally {
          drop();
          setStatus({ kind: "login", expired: false });
        }
      },
      retry: () => {
        setStatus({ kind: "checking" });
        void check();
      },
      recheck: async () => {
        const s = await client.getAuthState({}).catch(() => null);
        if (s && s.passwordSet && !s.authenticated && statusRef.current.kind === "ready") {
          drop();
          setStatus({ kind: "login", expired: true });
        }
      },
    }),
    [status, minPasswordLength, client, qc, drop, check],
  );

  return <AuthContext value={value}>{children}</AuthContext>;
}
