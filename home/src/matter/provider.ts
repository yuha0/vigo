import {
  ProviderError,
  type AccessoryView,
  type PairingGroupView,
  type Provider,
} from "../provider";

// Apple Home surfaces no TV device type over Matter, so this protocol is a placeholder.
export class MatterProvider implements Provider {
  readonly protocol = "matter" as const;
  readonly implemented = false;

  start(): Promise<void> {
    return Promise.resolve();
  }

  stop(): Promise<void> {
    return Promise.resolve();
  }

  listAccessories(): AccessoryView[] {
    return [];
  }

  setAccessoryEnabled(): Promise<AccessoryView> {
    return Promise.reject(new ProviderError("unimplemented", "the matter provider is not implemented"));
  }

  listPairingGroups(): PairingGroupView[] {
    return [];
  }

  resetPairing(): Promise<PairingGroupView> {
    return Promise.reject(new ProviderError("unimplemented", "the matter provider is not implemented"));
  }
}
