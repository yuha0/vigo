import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, type ConnectRouter, type ServiceImpl } from "@connectrpc/connect";
import {
  AccessoryKind,
  AccessorySchema,
  HomeService,
  PairingGroupSchema,
  Protocol,
  type Accessory,
  type PairingGroup,
} from "../gen/vigo/home/v1/home_pb";
import {
  ProviderError,
  type AccessoryKindName,
  type AccessoryView,
  type PairingGroupView,
  type Provider,
  type ProtocolName,
} from "../provider";

/** What HomeService needs to know about this process. */
export interface HomeRuntime {
  readonly protocol: ProtocolName;
  readonly implemented: boolean;
  readonly replica: string;
  /** The provider, while this replica is active. */
  provider(): Provider | undefined;
  leader(): boolean;
  coreConnected(): boolean;
}

const PROTOCOLS: Record<ProtocolName, Protocol> = {
  homekit: Protocol.HOMEKIT,
  matter: Protocol.MATTER,
};

const KINDS: Record<AccessoryKindName, AccessoryKind> = {
  television: AccessoryKind.TELEVISION,
  switch: AccessoryKind.SWITCH,
  lightbulb: AccessoryKind.LIGHTBULB,
};

const CODES: Record<ProviderError["code"], Code> = {
  not_found: Code.NotFound,
  invalid_argument: Code.InvalidArgument,
  unimplemented: Code.Unimplemented,
  unavailable: Code.Unavailable,
  internal: Code.Internal,
};

function toAccessory(a: AccessoryView): Accessory {
  return create(AccessorySchema, {
    id: a.id,
    name: a.name,
    description: a.description,
    kind: KINDS[a.kind],
    enabled: a.enabled,
    canDisable: a.canDisable,
    pairingGroupId: a.pairingGroupId,
  });
}

function toGroup(g: PairingGroupView): PairingGroup {
  return create(PairingGroupSchema, {
    id: g.id,
    name: g.name,
    advertised: g.advertised,
    paired: g.paired,
    setupCode: g.setupCode,
    setupUri: g.setupUri,
    accessoryIds: g.accessoryIds,
  });
}

function wrap(err: unknown): never {
  if (err instanceof ConnectError) throw err;
  if (err instanceof ProviderError) throw new ConnectError(err.message, CODES[err.code]);
  throw ConnectError.from(err, Code.Internal);
}

export function homeService(rt: HomeRuntime): ServiceImpl<typeof HomeService> {
  const active = (): Provider => {
    const p = rt.provider();
    if (!p) throw new ConnectError(`replica ${rt.replica} is on standby`, Code.Unavailable);
    return p;
  };
  return {
    getProvider() {
      return {
        provider: {
          protocol: PROTOCOLS[rt.protocol],
          implemented: rt.implemented,
          leader: rt.leader(),
          replica: rt.replica,
          coreConnected: rt.coreConnected(),
        },
      };
    },
    listAccessories() {
      return { accessories: active().listAccessories().map(toAccessory) };
    },
    async setAccessoryEnabled(req) {
      try {
        return { accessory: toAccessory(await active().setAccessoryEnabled(req.id, req.enabled)) };
      } catch (err) {
        wrap(err);
      }
    },
    listPairingGroups() {
      return { groups: active().listPairingGroups().map(toGroup) };
    },
    async resetPairing(req) {
      try {
        return { group: toGroup(await active().resetPairing(req.groupId)) };
      } catch (err) {
        wrap(err);
      }
    },
  };
}

export function homeRoutes(rt: HomeRuntime): (router: ConnectRouter) => void {
  return (router) => {
    router.service(HomeService, homeService(rt));
  };
}
