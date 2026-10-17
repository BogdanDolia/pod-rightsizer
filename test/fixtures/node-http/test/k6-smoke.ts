import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { once } from "node:events";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { join } from "node:path";
import { promisify } from "node:util";
import { fixtureRoot } from "./paths.js";
import { startReference } from "./spawn.js";

const execute = promisify(execFile);
const version = (await readFile(join(fixtureRoot, "scenario/k6-version"), "utf8")).trim();
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
const output = await mkdtemp(join(experiments, "k6-smoke-"));
console.log(`k6_smoke_output=${output}`);

interface Metric {
  passes?: number;
  fails?: number;
  value?: number;
}

async function runScenario(name: string, baseUrl: string, shouldPass: boolean): Promise<void> {
  let status = 0;
  let stdout = "";
  let stderr = "";
  try {
    ({ stdout, stderr } = await execute("k6", [
      "run",
      "--summary-export", join(output, `${name}-summary.json`),
      "--out", `json=${join(output, `${name}-raw.json`)}`,
      "-e", `BASE_URL=${baseUrl}`,
      "-e", "EXPECTED_MODE=baseline",
      "-e", "EXPECTED_INTENSITY=0",
      "scenario/quote.js",
    ], {
      cwd: fixtureRoot,
      env: { ...process.env, K6_NO_USAGE_REPORT: "true" },
      timeout: 60_000,
      maxBuffer: 4 * 1024 * 1024,
    }));
  } catch (error) {
    const failure = error as { code?: number | string; killed?: boolean; stdout?: string; stderr?: string };
    if (typeof failure.code !== "number" || failure.killed) {
      throw error;
    }
    status = failure.code;
    stdout = failure.stdout ?? "";
    stderr = failure.stderr ?? "";
  }
  await writeFile(join(output, `${name}-stdout.txt`), stdout + stderr);
  console.log(`${name}_exit=${status}`);
  if (shouldPass) {
    assert.equal(status, 0, stdout + stderr);
  } else {
    assert.notEqual(status, 0, "HTTP 200 with a wrong total must fail the scenario");
  }

  const summary = JSON.parse(await readFile(join(output, `${name}-summary.json`), "utf8")) as {
    metrics: Record<string, Metric>;
  };
  const checks = summary.metrics["checks{scenario:measured}"];
  assert.ok(checks && typeof checks.passes === "number" && checks.passes > 0, "measured checks must execute");
  assert.equal(summary.metrics["http_req_failed{scenario:measured}"]?.value, 0, "HTTP requests must succeed");
  assert.equal(typeof checks.fails, "number");
  if (shouldPass) {
    assert.equal(checks.fails, 0);
  } else {
    assert.ok(checks.fails! > 0, "failure must include measured application checks");
  }
}

const baseline = await startReference({ PORT: "0", NODE_HTTP_MODE: "baseline", NODE_HTTP_INTENSITY: "0" });
const wrongAnswer = createServer((request, response) => {
  const url = request.url ?? "/";
  if (request.method === "GET" && url.startsWith("/load-phases")) {
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end(JSON.stringify({ measuredOpen: true, warmupInFlight: 0 }));
    return;
  }
  if (request.method === "POST" && url.startsWith("/load-phases/open-measured")) {
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end(JSON.stringify({ measuredOpen: true, warmupInFlight: 0 }));
    return;
  }
  response.writeHead(200, { "Content-Type": "application/json" });
  response.end(JSON.stringify(wrongPayload));
});
let wrongPayload: Record<string, unknown>;
try {
  const response = await fetch(`${baseline.baseUrl}/v1/quote`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: await readFile(join(fixtureRoot, "scenario/quote-input.json"), "utf8"),
  });
  assert.equal(response.status, 200);
  wrongPayload = await response.json() as Record<string, unknown>;
  assert.equal(typeof wrongPayload.totalCents, "number");
  wrongPayload.totalCents = Number(wrongPayload.totalCents) + 1;
  wrongAnswer.listen(0, "127.0.0.1");
  await once(wrongAnswer, "listening");
  const address = wrongAnswer.address();
  assert.ok(address !== null && typeof address !== "string");

  await runScenario("baseline", baseline.baseUrl, true);
  await runScenario("wrong-answer", `http://127.0.0.1:${address.port}`, false);
  console.log("RESULT level=k6-smoke baseline=pass wrong_answer_gate=pass");
  console.log("Container, regression-mode, and kind experiments were not run.");
} finally {
  if (wrongAnswer.listening) {
    await new Promise<void>((resolve, reject) => wrongAnswer.close((error) => error ? reject(error) : resolve()));
  }
  assert.equal(await baseline.stop(), 0, "baseline process must stop cleanly");
}
