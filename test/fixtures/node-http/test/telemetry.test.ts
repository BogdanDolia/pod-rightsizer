import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, writeFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { promisify } from "node:util";
import { loadBuildManifest } from "../src/build-manifest.js";
import { parseConfig } from "../src/config.js";
import { formatEventLoopDelay, parseNodeOptions, readOldSpace } from "../src/telemetry.js";
import { startServer } from "../src/server.js";
import { fixtureRoot } from "./paths.js";
import { startReference } from "./spawn.js";

const execute = promisify(execFile);

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
  const unset = readOldSpace({ execArgv: [] });
  const maxOldSpaceSize = unset.maxOldSpaceSize as { state: string; source: string; bytes?: number };
  assert.equal(maxOldSpaceSize.state, "unset");
  assert.equal(maxOldSpaceSize.source, "absent");
  assert.equal(maxOldSpaceSize.bytes, undefined);
  const known = readOldSpace({
    execArgv: ["--max-old-space-size=128", "--max_old_space_size=256", "--max-old-space-size-percentage=40"],
  });
  const flag = known.maxOldSpaceSize as { unit: string; value: number; bytes: number; appliesTo: string; source: string };
  const percentage = known.maxOldSpaceSizePercentage as { unit: string; value: number; bytes?: number; source: string };
  const effective = known.effective as { state: string; setting: string; source: string };
  assert.equal(flag.unit, "mebibytes");
  assert.equal(flag.value, 256);
  assert.equal(flag.bytes, 256 * 1048576);
  assert.equal(flag.appliesTo, "v8_old_space");
  assert.equal(flag.source, "process.execArgv");
  assert.equal(percentage.unit, "percent");
  assert.equal(percentage.value, 40);
  assert.equal(percentage.bytes, undefined);
  assert.equal(effective.state, "known");
  assert.equal(effective.setting, "maxOldSpaceSizePercentage");
  assert.equal(known.distinctFrom, "v8HeapSizeLimit");
  assert.equal(JSON.stringify(known).includes("heap_size_limit"), false);
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
        oldSpace: {
          maxOldSpaceSize: { state: string; unit: string; value: number; bytes: number; source: string };
          effective: { setting: string; source: string };
        };
        v8HeapSizeLimit: { bytes: number; unit: string };
      };
    };
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.state, "known");
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.unit, "mebibytes");
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.value, 128);
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.bytes, 134217728);
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.source, "process.execArgv");
    assert.equal(telemetry.settings.oldSpace.effective.setting, "maxOldSpaceSize");
    assert.equal(telemetry.settings.oldSpace.effective.source, "process.execArgv");
    assert.equal(telemetry.settings.v8HeapSizeLimit.unit, "bytes");
    assert.notEqual(telemetry.settings.v8HeapSizeLimit.bytes, telemetry.settings.oldSpace.maxOldSpaceSize.value);
    assert.notEqual(telemetry.settings.v8HeapSizeLimit.bytes, telemetry.settings.oldSpace.maxOldSpaceSize.bytes);
    const control = await controlHeapLimit(["--max-old-space-size=128"]);
    assert.equal(telemetry.settings.v8HeapSizeLimit.bytes, control);
    console.log(`v8_control single_cli value=128 source=process.execArgv heap=${telemetry.settings.v8HeapSizeLimit.bytes} old_space_bytes=${telemetry.settings.oldSpace.maxOldSpaceSize.bytes}`);
  } finally {
    await server.stop();
  }
});

interface OldSpaceSetting {
  state: string;
  source: string;
  unit?: string;
  value?: number;
  bytes?: number;
  reason?: string;
  v8ZeroMeansDefault?: boolean;
}

interface OldSpaceReport {
  maxOldSpaceSize: OldSpaceSetting;
  maxOldSpaceSizePercentage: OldSpaceSetting;
  effective: { state: string; setting: string | null; source: string; reason?: string };
  distinctFrom: string;
}

interface TelemetrySettings {
  settings: {
    oldSpace: OldSpaceReport;
    v8HeapSizeLimit: { bytes: number; unit: string; source: string };
  };
}

function childEnv(env: Record<string, string>): NodeJS.ProcessEnv {
  const copy: NodeJS.ProcessEnv = { ...process.env, ...env };
  if (!Object.prototype.hasOwnProperty.call(env, "NODE_OPTIONS")) {
    delete copy.NODE_OPTIONS;
  }
  return copy;
}

