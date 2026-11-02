import { Eye, EyeOff } from "lucide-react";
import { useId, useState } from "react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { passwordStrength } from "@/features/auth/strength";
import { cn } from "@/lib/utils";

export function PasswordField({
  label,
  value,
  onChange,
  error,
  hint,
  autoComplete,
  autoFocus,
  showStrength,
  minLength,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  hint?: string;
  autoComplete: "current-password" | "new-password";
  autoFocus?: boolean;
  showStrength?: boolean;
  minLength?: number;
}) {
  const id = useId();
  const [visible, setVisible] = useState(false);
  const strength = showStrength && value ? passwordStrength(value, minLength ?? 0) : null;
  const describedBy = [error && `${id}-error`, (hint || strength) && `${id}-hint`].filter(Boolean).join(" ") || undefined;

  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{label}</Label>
      <div className="relative">
        <Input
          id={id}
          type={visible ? "text" : "password"}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          autoComplete={autoComplete}
          autoFocus={autoFocus}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          aria-invalid={!!error || undefined}
          aria-describedby={describedBy}
          className="pr-12"
        />
        <button
          type="button"
          onClick={() => setVisible((v) => !v)}
          aria-label={visible ? `Hide ${label.toLowerCase()}` : `Show ${label.toLowerCase()}`}
          aria-pressed={visible}
          className="absolute top-0 right-0 flex size-11 items-center justify-center rounded-xl text-muted-foreground outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          {visible ? <EyeOff className="size-[1.125rem]" /> : <Eye className="size-[1.125rem]" />}
        </button>
      </div>
      {strength && (
        <div className="flex items-center gap-3" aria-hidden="true">
          <div className="grid flex-1 grid-cols-4 gap-1">
            {[1, 2, 3, 4].map((i) => (
              <span
                key={i}
                className={cn(
                  "h-1 rounded-full bg-muted transition-colors",
                  i <= strength.score && (strength.score <= 1 ? "bg-destructive" : strength.score === 2 ? "bg-warning" : "bg-success"),
                )}
              />
            ))}
          </div>
          <span className="w-16 text-right text-xs text-muted-foreground">{strength.label}</span>
        </div>
      )}
      {(hint || strength) && (
        <p id={`${id}-hint`} className={cn("text-sm text-muted-foreground", strength && !hint && "sr-only")}>
          {strength ? `Strength: ${strength.label}. ` : ""}
          {hint}
        </p>
      )}
      {error && (
        <p id={`${id}-error`} role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
