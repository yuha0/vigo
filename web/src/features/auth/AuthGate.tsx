import type { ReactNode } from "react";

import { CheckingScreen, LoginScreen, SetupScreen, UnreachableScreen } from "@/features/auth/AuthScreen";
import { useAuth } from "@/lib/auth";

/** Renders the app only with a session; otherwise the setup or login screen. */
export function AuthGate({ children }: { children: ReactNode }) {
  const { status, retry } = useAuth();
  switch (status.kind) {
    case "checking":
      return <CheckingScreen />;
    case "setup":
      return <SetupScreen />;
    case "login":
      return <LoginScreen key={String(status.expired)} expired={status.expired} />;
    case "unreachable":
      return <UnreachableScreen error={status.error} onRetry={retry} />;
    case "ready":
      return children;
  }
}
