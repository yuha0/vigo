import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import {
  ArtModeState,
  ArtworkSchema,
  ConnectionState,
  ContentType,
  DeviceSchema,
  PowerState,
  SettingSchema,
  type Artwork,
  type Setting,
} from "@/gen/vigo/core/v1/core_pb";
import type { Scenario } from "@/lib/mock/control";

export const device = () =>
  create(DeviceSchema, {
    model: "24_PTM_FTV_T09",
    modelName: "QN75LS03DAFXZA",
    name: "Living Room Frame",
    networkType: "wired",
    flashSizeGb: 16,
    supportsMotionSensor: true,
    supportsBrightnessSensor: true,
  });

/** Mirrors the inventory in the research notes: 1 preinstall and 134 Art Store items, plus (optionally) one upload made outside vigo. */
export function artworks(uploads = true): Artwork[] {
  const out: Artwork[] = [
    create(ArtworkSchema, {
      contentId: "SAM-S00001",
      contentType: ContentType.PREINSTALL,
      categoryIds: ["MY-C0004"],
      width: 3840,
      height: 2160,
    }),
  ];
  for (let i = 0; i < 134; i++) {
    out.push(
      create(ArtworkSchema, {
        contentId: `SAM-F${String(101 + i).padStart(4, "0")}`,
        contentType: ContentType.SERVER,
        categoryIds: [i < 106 ? "MY-C0008" : "MY-C0004"],
        width: 3840,
        height: 2160,
        slideshow: i % 45 === 7,
      }),
    );
  }
  if (uploads) {
    out.push(
      create(ArtworkSchema, {
        contentId: "MY_F0001",
        contentType: ContentType.MOBILE,
        categoryIds: ["MY-C0002", "MY-C0008"],
        matteId: "none",
        // The TV reports uploads with width and height swapped.
        width: 2160,
        height: 3840,
        imageDate: timestampFromMs(Date.now() - 8 * 86_400_000),
        uploadedBytes: 1_613_337n,
      }),
    );
  }
  return out;
}

export function settings(): Setting[] {
  return [
    create(SettingSchema, { key: "brightness", kind: { case: "range", value: { value: 4, min: 0, max: 10 } } }),
    create(SettingSchema, { key: "color_temperature", kind: { case: "range", value: { value: 0, min: -5, max: 5 } } }),
    create(SettingSchema, { key: "motion_sensitivity", kind: { case: "range", value: { value: 2, min: 1, max: 3 } } }),
    create(SettingSchema, {
      key: "motion_timer",
      kind: { case: "enum", value: { value: "5", options: ["off", "5", "15", "30", "60", "120", "240"] } },
    }),
    create(SettingSchema, { key: "brightness_sensor_setting", kind: { case: "toggle", value: { value: true } } }),
  ];
}

export const matteTypes = ["shadowbox", "modern", "modernthin", "modernwide", "flexible", "panoramic", "triptych", "mix", "squares"];

export const matteColors = [
  { name: "polar", hex: "#f4f4f0" },
  { name: "antique", hex: "#e9e1d1" },
  { name: "warm", hex: "#dccdb5" },
  { name: "sand", hex: "#c8b79a" },
  { name: "sage", hex: "#9aa58b" },
  { name: "seafoam", hex: "#a9c2b6" },
  { name: "skyblue", hex: "#9cc0db" },
  { name: "lavender", hex: "#b7aecf" },
  { name: "apricot", hex: "#e9b38f" },
  { name: "redorange", hex: "#c6533a" },
  { name: "burgandy", hex: "#6e2b33" },
  { name: "navy", hex: "#263247" },
  { name: "black", hex: "#1b1b1b" },
];

export type StatusShape = {
  power: PowerState;
  artMode: ArtModeState;
  connection: ConnectionState;
  leader: boolean;
  warnings?: { code: string; message: string }[];
};

export function scenarioStatus(s: Scenario): StatusShape {
  const base = { leader: true };
  switch (s) {
    case "art":
      return { ...base, power: PowerState.ON, artMode: ArtModeState.ON, connection: ConnectionState.CONNECTED };
    case "tv-mode":
      return { ...base, power: PowerState.ON, artMode: ArtModeState.OFF, connection: ConnectionState.CONNECTED };
    case "motion-sleep":
      return { ...base, power: PowerState.STANDBY, artMode: ArtModeState.ON, connection: ConnectionState.CONNECTED };
    case "powered-off":
      return { ...base, power: PowerState.STANDBY, artMode: ArtModeState.OFF, connection: ConnectionState.CONNECTED };
    case "connecting":
      return { ...base, power: PowerState.ON, artMode: ArtModeState.UNSPECIFIED, connection: ConnectionState.CONNECTING };
    case "unreachable":
      return { ...base, power: PowerState.UNREACHABLE, artMode: ArtModeState.UNSPECIFIED, connection: ConnectionState.DISCONNECTED };
    case "not-paired":
      return { ...base, power: PowerState.ON, artMode: ArtModeState.UNSPECIFIED, connection: ConnectionState.NOT_PAIRED };
    case "no-art-host":
      return { ...base, power: PowerState.ON, artMode: ArtModeState.OFF, connection: ConnectionState.NO_ART_HOST };
    case "standby-replica":
      return { leader: false, power: PowerState.UNSPECIFIED, artMode: ArtModeState.UNSPECIFIED, connection: ConnectionState.STANDBY_REPLICA };
    case "unconfirmed":
      return { ...base, power: PowerState.ON, artMode: ArtModeState.ON, connection: ConnectionState.CONNECTED };
    case "model-changed":
      return {
        ...base,
        power: PowerState.ON,
        artMode: ArtModeState.ON,
        connection: ConnectionState.CONNECTED,
        warnings: [
          {
            code: "model_changed",
            message:
              "The TV's model string changed from 24_PONTUSM_FTV to 24_PTM_FTV_T09, which usually means a firmware update. Check that art and settings still work.",
          },
        ],
      };
  }
}
