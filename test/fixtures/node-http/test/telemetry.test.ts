import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { loadBuildManifest } from "../src/build-manifest.js";
import { parseConfig } from "../src/config.js";
import { formatEventLoopDelay, readOldSpace } from "../src/telemetry.js";
import { startServer } from "../src/server.js";
import { fixtureRoot } from "./paths.js";
import { startReference } from "./spawn.js";

const manifest = loadBuildManifest(readFileSync(join(fixtureRoot, "dist/build-manifest.json")));

test("an empty event-loop histogram is not a zero or NaN delay", () => {
  const formatted = formatEventLoopDelay({
    count: 0,
    min: Number.MAX_VALUE,
    max: 0,
    mean: Number.NaN,
    percentile: () => Number.NaN,
  }, { startedAt: "2026-10-17T00:00:00.000Z", observedAt: "2026-10-17T00:00:00.000Z" });
  assert.equal(formatted.state, "missing_signal");
  assert.equal(formatted.reason, "histogram_empty");
  assert.equal("minNs" in formatted, false);
  assert.equal("meanNs" in formatted, false);
  assert.equal(JSON.stringify(formatted).includes("NaN"), false);
});

test("old-space flags stay distinct from the V8 heap limit", () => {
  const unset = readOldSpace([]);
  const maxOldSpaceSize = unset.maxOldSpaceSize as { state: string; bytes?: number };
  assert.equal(maxOldSpaceSize.state, "unset");
  assert.equal(maxOldSpaceSize.bytes, undefined);
  const known = readOldSpace(["--max-old-space-size=128", "--max-old-space-size-percentage=40"]);
  const flag = known.maxOldSpaceSize as { unit: string; value: number; bytes: number; appliesTo: string };
  const percentage = known.maxOldSpaceSizePercentage as { unit: string; value: number; bytes?: number };
  assert.equal(flag.unit, "mebibytes");
  assert.equal(flag.value, 128);
  assert.equal(flag.bytes, 128 * 1048576);
  assert.equal(flag.appliesTo, "v8_old_space");
  assert.equal(percentage.unit, "percent");
  assert.equal(percentage.value, 40);
  assert.equal(percentage.bytes, undefined);
  assert.equal(known.distinctFrom, "v8HeapSizeLimit");
});

test("live telemetry uses real units and does not reset its window on scrape", async () => {
  const server = await startServer({
    config: parseConfig({ PORT: "0" }),
    manifest,
    host: "127.0.0.1",
  });
  try {
    await new Promise((resolve) => setTimeout(resolve, 40));
    const first = await (await fetch(`${server.baseUrl}/telemetry`)).json() as {
      formatVersion: string;
      disclaimer: string;
      identity: { processId: string; pid: number };
      build: { buildId: string };
      snapshot: { memory: { heapUsedBytes: number; externalBytes: number; arrayBuffersBytes: number; rssIsNotContainerMemory: boolean } };
      cumulative: { eventLoopDelay: { state: string; unit?: string; count?: number; minNs?: number; reason?: string } };
      interval: { eventLoopUtilization: { state: string; reason?: string; utilization?: number; startedAt?: string } };
      settings: { v8HeapSizeLimit: { unit: string; bytes: number; source: string }; oldSpace: { maxOldSpaceSize: { state: string } } };
      missingSignals: { gc: { state: string }; containerMemory: { state: string; rssIsNotASubstitute: boolean } };
    };
    assert.equal(first.formatVersion, "node-http-telemetry/v1");
    assert.match(first.disclaimer, /not a telemetry_validated/);
    assert.equal(first.build.buildId, manifest.buildId);
    assert.notEqual(first.identity.processId, String(first.identity.pid));
    assert.equal(first.snapshot.memory.arrayBuffersBytes <= first.snapshot.memory.externalBytes, true);
    assert.equal(first.snapshot.memory.rssIsNotContainerMemory, true);
    assert.ok(first.snapshot.memory.heapUsedBytes > 0);
    assert.equal(first.settings.v8HeapSizeLimit.unit, "bytes");
    assert.equal(first.settings.v8HeapSizeLimit.source, "v8.getHeapStatistics().heap_size_limit");
    assert.ok(first.settings.v8HeapSizeLimit.bytes > 0);
    assert.equal(first.missingSignals.gc.state, "missing_signal");
    assert.equal(first.missingSignals.containerMemory.state, "missing_signal");
    assert.equal(first.missingSignals.containerMemory.rssIsNotASubstitute, true);
    assert.equal(first.interval.eventLoopUtilization.state, "missing_signal");
    assert.equal(first.interval.eventLoopUtilization.reason, "interval_not_complete");
    assert.equal(first.interval.eventLoopUtilization.utilization, undefined);
    if (first.cumulative.eventLoopDelay.state === "known") {
      assert.equal(first.cumulative.eventLoopDelay.unit, "nanoseconds");
      assert.ok((first.cumulative.eventLoopDelay.count ?? 0) > 0);
      assert.equal(typeof first.cumulative.eventLoopDelay.minNs, "number");
    } else {
      assert.equal(first.cumulative.eventLoopDelay.reason, "histogram_empty");
      assert.equal(first.cumulative.eventLoopDelay.minNs, undefined);
    }
    const second = await (await fetch(`${server.baseUrl}/telemetry`)).json() as typeof first;
    const firstCount = first.cumulative.eventLoopDelay.count ?? 0;
    const secondCount = second.cumulative.eventLoopDelay.count ?? 0;
    assert.ok(secondCount >= firstCount);
    await new Promise((resolve) => setTimeout(resolve, 1100));
    const third = await (await fetch(`${server.baseUrl}/telemetry`)).json() as typeof first;
    const fourth = await (await fetch(`${server.baseUrl}/telemetry`)).json() as typeof first;
    assert.equal(third.interval.eventLoopUtilization.state, "known");
    const utilization = third.interval.eventLoopUtilization.utilization ?? -1;
    assert.ok(utilization >= 0 && utilization <= 1);
    assert.equal(third.interval.eventLoopUtilization.startedAt, fourth.interval.eventLoopUtilization.startedAt);
  } finally {
    await server.close();
  }
});