async function controlHeapLimit(args: string[], env: Record<string, string> = {}): Promise<number> {
  const result = await execute(process.execPath, [
    ...args,
    "-p",
    "require('node:v8').getHeapStatistics().heap_size_limit",
  ], { env: childEnv(env) });
  return Number(result.stdout.trim());
}

async function telemetryFor(args: string[], env: Record<string, string> = {}): Promise<TelemetrySettings> {
  const server = await startReference({ PORT: "0", ...env }, args);
  try {
    return await (await fetch(`${server.baseUrl}/telemetry`)).json() as TelemetrySettings;
  } finally {
    await server.stop();
  }
}

test("NODE_OPTIONS quoting that Node cannot parse is not reported as unset", () => {
  const secret = "SECRET_SHOULD_NOT_LEAK";
  const parsed = parseNodeOptions(`--max-old-space-size="${secret}`);
  assert.equal(parsed.ok, false);
  const report = readOldSpace({ execArgv: ["--max-old-space-size=256"], nodeOptions: `--max-old-space-size="${secret}` });
  const encoded = JSON.stringify(report);
  assert.equal(encoded.includes(secret), false);
  assert.equal(encoded.includes("NODE_OPTIONS="), false);
  const size = report.maxOldSpaceSize as OldSpaceSetting;
  assert.equal(size.state, "missing_signal");
  assert.notEqual(size.state, "unset");
  assert.equal(size.value, undefined);
  assert.equal((report.effective as { state: string }).state, "missing_signal");
});

test("a bare flag does not consume the following argv element", () => {
  const report = readOldSpace({ execArgv: ["--max-old-space-size", "256", "--enable-source-maps"] }) as unknown as OldSpaceReport;
  assert.equal(report.maxOldSpaceSize.state, "invalid");
  assert.equal(report.maxOldSpaceSize.reason, "node_24_requires_equals");
  assert.equal(report.maxOldSpaceSize.value, undefined);
  const fromOptions = readOldSpace({ execArgv: [], nodeOptions: "--max-old-space-size 256" }) as unknown as OldSpaceReport;
  assert.equal(fromOptions.maxOldSpaceSize.value, undefined);
  assert.notEqual(fromOptions.maxOldSpaceSize.state, "known");
});

test("Node 24.22.0 rejects a separated old-space argument", async () => {
  const separated = await execute(process.execPath, ["--max-old-space-size", "128", "-e", "process.exit(0)"]).then(() => 0, (error: { code?: number }) => error.code);
  assert.notEqual(separated, 0);
  const named = await controlHeapLimit(["--max-old-space-size=128"]);
  assert.equal(Number.isSafeInteger(named), true);
});

