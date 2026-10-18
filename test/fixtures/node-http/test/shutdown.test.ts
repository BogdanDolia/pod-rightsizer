import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { test } from "node:test";
import { fixtureRoot } from "./paths.js";
import { startReference } from "./spawn.js";

test("invalid configuration exits before listening", async () => {
  const child = spawn(process.execPath, ["--enable-source-maps", "dist/server.js"], {
    cwd: fixtureRoot,
    env: { ...process.env, NODE_HTTP_MODE: "nope" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stderr = "";
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk: string) => {
    stderr += chunk;
  });
  const [code] = (await once(child, "exit")) as [number | null];
  assert.equal(code, 2);
  assert.match(stderr, /invalid configuration: unknown mode/);
});

test("SIGTERM drains readiness and exits within the grace period", async () => {
  const server = await startReference({
    PORT: "0",
    NODE_HTTP_MODE: "latency",
    NODE_HTTP_INTENSITY: "8",
    NODE_HTTP_SHUTDOWN_GRACE_MS: "800",
  });
  const quote = fetch(`${server.baseUrl}/v1/quote`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ items: [{ sku: "apple", quantity: 1 }] }),
  }).then(
    async (response) => {
      await response.text();
      return response.status;
    },
    () => "closed" as const,
  );
  await new Promise((resolve) => setTimeout(resolve, 30));
  const started = Date.now();
  server.child.kill("SIGTERM");
  let readyStatus = 0;
  for (let attempt = 0; attempt < 20 && readyStatus !== 503; attempt += 1) {
    try {
      const ready = await fetch(`${server.baseUrl}/readyz`);
      readyStatus = ready.status;
      await ready.text();
    } catch {
      break;
    }
    if (readyStatus !== 503) {
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  }
  assert.equal(readyStatus, 503);
  const health = await fetch(`${server.baseUrl}/healthz`);
  assert.equal(health.status, 200);
  await health.text();
  const [code] = (await once(server.child, "exit")) as [number | null];
  const elapsed = Date.now() - started;
  assert.equal(code, 0);
  assert.ok(elapsed < 1_000, `shutdown took ${elapsed}ms`);
  await quote;
});

test("health stays distinct from readiness while the process is running", async () => {
  const server = await startReference({ PORT: "0" });
  try {
    const health = await (await fetch(`${server.baseUrl}/healthz`)).json() as { status: string };
    const ready = await (await fetch(`${server.baseUrl}/readyz`)).json() as { status: string };
    assert.equal(health.status, "ok");
    assert.equal(ready.status, "ready");
  } finally {
    const code = await server.stop();
    assert.equal(code, 0);
  }
});
