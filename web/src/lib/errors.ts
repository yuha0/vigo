import { Code, ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";

import type { Status } from "@/gen/vigo/core/v1/core_pb";
import { tvState } from "@/lib/tv-state";

export type ErrorDescription = { title: string; description?: string };

function sentence(s: string): string {
  const t = s.trim();
  if (!t) return t;
  const capped = t[0].toUpperCase() + t.slice(1);
  return /[.!?]$/.test(capped) ? capped : `${capped}.`;
}

export function describeError(err: unknown, opts: { action?: string; status?: Status | null } = {}): ErrorDescription | null {
  const e = ConnectError.from(err);
  const action = opts.action;
  const raw = e.rawMessage ? sentence(e.rawMessage) : "";
  switch (e.code) {
    case Code.Canceled:
      return null;
    case Code.FailedPrecondition: {
      const state = tvState(opts.status);
      const explain = state.kind === "loading" ? "" : `${state.title}: ${state.body}`;
      return {
        title: action ? `Couldn't ${action.toLowerCase()} right now` : "The TV isn't ready for that",
        description: [raw, explain].filter(Boolean).join(" ") || undefined,
      };
    }
    case Code.DeadlineExceeded:
      return {
        title: "The TV didn't answer in time",
        description: "It may be busy or waking up. vigo didn't retry, so check the TV before trying again.",
      };
    case Code.Unavailable:
      return {
        title: action ? `Couldn't ${action.toLowerCase()}` : "vigo is unreachable",
        description: raw || "vigo couldn't be reached. Check your connection.",
      };
    case Code.InvalidArgument:
      return { title: action ? `Couldn't ${action.toLowerCase()}` : "That request was rejected", description: raw || undefined };
    case Code.AlreadyExists:
      return {
        title: "That name is already taken",
        description: `${raw ? raw + " " : ""}vigo never overwrites files; rename the photo or choose another folder.`,
      };
    case Code.ResourceExhausted:
      return { title: "Too large", description: raw || "The file is over the 64 MB limit." };
    case Code.NotFound:
      return { title: "Not found", description: raw || "It may have just been removed." };
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return { title: "Not allowed", description: raw || undefined };
    default:
      return {
        title: action ? `Couldn't ${action.toLowerCase()}` : "Something went wrong",
        description: raw || undefined,
      };
  }
}

export function toastError(err: unknown, opts: { action?: string; status?: Status | null } = {}) {
  const d = describeError(err, opts);
  if (!d) return;
  // One toast per kind of failure, so a held remote key can't stack dozens.
  toast.error(d.title, { description: d.description, id: `err:${d.title}` });
}

export function isUnavailable(err: unknown): boolean {
  return err != null && ConnectError.from(err).code === Code.Unavailable;
}