test("the last old-space flag wins across aliases and overrides NODE_OPTIONS", async () => {
  const underscored = await telemetryFor(["--max_old_space_size=128"]);
  const only256 = await controlHeapLimit(["--max-old-space-size=256"]);
  const only128 = await controlHeapLimit(["--max-old-space-size=128"]);
  assert.equal(underscored.settings.oldSpace.maxOldSpaceSize.state, "known");
  assert.equal(underscored.settings.oldSpace.maxOldSpaceSize.value, 128);
  assert.equal(underscored.settings.oldSpace.maxOldSpaceSize.source, "process.execArgv");
  assert.equal(underscored.settings.v8HeapSizeLimit.bytes, only128);
  console.log(`v8_control underscore value=128 source=process.execArgv heap=${underscored.settings.v8HeapSizeLimit.bytes}`);

  const repeated = await telemetryFor(["--max-old-space-size=128", "--max_old_space_size=256"]);
  assert.equal(repeated.settings.oldSpace.maxOldSpaceSize.state, "known");
  assert.equal(repeated.settings.oldSpace.maxOldSpaceSize.value, 256);
  assert.equal(repeated.settings.oldSpace.maxOldSpaceSize.bytes, 256 * 1048576);
  assert.equal(repeated.settings.oldSpace.maxOldSpaceSize.source, "process.execArgv");
  assert.equal(repeated.settings.oldSpace.effective.setting, "maxOldSpaceSize");
  assert.equal(repeated.settings.v8HeapSizeLimit.bytes, only256);
  assert.notEqual(repeated.settings.v8HeapSizeLimit.bytes, only128);
  assert.notEqual(repeated.settings.v8HeapSizeLimit.bytes, repeated.settings.oldSpace.maxOldSpaceSize.bytes);

  const fromEnvironment = await telemetryFor([], { NODE_OPTIONS: "--max-old-space-size=128" });
  assert.equal(fromEnvironment.settings.oldSpace.maxOldSpaceSize.state, "known");
  assert.equal(fromEnvironment.settings.oldSpace.maxOldSpaceSize.source, "NODE_OPTIONS");
  assert.equal(fromEnvironment.settings.oldSpace.maxOldSpaceSize.value, 128);
  assert.equal(fromEnvironment.settings.v8HeapSizeLimit.bytes, only128);

  const quoted = await telemetryFor([], { NODE_OPTIONS: '--max-old-space-size="256"' });
  assert.equal(quoted.settings.oldSpace.maxOldSpaceSize.value, 256);
  assert.equal(quoted.settings.oldSpace.maxOldSpaceSize.source, "NODE_OPTIONS");
  assert.equal(quoted.settings.v8HeapSizeLimit.bytes, only256);

  const overridden = await telemetryFor(["--max-old-space-size=256"], { NODE_OPTIONS: "--max-old-space-size=128" });
  assert.equal(overridden.settings.oldSpace.maxOldSpaceSize.value, 256);
  assert.equal(overridden.settings.oldSpace.maxOldSpaceSize.source, "process.execArgv");
  assert.equal(overridden.settings.v8HeapSizeLimit.bytes, only256);
  console.log(`v8_control cli_last value=256 source=process.execArgv heap=${overridden.settings.v8HeapSizeLimit.bytes} control256=${only256} control128=${only128}`);
  console.log(`v8_control node_options value=128 source=NODE_OPTIONS heap=${fromEnvironment.settings.v8HeapSizeLimit.bytes}`);
});

test("an absent old-space setting stays unset and is not the live heap limit", async () => {
  const server = await startReference({ PORT: "0", NODE_OPTIONS: "" });
  try {
    const telemetry = await (await fetch(`${server.baseUrl}/telemetry`)).json() as TelemetrySettings;
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.state, "unset");
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.source, "absent");
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.bytes, undefined);
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSizePercentage.state, "unset");
    assert.equal(telemetry.settings.oldSpace.effective.state, "unset");
    const control = await controlHeapLimit([]);
    assert.equal(telemetry.settings.v8HeapSizeLimit.bytes, control);
    assert.equal(telemetry.settings.v8HeapSizeLimit.source, "v8.getHeapStatistics().heap_size_limit");
  } finally {
    await server.stop();
  }
});

