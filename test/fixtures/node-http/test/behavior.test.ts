import assert from "node:assert/strict";
import { test } from "node:test";
import { startReference } from "./spawn.js";

const cart = JSON.stringify({ items: [{ sku: "apple", quantity: 1 }] });

async function quote(baseUrl: string): Promise<Response> {
  return fetch(`${baseUrl}/v1/quote`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: cart,
  });
}

test("latency delay leaves the event loop free", async () => {
  const server = await startReference({
    PORT: "0",
    NODE_HTTP_MODE: "latency",
    NODE_HTTP_INTENSITY: "8",
  });
  try {
    const pending = quote(server.baseUrl);
    await new Promise((resolve) => setTimeout(resolve, 20));
    const started = Date.now();
    const health = await fetch(`${server.baseUrl}/healthz`);
    const elapsed = Date.now() - started;
    assert.equal(health.status, 200);
    assert.ok(elapsed < 80, `healthz took ${elapsed}ms during a 320ms delay`);
    const response = await pending;
    assert.equal(response.status, 200);
    const body = await response.json() as { totalCents: number; regression: { delayMs: number } };
    assert.equal(body.totalCents, 135);
    assert.equal(body.regression.delayMs, 320);
  } finally {
    await server.stop();
  }
});

test("cpu mode blocks another request in the same process", async () => {
  const server = await startReference({
    PORT: "0",
    NODE_HTTP_MODE: "cpu",
    NODE_HTTP_INTENSITY: "8",
  });
  try {
    const pending = quote(server.baseUrl);
    await new Promise((resolve) => setTimeout(resolve, 30));
    const healthStarted = Date.now();
    const health = await fetch(`${server.baseUrl}/healthz`);
    const healthMs = Date.now() - healthStarted;
    const response = await pending;
    assert.equal(response.status, 200);
    assert.equal(health.status, 200);
    const body = await response.json() as { totalCents: number; regression: { cpuIterations: number } };
    assert.equal(body.totalCents, 135);
    assert.equal(body.regression.cpuIterations, 64_000_000);
    assert.ok(healthMs > 40, `healthz returned in ${healthMs}ms while cpu work should have blocked the loop`);
  } finally {
    await server.stop();
  }
});
