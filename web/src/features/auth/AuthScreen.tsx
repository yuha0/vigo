import { Code, ConnectError } from "@connectrpc/connect";
import { LoaderCircle, LockKeyhole, RefreshCw } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";

import { Mark } from "@/components/Mark";
import { Button } from "@/components/ui/button";
import { AuthService } from "@/gen/vigo/core/v1/auth_pb";
import { PasswordField } from "@/features/auth/PasswordField";
import { useAuth } from "@/lib/auth";
import { useClient } from "@/lib/backend";
import { describeError } from "@/lib/errors";

function Shell({ title, description, children }: { title: string; description: ReactNode; children: ReactNode }) {
  return (
    <main className="flex min-h-dvh items-center justify-center px-4 pt-[max(2rem,env(safe-area-inset-top))] pb-[max(2rem,env(safe-area-inset-bottom))]">
      <div className="w-full max-w-sm animate-fade-up">
        <Mark className="mb-8 size-12" />
        <h1 className="font-display text-[2rem] leading-tight">{title}</h1>
        <div className="mt-3 text-[0.9375rem] text-muted-foreground">{description}</div>
        <div className="mt-8">{children}</div>
      </div>
    </main>
  );
}

function authError(err: unknown, wrongPassword: string): string {
  const e = ConnectError.from(err);
  if (e.code === Code.ResourceExhausted) return "Too many attempts. Wait a moment, then try again.";
  if (e.code === Code.Unauthenticated) return wrongPassword;
  if (e.code === Code.InvalidArgument && e.rawMessage) return e.rawMessage;
  const d = describeError(err);
  return [d?.title, d?.description].filter(Boolean).join(". ");
}

export function SetupScreen() {
  const { minPasswordLength: min, signedIn } = useAuth();
  const client = useClient(AuthService);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [serverError, setServerError] = useState<string | null>(null);

  const tooShort = password.length < min;
  const mismatch = confirm !== password;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    if (tooShort || mismatch) return;
    setBusy(true);
    setServerError(null);
    try {
      await client.setupPassword({ password });
      signedIn();
    } catch (err) {
      setServerError(authError(err, "That didn't work. Try again."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Shell
      title="Set a password for vigo"
      description={
        <>
          <p>Welcome. Choose a password to protect this web app; anyone who opens vigo will need it to control the TV.</p>
          <p className="mt-2">There's no username, just this one password.</p>
        </>
      }
    >
      <form className="grid gap-5" onSubmit={(e) => void submit(e)} noValidate>
        <PasswordField
          label="Password"
          value={password}
          onChange={setPassword}
          autoComplete="new-password"
          autoFocus
          showStrength
          minLength={min}
          hint={`At least ${min} characters. A few unrelated words make a good one.`}
          error={tried && tooShort ? `Use at least ${min} characters.` : undefined}
        />
        <PasswordField
          label="Confirm password"
          value={confirm}
          onChange={setConfirm}
          autoComplete="new-password"
          error={tried && !tooShort && mismatch ? "The passwords don't match." : undefined}
        />
        {serverError && (
          <p role="alert" className="text-sm text-destructive">
            {serverError}
          </p>
        )}
        <Button type="submit" size="lg" disabled={busy}>
          {busy && <LoaderCircle className="animate-spin" />}
          Set password
        </Button>
      </form>
    </Shell>
  );
}

export function LoginScreen({ expired }: { expired: boolean }) {
  const { signedIn } = useAuth();
  const client = useClient(AuthService);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!password) return;
    setBusy(true);
    setError(null);
    try {
      await client.login({ password });
      signedIn();
    } catch (err) {
      setError(authError(err, "That password isn't right."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Shell title="Welcome back" description={expired ? "Your session ended. Sign in again to continue." : "Enter vigo's password to continue."}>
      <form className="grid gap-5" onSubmit={(e) => void submit(e)}>
        <PasswordField label="Password" value={password} onChange={setPassword} autoComplete="current-password" autoFocus error={error ?? undefined} />
        <Button type="submit" size="lg" disabled={busy || !password}>
          {busy ? <LoaderCircle className="animate-spin" /> : <LockKeyhole />}
          Sign in
        </Button>
      </form>
    </Shell>
  );
}

export function UnreachableScreen({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const d = describeError(error);
  return (
    <Shell title="Can't reach vigo" description={d?.description ?? "vigo isn't answering. Check your connection."}>
      <Button size="lg" variant="outline" onClick={onRetry}>
        <RefreshCw /> Try again
      </Button>
    </Shell>
  );
}

export function CheckingScreen() {
  return (
    <main className="flex min-h-dvh items-center justify-center" aria-busy="true" aria-label="Loading">
      <Mark className="size-12 animate-pulse" />
    </main>
  );
}
