import { useId, useState } from "react";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { PairingGroup } from "@/gen/vigo/home/v1/home_pb";

const PHRASE = "reset";

export function ResetPairingDialog({ group, onConfirm, pending }: { group: PairingGroup; onConfirm: () => void; pending: boolean }) {
  const [typed, setTyped] = useState("");
  const inputId = useId();
  const ok = typed.trim().toLowerCase() === PHRASE;

  return (
    <AlertDialog onOpenChange={(o) => !o && setTyped("")}>
      <AlertDialogTrigger asChild>
        <Button variant="destructive-outline" disabled={pending}>
          Reset pairing
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Reset {group.name} pairing?</AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="grid gap-2">
              <p>
                Apple Home will forget every device paired with this group, and vigo will issue a new setup code. The accessories disappear
                from the Home app, along with their rooms, scenes and automations.
              </p>
              <p>To use them again you'll have to add them in the Home app by scanning the new code.</p>
            </div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="grid gap-2">
          <label htmlFor={inputId} className="text-sm">
            Type <span className="font-mono font-semibold">{PHRASE}</span> to confirm
          </label>
          <Input id={inputId} value={typed} onChange={(e) => setTyped(e.target.value)} autoCapitalize="off" autoCorrect="off" autoComplete="off" spellCheck={false} />
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction disabled={!ok} onClick={onConfirm}>
            Reset pairing
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
