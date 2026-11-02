import { describe, expect, it } from "vitest";

import { folderPath } from "@/features/gallery/folder-path";

describe("folderPath", () => {
  it("normalizes relative folders", () => {
    expect(folderPath("")).toEqual({ value: "" });
    expect(folderPath("  2024/iceland ")).toEqual({ value: "2024/iceland" });
    expect(folderPath("/2024//iceland/")).toEqual({ value: "2024/iceland" });
  });

  it.each(["../etc", "a/../b", "a/./b", ".hidden", "a/.git", "a\\b", "a\u0000b", "tab\there"])("rejects %j", (p) => {
    expect(folderPath(p).error).toBeTruthy();
  });
});
