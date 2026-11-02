import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport, type Interceptor } from "@connectrpc/connect";

import {
  ArtService,
  ConfigService,
  ListMattesResponseSchema,
  PairingService,
  TVService,
} from "@/gen/vigo/core/v1/core_pb";
import { LibraryService, type LibraryArtwork } from "@/gen/vigo/core/v1/library_pb";
import { AuthService } from "@/gen/vigo/core/v1/auth_pb";
import { HomeService } from "@/gen/vigo/home/v1/home_pb";
import type { ArtApi } from "@/lib/art-api";
import type { Backend } from "@/lib/backend";
import { notifySessionLost, sessionInterceptor } from "@/lib/session";
import { decodeOriented, drawPadded } from "@/lib/image";
import { generatedArtUrl } from "@/lib/mock/art";
import { type HomeMode, type MockControl, type Scenario } from "@/lib/mock/control";
import { DEV_PASSWORD, MockAuth, type AuthMode } from "@/lib/mock/auth";
import { MockHome } from "@/lib/mock/home";
import { MockLibrary, type MockSources } from "@/lib/mock/library";
import { MockTv, type MockTvOptions } from "@/lib/mock/tv";

export type MockBackendOptions = Partial<MockTvOptions> & {
  /** Added to every RPC, to make loading states visible. */
  latencyMs?: number;
  homeMode?: HomeMode;
  /** "setup": no password yet; "login": password set, signed out; "ok": signed in (default). */
  auth?: AuthMode;
  /** Which library sources exist: two writable ones, one, or only read-only ones. */
  sources?: MockSources;
};

export type MockBackend = Backend & {
  mock: MockControl;
  tv: MockTv;
  home: MockHome;
  library: MockLibrary;
  auth: MockAuth;
  /** Every RPC the UI made, as "Service/Method", for tests. */
  calls: string[];
};

const sleep = (ms: number) => (ms > 0 ? new Promise<void>((r) => setTimeout(r, ms)) : Promise.resolve());

type Decoded = { width: number; height: number; thumbnail: string | null };

/** Reads an uploaded file's upright size and renders a padded 16:9 thumbnail, as vigo-core would. */
async function decode(file: File): Promise<Decoded> {
  try {
    const bmp = await decodeOriented(file);
    const { width, height } = bmp;
    let thumbnail: string | null = null;
    const canvas = document.createElement("canvas");
    canvas.width = 640;
    canvas.height = 360;
    const ctx = canvas.getContext("2d");
    if (ctx) {
      drawPadded(ctx, bmp, canvas.width, canvas.height);
      const blob = await new Promise<Blob | null>((r) => canvas.toBlob(r, "image/jpeg", 0.85));
      thumbnail = blob ? URL.createObjectURL(blob) : null;
    }
    bmp.close();
    return { width, height, thumbnail };
  } catch {
    return { width: 3840, height: 2160, thumbnail: null };
  }
}

/**
 * A complete in-browser stand-in for vigo-core and vigo-home, so the UI can be
 * developed and tested without a backend or a TV.
 */