test("metadata comes from the running build and omits secrets", async () => {
  const server = await startReference({
    PORT: "0",
    NODE_HTTP_MODE: "baseline",
    NODE_HTTP_INTENSITY: "0",
    SECRET_TOKEN: "super-secret-value",
  });
  try {
    const response = await fetch(`${server.baseUrl}/metadata`);
    const body = await response.text();
    assert.equal(body.includes("super-secret-value"), false);
    assert.equal(body.includes("SECRET_TOKEN"), false);
    const metadata = JSON.parse(body) as {
      formatVersion: string;
      logicalService: string;
      appRoot: string;
      node: { version: string; v8: string; platform: string; arch: string };
      process: { id: string; pid: number; startedAt: string };
      regression: { mode: string; intensity: number };
      build: { buildId: string; sourceMethod: string; verifiedLinkage: boolean };
    };
    assert.equal(metadata.formatVersion, "node-http-metadata/v1");
    assert.equal(metadata.logicalService, "node-http-reference");
    assert.equal(metadata.appRoot, "test/fixtures/node-http");
    assert.equal(metadata.node.version, "v24.22.0");
    assert.equal(metadata.node.v8, process.versions.v8);
    assert.equal(metadata.node.platform, process.platform);
    assert.equal(metadata.node.arch, process.arch);
    assert.notEqual(metadata.process.id, String(metadata.process.pid));
    assert.match(metadata.process.startedAt, /^\d{4}-\d{2}-\d{2}T/);
    assert.equal(metadata.regression.mode, "baseline");
    assert.equal(metadata.regression.intensity, 0);
    assert.equal(metadata.build.buildId, manifest.buildId);
    assert.equal(metadata.build.sourceMethod, "explicit_build_input");
    assert.equal(metadata.build.verifiedLinkage, false);
  } finally {
    await server.stop();
  }
});

test("a configured old-space flag is reported separately from heap_size_limit", async () => {
  const server = await startReference({ PORT: "0" }, ["--max-old-space-size=128"]);
  try {
    const telemetry = await (await fetch(`${server.baseUrl}/telemetry`)).json() as {
      settings: {
        oldSpace: { maxOldSpaceSize: { state: string; unit: string; value: number; bytes: number } };
        v8HeapSizeLimit: { bytes: number; unit: string };
      };
    };
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.state, "known");
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.unit, "mebibytes");
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.value, 128);
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.bytes, 134217728);
    assert.equal(telemetry.settings.v8HeapSizeLimit.unit, "bytes");
    assert.notEqual(telemetry.settings.v8HeapSizeLimit.bytes, telemetry.settings.oldSpace.maxOldSpaceSize.value);
  } finally {
    await server.stop();
  }
});
