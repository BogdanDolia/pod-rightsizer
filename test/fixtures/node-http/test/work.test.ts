import assert from "node:assert/strict";
import { test } from "node:test";
import { MAX_CPU_ITERATIONS } from "../src/limits.js";
import { BoundedRetainer } from "../src/retention.js";
import { InFlightGate } from "../src/capacity.js";
import { allocateHeapObjects, allocateUsedBuffer, runCpuWork } from "../src/work.js";

test("cpu work is deterministic and capped", () => {
  const once = runCpuWork(1000);
  const twice = runCpuWork(1000);
  assert.deepEqual(once, twice);
  assert.equal(once.iterations, 1000);
  assert.notEqual(once.digest, runCpuWork(1001).digest);
  assert.throws(() => runCpuWork(MAX_CPU_ITERATIONS + 1), /per-request cap/);
});

test("heap objects are real and their payload is read", () => {
  const work = allocateHeapObjects(3);
  assert.equal(work.objects.length, 3);
  assert.equal(work.objects[2]?.payload[0], (2 + 0) & 0xff);
  assert.equal(work.checksum, (0 + 0 + 1 + 1 + 2 + 2) >>> 0);
});

test("buffer bytes are written and read back", () => {
  const work = allocateUsedBuffer(8192);
  assert.equal(work.buffer.length, 8192);
  assert.equal(work.buffer[0], 0);
  assert.equal(work.buffer[4096], 4096 & 0xff);
  assert.equal(work.buffer[8191], 0x5a);
  assert.ok(work.checksum > 0);
});

test("retention evicts oldest blocks and rejects one oversized block", () => {
  const retainer = new BoundedRetainer<string>(10);
  const first = retainer.retain("aaaa", 6);
  const second = retainer.retain("bbbb", 6);
  assert.equal(first.retained, true);
  assert.equal(first.evicted, 0);
  assert.equal(second.retained, true);
  assert.equal(second.evicted, 1);
  assert.deepEqual(retainer.held(), ["bbbb"]);
  assert.equal(retainer.stats().accountedBytes, 6);
  const rejected = retainer.retain("too-big", 11);
  assert.equal(rejected.retained, false);
  assert.equal(rejected.reason, "single_block_exceeds_cap");
  assert.equal(retainer.stats().rejected, 1);
  assert.equal(retainer.stats().accountedBytes, 6);
});

test("the in-flight gate does not queue", () => {
  const gate = new InFlightGate(1);
  assert.equal(gate.tryEnter(), true);
  assert.equal(gate.tryEnter(), false);
  assert.equal(gate.inFlight, 1);
  gate.leave();
  assert.equal(gate.tryEnter(), true);
});
