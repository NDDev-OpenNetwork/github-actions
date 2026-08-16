import { describe, expect, test } from "bun:test";

import { summarize } from "./summarize";

describe("summarize", () => {
  test("is deterministic", () => {
    expect(summarize(50_000)).toEqual(summarize(50_000));
    expect(summarize(50_000).checksum).toHaveLength(8);
  });
});
