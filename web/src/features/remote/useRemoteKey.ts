import { useMutation } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, type KeyboardEvent, type MouseEvent, type PointerEvent } from "react";

import { KeyAction, TVService } from "@/gen/vigo/core/v1/core_pb";
import { useClient } from "@/lib/backend";
import { haptic } from "@/lib/platform";

/** How long a press must last before it becomes a hold. */
const HOLD_MS = 350;

export function useSendKey() {
  const client = useClient(TVService);
  return useMutation({
    mutationFn: ({ key, action }: { key: string; action: KeyAction }) => client.sendKey({ key, action }),
    meta: { action: "Send the key" },
  });
}

/**
 * Pointer handlers for one remote button: a tap sends CLICK; holding sends
 * PRESS, then RELEASE when the finger lifts or leaves.
 */
export function useRemoteKey(key: string, send: (key: string, action: KeyAction) => void) {
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const held = useRef(false);
  const down = useRef(false);

  const finish = useCallback(
    (cancelled: boolean) => {
      if (!down.current) return;
      down.current = false;
      clearTimeout(timer.current);
      if (held.current) send(key, KeyAction.RELEASE);
      else if (!cancelled) send(key, KeyAction.CLICK);
      held.current = false;
    },
    [key, send],
  );

  useEffect(() => () => finish(true), [finish]);

  return {
    onPointerDown: (e: PointerEvent<HTMLElement>) => {
      if (e.button !== 0) return;
      e.currentTarget.setPointerCapture?.(e.pointerId);
      down.current = true;
      held.current = false;
      haptic(8);
      timer.current = setTimeout(() => {
        held.current = true;
        haptic([4, 30, 4]);
        send(key, KeyAction.PRESS);
      }, HOLD_MS);
    },
    onPointerUp: () => finish(false),
    onPointerCancel: () => finish(true),
    onLostPointerCapture: () => finish(true),
    onContextMenu: (e: MouseEvent) => e.preventDefault(),
    // Keyboard and assistive-technology activation arrive as clicks with no pointer.
    onClick: (e: MouseEvent) => {
      if (e.detail === 0) send(key, KeyAction.CLICK);
    },
    onKeyDown: (e: KeyboardEvent) => {
      if (e.key === " ") e.preventDefault();
    },
  };
}