test("percentage keeps its own unit and overrides the mebibyte setting", async () => {
  const percentageOnly = await telemetryFor(["--max-old-space-size-percentage=5"]);
  const percentageControl = await controlHeapLimit(["--max-old-space-size-percentage=5"]);
  const sizeControl = await controlHeapLimit(["--max-old-space-size=128"]);
  const percentage = percentageOnly.settings.oldSpace.maxOldSpaceSizePercentage;
  assert.equal(percentage.state, "known");
  assert.equal(percentage.unit, "percent");
  assert.equal(percentage.value, 5);
  assert.equal(percentage.bytes, undefined);
  assert.equal(percentageOnly.settings.oldSpace.effective.setting, "maxOldSpaceSizePercentage");
  assert.equal(percentageOnly.settings.v8HeapSizeLimit.bytes, percentageControl);
  assert.notEqual(percentageOnly.settings.v8HeapSizeLimit.bytes, sizeControl);

  const both = await telemetryFor(
    ["--max-old-space-size=128"],
    { NODE_OPTIONS: "--max-old-space-size-percentage=5" },
  );
  assert.equal(both.settings.oldSpace.maxOldSpaceSize.value, 128);
  assert.equal(both.settings.oldSpace.maxOldSpaceSize.bytes, 128 * 1048576);
  assert.equal(both.settings.oldSpace.maxOldSpaceSize.source, "process.execArgv");
  assert.equal(both.settings.oldSpace.maxOldSpaceSizePercentage.value, 5);
  assert.equal(both.settings.oldSpace.maxOldSpaceSizePercentage.unit, "percent");
  assert.equal(both.settings.oldSpace.maxOldSpaceSizePercentage.bytes, undefined);
  assert.equal(both.settings.oldSpace.maxOldSpaceSizePercentage.source, "NODE_OPTIONS");
  assert.equal(both.settings.oldSpace.effective.setting, "maxOldSpaceSizePercentage");
  assert.equal(both.settings.oldSpace.effective.source, "NODE_OPTIONS");
  assert.equal(both.settings.v8HeapSizeLimit.bytes, percentageControl);
  assert.notEqual(both.settings.oldSpace.maxOldSpaceSize.bytes, both.settings.v8HeapSizeLimit.bytes);

  const reversed = await telemetryFor(["--max-old-space-size-percentage=5", "--max-old-space-size=128"]);
  assert.equal(reversed.settings.oldSpace.effective.setting, "maxOldSpaceSizePercentage");
  assert.equal(reversed.settings.v8HeapSizeLimit.bytes, percentageControl);

  const fractional = await telemetryFor(["--max-old-space-size-percentage=5.5"]);
  const fractionalControl = await controlHeapLimit(["--max-old-space-size-percentage=5.5"]);
  assert.equal(fractional.settings.oldSpace.maxOldSpaceSizePercentage.value, 5.5);
  assert.equal(fractional.settings.oldSpace.maxOldSpaceSizePercentage.bytes, undefined);
  assert.equal(fractional.settings.v8HeapSizeLimit.bytes, fractionalControl);
  const hexadecimal = await telemetryFor(["--max_old_space_size_percentage=0x10"]);
  const hexadecimalControl = await controlHeapLimit(["--max-old-space-size-percentage=16"]);
  assert.equal(hexadecimal.settings.oldSpace.maxOldSpaceSizePercentage.value, 16);
  assert.equal(hexadecimal.settings.oldSpace.maxOldSpaceSizePercentage.unit, "percent");
  assert.equal(hexadecimal.settings.oldSpace.maxOldSpaceSizePercentage.bytes, undefined);
  assert.equal(hexadecimal.settings.v8HeapSizeLimit.bytes, hexadecimalControl);
  console.log(`v8_control percentage value=5 source=process.execArgv heap=${percentageOnly.settings.v8HeapSizeLimit.bytes} control=${percentageControl}`);
  console.log(`v8_control percentage_over_size effective=percentage heap=${both.settings.v8HeapSizeLimit.bytes} size_bytes=${both.settings.oldSpace.maxOldSpaceSize.bytes}`);
  console.log(`v8_control percentage_fraction value=5.5 heap=${fractional.settings.v8HeapSizeLimit.bytes}`);
});

test("telemetry does not publish unrelated NODE_OPTIONS contents", async () => {
  const secret = "SECRET_SHOULD_NOT_LEAK";
  const directory = await mkdtemp(join(tmpdir(), `node-http-${secret}-`));
  const preload = join(directory, `preload ${secret}.js`);
  await writeFile(preload, "");
  const nodeOptions = `--max-old-space-size="128" --require "${preload}"`;
  const server = await startReference({ PORT: "0", NODE_OPTIONS: nodeOptions });
  try {
    const telemetryText = await (await fetch(`${server.baseUrl}/telemetry`)).text();
    const metadataText = await (await fetch(`${server.baseUrl}/metadata`)).text();
    assert.equal(telemetryText.includes(secret), false);
    assert.equal(metadataText.includes(secret), false);
    assert.equal(telemetryText.includes(preload), false);
    assert.equal(telemetryText.includes("--require"), false);
    const telemetry = JSON.parse(telemetryText) as TelemetrySettings;
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.value, 128);
    assert.equal(telemetry.settings.oldSpace.maxOldSpaceSize.source, "NODE_OPTIONS");
    const control = await controlHeapLimit(["--max-old-space-size=128"]);
    assert.equal(telemetry.settings.v8HeapSizeLimit.bytes, control);
    console.log(`v8_control quoted_node_options value=128 source=NODE_OPTIONS heap=${telemetry.settings.v8HeapSizeLimit.bytes}`);
  } finally {
    await server.stop();
  }
});
