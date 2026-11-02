export type FolderPath = { value: string; error?: string };

/**
 * Normalizes the upload folder the way vigo-core expects it: a directory
 * relative to the source root, "" for the root. Rejects what the server
 * rejects so the user hears about it before uploading.
 */
export function folderPath(input: string): FolderPath {
  const trimmed = input.trim();
  if (!trimmed) return { value: "" };
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(trimmed)) return { value: "", error: "The folder name has invisible control characters." };
  if (trimmed.includes("\\")) return { value: "", error: "Use “/” to separate folders, not “\\”." };
  const parts = trimmed.split("/").filter((p) => p !== "");
  if (parts.some((p) => p === ".." || p === ".")) return { value: "", error: "The folder must stay inside the source; “..” and “.” aren't allowed." };
  if (parts.some((p) => p.startsWith("."))) return { value: "", error: "Folders starting with “.” are hidden and aren't allowed." };
  return { value: parts.join("/") };
}
