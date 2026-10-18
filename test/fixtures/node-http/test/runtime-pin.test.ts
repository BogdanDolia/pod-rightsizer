import assert from "node:assert/strict";
import { test } from "node:test";

test("the fixture test process is the pinned Node.js patch", () => {
  assert.equal(process.version, "v24.22.0");
  assert.equal(process.release.lts, "Krypton");
});
