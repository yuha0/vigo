import { Accessory, AccessoryInfo, MDNSAdvertiser, type PublishInfo } from "@homebridge/hap-nodejs";

/** The HAP-NodeJS operations that open sockets or touch persisted state, kept apart so tests can fake them. */
export interface HapHost {
  publish(accessory: Accessory, info: PublishInfo): Promise<void>;
  unpublish(accessory: Accessory): Promise<void>;
  /** Unpublishes and deletes the accessory's persisted pairings and identifiers. */
  destroy(accessory: Accessory): Promise<void>;
  /** Deletes persisted pairings and identifiers of an accessory that is not published. */
  forget(username: string): void;
  isPaired(username: string): boolean;
}

export const hapHost: HapHost = {
  publish: (accessory, info) => accessory.publish({ advertiser: MDNSAdvertiser.CIAO, ...info }),
  unpublish: (accessory) => accessory.unpublish(),
  destroy: (accessory) => accessory.destroy(),
  forget: (username) => Accessory.cleanupAccessoryData(username),
  isPaired: (username) => AccessoryInfo.load(username)?.paired() ?? false,
};
