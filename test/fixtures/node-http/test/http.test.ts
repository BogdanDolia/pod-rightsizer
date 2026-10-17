import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { loadBuildManifest } from "../src/build-manifest.js";
import { parseConfig } from "../src/config.js";
import { CPU_ITERATIONS_PER_INTENSITY, DELAY_MS_PER_INTENSITY, HEAP_OBJECTS_PER_INTENSITY } from "../src/limits.js";
import { runCpuWork } from "../src/work.js";
import { startServer } from "../src/server.js";
import { fixtureRoot } from "./paths.js";

const input = JSON.parse(readFileSync(join(fixtureRoot, "scenario/quote-input.json"), "utf8")) as unknown;
const manifest = loadBuildManifest(readFileSync(join(fixtureRoot, "dist/build-manifest.json")));

async function postQuote(baseUrl: string, body: unknown = input): Promise<{ status: number; json: Record<string, unknown> }> {
  const response = await fetch(`${baseUrl}/v1/quote`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
  return { status: response.status, json: await response.json() as Record<string, unknown> };
}

test("baseline and every regression mode keep the cart total", async () => {
  const cases = [
    { mode: "baseline", intensity: "0" },
    { mode: "cpu", intensity: "1" },
    { mode: "heap", intensity: "1" },
    { mode: "buffer", intensity: "1" },
    { mode: "latency", intensity: "1" },
  ] as const;
  for (const item of cases) {
    const config = parseConfig({ NODE_HTTP_MODE: item.mode, NODE_HTTP_INTENSITY: item.intensity, PORT: "0" });
    const server = await startServer({ config, manifest, host: "127.0.0.1" });
    try {
      const response = await postQuote(server.baseUrl);
      assert.equal(response.status, 200, item.mode);
      assert.equal(response.json.totalCents, 1142, item.mode);
      assert.equal(response.json.subtotalCents, 1057, item.mode);
      assert.equal(response.json.taxCents, 85, item.mode);
      const regression = response.json.regression as { mode: string; intensity: number; cpuDigest: number | null; heapObjects: number; bufferBytes: number; delayMs: number };
      assert.equal(regression.mode, item.mode);
      assert.equal(regression.intensity, Number(item.intensity));
      if (item.mode === "cpu") {
        const expected = runCpuWork(CPU_ITERATIONS_PER_INTENSITY);
        assert.equal(regression.cpuDigest, expected.digest);
      }
      if (item.mode === "heap") {
        assert.equal(regression.heapObjects, HEAP_OBJECTS_PER_INTENSITY);
      }
      if (item.mode === "buffer") {
        assert.equal(regression.bufferBytes, 128 * 1024);
      }
      if (item.mode === "latency") {
        assert.equal(regression.delayMs, DELAY_MS_PER_INTENSITY);
      }
    } finally {
      await server.close();
    }
  }
});

test("invalid quotes and invalid JSON are rejected", async () => {
  const server = await startServer({
    config: parseConfig({ PORT: "0" }),
    manifest,
    host: "127.0.0.1",
  });
  try {
    const unknown = await postQuote(server.baseUrl, { items: [{ sku: "nope", quantity: 1 }] });
    assert.equal(unknown.status, 400);
    assert.equal(unknown.json.error, "invalid_quote");
    const negative = await postQuote(server.baseUrl, { items: [{ sku: "apple", quantity: -1 }] });
    assert.equal(negative.status, 400);
    const raw = await fetch(`${server.baseUrl}/v1/quote`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: "{\"items\":",
    });
    assert.equal(raw.status, 400);
    const json = await raw.json() as { error: string };
    assert.equal(json.error, "invalid_json");
  } finally {
    await server.close();
  }
});

test("a full in-flight gate rejects immediately", async () => {
  const server = await startServer({
    config: parseConfig({ NODE_HTTP_MODE: "latency", NODE_HTTP_INTENSITY: "8", PORT: "0" }),
    manifest,
    host: "127.0.0.1",
    maxInFlight: 1,
  });
  try {
    const first = postQuote(server.baseUrl);
    await new Promise((resolve) => setTimeout(resolve, 30));
    const started = Date.now();
    const second = await postQuote(server.baseUrl);
    const elapsed = Date.now() - started;
    assert.equal(second.status, 429);
    assert.equal(second.json.error, "capacity_exceeded");
    assert.equal(second.json.behavior, "reject_immediately");
    assert.ok(elapsed < 100, `rejected request waited ${elapsed}ms`);
    const completed = await first;
    assert.equal(completed.status, 200);
    assert.equal(completed.json.totalCents, 1142);
  } finally {
    await server.close();
  }
});

test("heap and buffer retention stay within the cap", async () => {
  const server = await startServer({
    config: parseConfig({ NODE_HTTP_MODE: "heap", NODE_HTTP_INTENSITY: "1", PORT: "0" }),
    manifest,
    host: "127.0.0.1",
    heapRetentionMaxBytes: 256 * 1000,
  });
  try {
    const first = await postQuote(server.baseUrl);
    const second = await postQuote(server.baseUrl);
    assert.equal(first.status, 200);
    assert.equal(second.status, 200);
    assert.equal(second.json.totalCents, 1142);
    const regression = second.json.regression as { retained: boolean; evicted: number };
    assert.equal(regression.retained, true);
    assert.ok(regression.evicted >= 1);
  } finally {
    await server.close();
  }
});

test("one buffer block larger than the cap is not retained", async () => {
  const server = await startServer({
    config: parseConfig({ NODE_HTTP_MODE: "buffer", NODE_HTTP_INTENSITY: "1", PORT: "0" }),
    manifest,
    host: "127.0.0.1",
    bufferRetentionMaxBytes: 1024,
  });
  try {
    const response = await postQuote(server.baseUrl);
    assert.equal(response.status, 200);
    assert.equal(response.json.totalCents, 1142);
    const regression = response.json.regression as { retained: boolean; retentionReason: string; bufferBytes: number; bufferChecksum: number };
    assert.equal(regression.bufferBytes, 128 * 1024);
    assert.equal(regression.retained, false);
    assert.equal(regression.retentionReason, "single_block_exceeds_cap");
    assert.ok(regression.bufferChecksum > 0);
  } finally {
    await server.close();
  }
});

test("a retained buffer increases arrayBuffers and stays inside external", async () => {
  const server = await startServer({
    config: parseConfig({ NODE_HTTP_MODE: "buffer", NODE_HTTP_INTENSITY: "1", PORT: "0" }),
    manifest,
    host: "127.0.0.1",
  });
  try {
    const before = process.memoryUsage().arrayBuffers;
    const response = await postQuote(server.baseUrl);
    const after = process.memoryUsage().arrayBuffers;
    assert.equal(response.status, 200);
    const regression = response.json.regression as { retained: boolean; bufferChecksum: number };
    assert.equal(regression.retained, true);
    assert.ok(regression.bufferChecksum > 0);
    assert.ok(after >= before + 64 * 1024, `arrayBuffers ${before} -> ${after}`);
    const telemetry = await (await fetch(`${server.baseUrl}/telemetry`)).json() as {
      snapshot: {
        memory: { externalBytes: number; arrayBuffersBytes: number };
        retention: { buffer: { accountedBytes: number } };
      };
    };
    assert.ok(telemetry.snapshot.retention.buffer.accountedBytes >= 128 * 1024);
    assert.ok(telemetry.snapshot.memory.arrayBuffersBytes <= telemetry.snapshot.memory.externalBytes);
  } finally {
    await server.close();
  }
});
