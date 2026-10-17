import assert from "node:assert/strict";
import { test } from "node:test";
import { LoadPhaseLedger } from "../src/load-phases.js";

test("measured stays closed while warmup work is still running", () => {
  const ledger = new LoadPhaseLedger();
  const warmup = ledger.begin("warmup");
  assert.equal(ledger.tryOpenMeasured(), false);
  const closed = ledger.snapshot();
  assert.equal(closed.measuredOpen, false);
  assert.equal(closed.warmupInFlight, 1);
  ledger.finish(warmup);
  assert.equal(ledger.tryOpenMeasured(), true);
  const measured = ledger.begin("measured");
  const open = ledger.snapshot() as {
    warmupInFlightAtFirstMeasuredStart: number;
    warmup: { lastWorkFinishedOffsetMs: number };
    measured: { firstStartedOffsetMs: number };
  };
  assert.equal(open.warmupInFlightAtFirstMeasuredStart, 0);
  assert.ok(open.warmup.lastWorkFinishedOffsetMs <= open.measured.firstStartedOffsetMs);
  ledger.finish(measured);
});

test("a phase ledger does not keep request bodies", () => {
  const ledger = new LoadPhaseLedger();
  const id = ledger.begin("warmup");
  ledger.finish(id);
  const encoded = JSON.stringify(ledger.snapshot());
  assert.equal(encoded.includes("SECRET_SHOULD_NOT_LEAK"), false);
  assert.match(encoded, /node-http-load-phases\/v1/);
});
