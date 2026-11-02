import { fromJson } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { LibraryArtworkSchema, type LibraryArtwork } from "@/gen/vigo/core/v1/library_pb";

export type UploadOptions = {
  /** Directory relative to the source root; empty means the root. */
  path?: string;
  onProgress?: (fraction: number) => void;
  signal?: AbortSignal;
};

/** vigo-core's plain-HTTP image endpoints, kept apart from the RPC clients. */
export interface ArtApi {
  /** Thumbnail of a TV content id, e.g. the TV's current selection. */
  tvThumbnailUrl(contentId: string): string;
  /** Thumbnail of a library artwork. */
  thumbnailUrl(artworkId: string): string;
  /** Upright preview of a library artwork for the crop editor (long side at most 2048). */
  imageUrl(artworkId: string): string;
  /** Writes a file into a source; vigo-core then uploads it to the TV. */
  upload(sourceId: string, file: File, options?: UploadOptions): Promise<LibraryArtwork>;
}

const codeByName = new Map<string, Code>(
  Object.entries(Code)
    .filter(([, v]) => typeof v === "number")
    .map(([k, v]) => [k.replace(/[A-Z]/g, (c, i) => (i ? "_" : "") + c.toLowerCase()), v as Code]),
);

function errorFromBody(status: number, text: string): ConnectError {
  try {
    const body = JSON.parse(text) as { code?: string; message?: string };
    const code = body.code ? codeByName.get(body.code) : undefined;
    if (code !== undefined) return new ConnectError(body.message ?? "", code);
  } catch {
    // Not a JSON error body; fall through to the HTTP status.
  }
  const byStatus: Record<number, Code> = {
    400: Code.InvalidArgument,
    404: Code.NotFound,
    409: Code.AlreadyExists,
    413: Code.ResourceExhausted,
    429: Code.ResourceExhausted,
  };
  if (byStatus[status] !== undefined) return new ConnectError(text || `HTTP ${status}`, byStatus[status]);
  if (status >= 500) return new ConnectError(text || `HTTP ${status}`, Code.Unavailable);
  return new ConnectError(text || `HTTP ${status}`, Code.Unknown);
}

const enc = encodeURIComponent;

export function createHttpArtApi(baseUrl = ""): ArtApi {
  return {
    tvThumbnailUrl: (id) => `${baseUrl}/art/${enc(id)}/thumbnail`,
    thumbnailUrl: (id) => `${baseUrl}/library/${enc(id)}/thumbnail`,
    imageUrl: (id) => `${baseUrl}/library/${enc(id)}/image`,
    upload(sourceId, file, { path, onProgress, signal } = {}) {
      return new Promise<LibraryArtwork>((resolve, reject) => {
        const form = new FormData();
        form.append("file", file);
        if (path) form.append("path", path);

        // XHR rather than fetch: fetch still has no upload progress events in Safari.
        const xhr = new XMLHttpRequest();
        xhr.open("POST", `${baseUrl}/sources/${enc(sourceId)}/files`);
        xhr.responseType = "text";
        xhr.upload.onprogress = (e) => {
          if (e.lengthComputable) onProgress?.(e.loaded / e.total);
        };
        xhr.onload = () => {
          if (xhr.status === 201 || xhr.status === 200) {
            try {
              resolve(fromJson(LibraryArtworkSchema, JSON.parse(xhr.responseText), { ignoreUnknownFields: true }));
            } catch (err) {
              reject(ConnectError.from(err, Code.Internal));
            }
          } else {
            reject(errorFromBody(xhr.status, xhr.responseText));
          }
        };
        xhr.onerror = () => reject(new ConnectError("Upload failed: vigo could not be reached.", Code.Unavailable));
        xhr.onabort = () => reject(new ConnectError("Upload cancelled.", Code.Canceled));
        signal?.addEventListener("abort", () => xhr.abort(), { once: true });
        xhr.send(form);
      });
    },
  };
}
