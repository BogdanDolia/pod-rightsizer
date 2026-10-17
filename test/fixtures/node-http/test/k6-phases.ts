import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { once } from "node:events";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";
import { LoadPhaseLedger, loadPhaseName } from "../src/load-phases.js";
import { fixtureRoot } from "./paths.js";

const execute = promisify(execFile);
const version = (await readFile(join(fixtureRoot, "scenario/k6-version"), "utf8")).trim();
const delayMs = 400;
let installedVersion = "";
try {
  const installed = await execute("k6", ["version"]);
  installedVersion = installed.stdout.match(/^k6 v([^\s]+)/m)?.[1] ?? "";
} catch (error) {
  const failure = error as { code?: string };
  if (failure.code === "ENOENT") {
    console.error(`pinned k6 ${version} is not installed; refusing to run and not falling back to the built-in Go tester`);
    console.error("install: bash scripts/install-fixture-k6.sh <bin-directory>");
    process.exit(1);
  }
  throw error;
}
if (installedVersion !== version) {
  console.error(`pinned k6 ${version} is required, found ${installedVersion || "unknown"}; refusing to run and not falling back to the built-in Go tester`);
  process.exit(1);
}

const experiments = join(fixtureRoot, ".experiments");
await mkdir(experiments, { recursive: true });
const output = await mkdtemp(join(experiments, "k6-phases-"));
console.log(`k6_phases_output=${output}`);

const expected = JSON.parse(await readFile(join(fixtureRoot, "scenario/expected.json"), "utf8")) as {
  currency: string;
  subtotalCents: number;
  taxCents: number;
  totalCents: number;
  lineCount: number;
};
const payload = JSON.stringify({
  currency: expected.currency,
  lines: Array.from({ length: expected.lineCount }, () => ({ sku: "fixture" })),
  subtotalCents: expected.subtotalCents,
  taxCents: expected.taxCents,
  totalCents: expected.totalCents,
  regression: { mode: "baseline", intensity: 0 },
});
const ledger = new LoadPhaseLedger();

async function readBody(request: IncomingMessage): Promise<void> {
  for await (const _chunk of request) {
    // The body is not part of the phase boundary.
  }
}

function sendJson(response: ServerResponse, status: number, body: unknown): void {
  if (response.writableEnded || response.destroyed) {
    return;
  }
  response.writeHead(status, { "Content-Type": "application/json" });
  response.end(JSON.stringify(body));
}

const server = createServer((request, response) => {
  const url = request.url ?? "/";
  if (request.method === "GET" && url.startsWith("/load-phases")) {
    sendJson(response, 200, ledger.snapshot());
    return;
  }
  if (request.method === "POST" && url.startsWith("/load-phases/open-measured")) {
    request.resume();
    const opened = ledger.tryOpenMeasured();
    sendJson(response, opened ? 200 : 409, ledger.snapshot());
    return;
  }
  if (request.method === "POST" && url.startsWith("/v1/quote")) {
    const header = request.headers["x-load-phase"];
    const phase = loadPhaseName(typeof header === "string" ? header : undefined);
    const workId = ledger.begin(phase);
    void (async () => {
      try {
        await readBody(request);
        await new Promise((resolve) => setTimeout(resolve, delayMs));
        sendJson(response, 200, JSON.parse(payload) as unknown);
      } finally {
        ledger.finish(workId);
      }
    })();
    return;
  }
  sendJson(response, 404, { error: "not_found" });
});

try {
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  assert.ok(address !== null && typeof address !== "string");
  const baseUrl = `http://127.0.0.1:${address.port}`;
  try {
    await execute("k6", [
      "run",
      "--summary-export", join(output, "summary.json"),
      "--out", `json=${join(output, "raw.json")}`,
      "-e", `BASE_URL=${baseUrl}`,
      "-e", "EXPECTED_MODE=baseline",
      "-e", "EXPECTED_INTENSITY=0",
      "scenario/quote.js",
    ], {
      cwd: fixtureRoot,
      env: { ...process.env, K6_NO_USAGE_REPORT: "true" },
      timeout: 60_000,
      maxBuffer: 8 * 1024 * 1024,
    });
  } catch (error) {
    const failure = error as { stdout?: string; stderr?: string };
    await writeFile(join(output, "stdout.txt"), `${failure.stdout ?? ""}${failure.stderr ?? ""}`);
    throw error;
  }
  const phases = ledger.snapshot() as {
    warmupInFlight: number;
    warmupInFlightAtFirstMeasuredStart: number | null;
    measuredOpenedOffsetMs: number | null;
    warmup: {
      count: number;
      firstStartedAt: string | null;
      firstStartedOffsetMs: number | null;
      lastWorkFinishedAt: string | null;
      lastWorkFinishedOffsetMs: number | null;
      shortestWorkMs: number | null;
    };
    measured: {
      count: number;
      firstStartedAt: string | null;
      firstStartedOffsetMs: number | null;
    };
  };
  await writeFile(join(output, "phases.json"), JSON.stringify(phases, null, 2));
  assert.ok(phases.warmup.count > 0, "warmup requests must reach the server");
  assert.ok(phases.measured.count > 0, "measured requests must reach the server");
  assert.equal(phases.warmupInFlight, 0);
  assert.equal(phases.warmupInFlightAtFirstMeasuredStart, 0);
  const warmupFinished = phases.warmup.lastWorkFinishedOffsetMs;
  const measuredStarted = phases.measured.firstStartedOffsetMs;
  const opened = phases.measuredOpenedOffsetMs;
  assert.equal(typeof warmupFinished, "number");
  assert.equal(typeof measuredStarted, "number");
  assert.equal(typeof opened, "number");
  assert.ok(warmupFinished! <= measuredStarted!, `warmup work finished at ${warmupFinished} after measured started at ${measuredStarted}`);
  assert.ok(opened! <= measuredStarted!, "measured requests started before the server opened the phase");
  assert.ok((phases.warmup.shortestWorkMs ?? 0) >= delayMs - 50, "server work must run for the full delay, not stop when a client gives up");
  const gapMs = measuredStarted! - warmupFinished!;
  console.log(`observed_warmup_first_started_at=${phases.warmup.firstStartedAt}`);
  console.log(`observed_warmup_last_work_finished_at=${phases.warmup.lastWorkFinishedAt}`);
  console.log(`observed_warmup_last_work_finished_offset_ms=${warmupFinished}`);
  console.log(`observed_measured_first_started_at=${phases.measured.firstStartedAt}`);
  console.log(`observed_measured_first_started_offset_ms=${measuredStarted}`);
  console.log(`observed_gap_ms=${gapMs}`);
  console.log(`observed_warmup_count=${phases.warmup.count} observed_measured_count=${phases.measured.count} delay_ms=${delayMs}`);
  console.log("RESULT level=k6-phases separated=pass");
} finally {
  server.closeAllConnections();
  if (server.listening) {
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
}