export function createMockBackend(options: MockBackendOptions = {}): MockBackend {
  const latency = options.latencyMs ?? 0;
  const tv = new MockTv({
    scenario: options.scenario ?? "art",
    transitionMs: options.transitionMs ?? 0,
    pairingDelayMs: options.pairingDelayMs ?? 0,
    pairingOutcome: options.pairingOutcome ?? "allow",
    motionMinuteMs: options.motionMinuteMs ?? null,
    stubbornKeys: options.stubbornKeys ?? ["color_temperature"],
    settings: options.settings,
    uploads: options.uploads,
  });
  const home = new MockHome(options.homeMode ?? "online");
  const library = new MockLibrary(tv, {
    sources: options.sources ?? "default",
    transitionMs: options.transitionMs ?? 0,
  });
  const uploaded = new Map<string, { thumbnail: string; image: string }>();

  const auth = new MockAuth(options.auth ?? "ok");
  const calls: string[] = [];
  // Every non-auth RPC needs a session, like vigo-core's browser listener.
  const wait = async () => {
    await sleep(latency);
    auth.require();
  };
  const record: Interceptor = (next) => (req) => {
    calls.push(`${req.service.name}/${req.method.name}`);
    return next(req);
  };

  const transport = createRouterTransport(({ service }) => {
    service(AuthService, {
      async getAuthState() {
        await sleep(latency);
        return auth.state();
      },
      async setupPassword(req) {
        await sleep(latency);
        auth.setup(req.password);
        return {};
      },
      async login(req) {
        await sleep(latency);
        auth.login(req.password);
        return {};
      },
      async logout() {
        await sleep(latency);
        auth.logout();
        return {};
      },
      async changePassword(req) {
        await sleep(latency);
        auth.change(req.currentPassword, req.newPassword);
        return {};
      },
    });
    service(TVService, {
      async getStatus() {
        await wait();
        return { status: tv.status };
      },
      async refresh() {
        await wait();
        return { status: tv.refresh() };
      },
      watchEvents(_req, ctx) {
        auth.require();
        return tv.events(ctx.signal);
      },
      async showArt(req) {
        await wait();
        return { status: await tv.showArt(req.contentId) };
      },
      async setArtMode(req) {
        await wait();
        return { status: await tv.setArtMode(req.on) };
      },
      async listSettings() {
        await wait();
        return { settings: tv.listSettings() };
      },
      async updateSetting(req) {
        await wait();
        return tv.updateSetting(req);
      },
      async sendKey(req) {
        await sleep(Math.min(latency, 60));
        auth.require();
        tv.sendKey(req.key, req.action);
        return {};
      },
    });
    service(ArtService, {
      async listArt(req) {
        await wait();
        return tv.listArt(req.categoryId);
      },
      async selectArt(req) {
        await wait();
        return { status: await tv.selectArt(req.contentId) };
      },
      async deleteArt(req) {
        await wait();
        return { deletedContentIds: await tv.deleteArt(req.contentIds) };
      },
      async listMattes() {
        await wait();
        return create(ListMattesResponseSchema, tv.listMattes());
      },
      async changeMatte() {
        await wait();
        return tv.changeMatte();
      },
    });
    service(LibraryService, {
      async listArtworks(req) {
        await wait();
        return library.listArtworks(req);
      },
      async getArtwork(req) {
        await wait();
        return { artwork: library.getArtwork(req.id) };
      },
      async listTags(req) {
        await wait();
        return { tags: library.listTags(req.prefix) };
      },
      async updateTags(req) {
        await wait();
        return { artwork: library.updateTags(req.artworkId, req.add, req.remove) };
      },
      async deleteArtwork(req) {
        await wait();
        return { artwork: library.deleteArtwork(req.id) };
      },
      async cropArtwork(req) {
        await wait();
        return { artwork: library.cropArtwork(req.id, req.rect) };
      },
      async listSources() {
        await wait();
        return { sources: library.listSources() };
      },
      async syncSource(req) {
        await wait();
        library.syncSource(req.sourceId);
        return {};
      },
    });
    service(PairingService, {
      async getPairing() {
        await wait();
        return { pairing: tv.pairing };
      },
      async startPairing() {
        await wait();
        return { pairing: tv.startPairing() };
      },
      async cancelPairing() {
        await wait();
        return { pairing: tv.cancelPairing() };
      },
    });
    service(ConfigService, {
      async getConfig() {
        await wait();
        return { config: tv.config };
      },
      async updateConfig(req) {
        await wait();
        return { config: await tv.updateConfig(req.config) };
      },
    });
    service(HomeService, {
      async getProvider() {
        await wait();
        return { provider: home.provider() };
      },
      async listAccessories() {
        await wait();
        return { accessories: home.listAccessories() };
      },
      async setAccessoryEnabled(req) {
        await wait();
        return { accessory: home.setEnabled(req.id, req.enabled) };
      },
      async listPairingGroups() {
        await wait();
        return { groups: home.listGroups() };
      },
      async resetPairing(req) {
        await wait();
        return { group: home.reset(req.groupId) };
      },
    });
  }, { transport: { interceptors: [record, sessionInterceptor] } });

  // Source artworks get art seeded by their own id, TV-only ones by content id, so the
  // Home preview of a TV content id matches its gallery tile.
  const seed = (a: LibraryArtwork) => (a.sources.length ? a.id : a.tvContentId);
  const aspect = (a: LibraryArtwork) => (a.width > 0 && a.height > 0 ? a.width / a.height : 16 / 9);
  const byId = (id: string) => library.artworks.find((a) => a.id === id);

  const art: ArtApi = {
    tvThumbnailUrl(contentId) {
      const a = library.artworks.find((x) => x.tvContentId === contentId);
      if (!a) return generatedArtUrl(contentId);
      return uploaded.get(a.id)?.thumbnail ?? generatedArtUrl(seed(a), aspect(a));
    },
    thumbnailUrl(id) {
      const a = byId(id);
      return uploaded.get(id)?.thumbnail ?? (a ? generatedArtUrl(seed(a), aspect(a)) : generatedArtUrl(id));
    },
    imageUrl(id) {
      const a = byId(id);
      return uploaded.get(id)?.image ?? (a ? generatedArtUrl(seed(a), aspect(a)) : generatedArtUrl(id));
    },
    async upload(sourceId, file, { path, onProgress, signal } = {}) {
      if (!auth.session) {
        notifySessionLost();
        throw new ConnectError("sign in first", Code.Unauthenticated);
      }
      await wait();
      const steps = 10;
      for (let i = 1; i <= steps; i++) {
        if (signal?.aborted) throw signal.reason;
        await sleep(latency > 0 ? 80 : 0);
        onProgress?.(i / steps);
      }
      await wait();
      const d = await decode(file);
      const created = library.addFile(sourceId, file, path ?? "", d.width, d.height);
      if (d.thumbnail) uploaded.set(created.id, { thumbnail: d.thumbnail, image: URL.createObjectURL(file) });
      return created;
    },
  };

  const mock: MockControl = {
    get scenario() {
      return tv.scenario;
    },
    setScenario: (s: Scenario) => tv.applyScenario(s),
    get homeMode() {
      return home.mode;
    },
    setHomeMode: (m: HomeMode) => {
      home.mode = m;
    },
    dropStreams: () => tv.dropStreams(),
    devPassword: DEV_PASSWORD,
    expireSession: () => {
      auth.session = false;
      tv.dropStreams();
    },
    setRateLimited: (on: boolean) => {
      auth.rateLimited = on;
    },
    get sentKeys() {
      return tv.sentKeys;
    },
  };

  return { transport, art, mock, tv, home, library, auth, calls };
}
