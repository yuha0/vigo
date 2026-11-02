import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import { toast } from "sonner";

import { ArtService, type Status } from "@/gen/vigo/core/v1/core_pb";
import { useClient } from "@/lib/backend";
import { describeError } from "@/lib/errors";
import { qk, setStatus, useShowArt } from "@/lib/queries";
import { tvState } from "@/lib/tv-state";

/**
 * SelectArt without touching the cached Status: "Selected" must only ever
 * reflect what the TV has confirmed, so the badge moves when the response or
 * WatchEvents says so.
 */
function useSelectArt() {
  const client = useClient(ArtService);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (contentId: string) => client.selectArt({ contentId }),
    onSuccess: (res) => setStatus(qc, res.status),
    meta: { silent: true },
  });
}

/**
 * Selecting artwork with an honest pending state: the tapped item is "pending"
 * until the TV answers, and the confirmed selection is left alone meanwhile.
 * When the TV is asleep or off, SelectArt fails with failed_precondition and the
 * error toast offers Show art for the same artwork, which wakes the TV.
 */
export function useSelectArtwork() {
  const qc = useQueryClient();
  const select = useSelectArt();
  const showArt = useShowArt();
  const [pendingId, setPendingId] = useState<string | null>(null);

  const wake = useCallback(
    (contentId: string) => {
      setPendingId(contentId);
      showArt.mutate(contentId, {
        onSuccess: () => toast.success("Showing art", { description: "The TV is waking up with this artwork.", id: "selected" }),
        onSettled: () => setPendingId(null),
      });
    },
    [showArt],
  );

  const selectArtwork = useCallback(
    (contentId: string) => {
      if (pendingId) return;
      setPendingId(contentId);
      select.mutate(contentId, {
        onSuccess: () => toast.success("Selected", { description: "The panel catches up within a few minutes.", id: "selected" }),
        onError: (err) => {
          const status = qc.getQueryData<Status | null>(qk.status);
          const e = ConnectError.from(err);
          const state = tvState(status);
          const offerShowArt = e.code === Code.FailedPrecondition && state.canShowArt;
          const d = offerShowArt
            ? { title: "The TV isn't awake", description: e.rawMessage ? `${e.rawMessage[0].toUpperCase()}${e.rawMessage.slice(1)}.` : state.body }
            : describeError(err, { action: "Select the artwork", status });
          if (!d) return;
          toast.error(d.title, {
            id: "select-error",
            description: d.description,
            action: offerShowArt ? { label: "Show art", onClick: () => wake(contentId) } : undefined,
          });
        },
        onSettled: () => setPendingId(null),
      });
    },
    [pendingId, qc, select, wake],
  );

  return { selectArtwork, pendingId };
}
