import { Code, ConnectError } from "@connectrpc/connect";
import { LoaderCircle } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { AuthService } from "@/gen/vigo/core/v1/auth_pb";
import { PasswordField } from "@/features/auth/PasswordField";
import { useAuth } from "@/lib/auth";
import { useClient } from "@/lib/backend";
import { describeError } from "@/lib/errors";

export function ChangePasswordCard() {
  const { minPasswordLength: min, recheck } = useAuth();
  const client = useClient(AuthService);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ field: "current" | "form"; message: string } | null>(null);

  const tooShort = next.length < min;
  const mismatch = confirm !== next;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    if (!current || tooShort || mismatch) return;
    setBusy(true);
    setError(null);
    try {
      await client.changePassword({ currentPassword: current, newPassword: next });
      toast.success("Password changed", { description: "Other devices were signed out. This one stays signed in." });
      setCurrent("");
      setNext("");
      setConfirm("");
      setTried(false);
    } catch (err) {
      const code = ConnectError.from(err).code;
      if (code === Code.Unauthenticated || code === Code.PermissionDenied) {
        setError({ field: "current", message: "The current password isn't right." });
        // An unauthenticated answer may also mean this session itself ended.
        void recheck();
      } else if (code === Code.ResourceExhausted) {
        setError({ field: "form", message: "Too many attempts. Wait a moment, then try again." });
      } else {
        const d = describeError(err);
        setError({ field: "form", message: ConnectError.from(err).rawMessage || d?.title || "Couldn't change the password." });
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardContent>
        <form className="grid gap-5" onSubmit={(e) => void submit(e)} noValidate aria-label="Change password">
          <p className="text-sm text-muted-foreground">
            Changing the password signs out every other device that's signed in to vigo. This one stays signed in.
          </p>
          <PasswordField
            label="Current password"
            value={current}
            onChange={setCurrent}
            autoComplete="current-password"
            error={error?.field === "current" ? error.message : tried && !current ? "Enter your current password." : undefined}
          />
          <PasswordField
            label="New password"
            value={next}
            onChange={setNext}
            autoComplete="new-password"
            showStrength
            minLength={min}
            error={tried && tooShort ? `Use at least ${min} characters.` : undefined}
          />
          <PasswordField
            label="Confirm new password"
            value={confirm}
            onChange={setConfirm}
            autoComplete="new-password"
            error={tried && !tooShort && mismatch ? "The passwords don't match." : undefined}
          />
          {error?.field === "form" && (
            <p role="alert" className="text-sm text-destructive">
              {error.message}
            </p>
          )}
          <div className="flex justify-end">
            <Button type="submit" disabled={busy}>
              {busy && <LoaderCircle className="animate-spin" />}
              Change password
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
