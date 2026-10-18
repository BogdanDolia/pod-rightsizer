import assert from "node:assert/strict";
import { test } from "node:test";
import { parseConfig, ConfigError } from "../src/config.js";

function expectInvalid(env: NodeJS.ProcessEnv, fragment: string): void {
  assert.throws(() => parseConfig(env), (error: unknown) => {
    assert.ok(error instanceof ConfigError);
    assert.match(error.message, new RegExp(fragment));
    return true;
  });
}

test("baseline is the default", () => {
  const config = parseConfig({});
  assert.deepEqual(config, { mode: "baseline", intensity: 0, port: 8080, shutdownGraceMs: 5000 });
});

test("accepts an explicit regression mode", () => {
  const config = parseConfig({ NODE_HTTP_MODE: "cpu", NODE_HTTP_INTENSITY: "4", PORT: "0" });
  assert.equal(config.mode, "cpu");
  assert.equal(config.intensity, 4);
  assert.equal(config.port, 0);
});

test("rejects invalid configuration", () => {
  expectInvalid({ NODE_HTTP_MODE: "sleep" }, "unknown mode");
  expectInvalid({ NODE_HTTP_MODE: "CPU" }, "unknown mode");
  expectInvalid({ NODE_HTTP_MODE: "cpu" }, "intensity must be an integer from 1 to 8");
  expectInvalid({ NODE_HTTP_MODE: "cpu", NODE_HTTP_INTENSITY: "0" }, "intensity must be an integer from 1 to 8");
  expectInvalid({ NODE_HTTP_MODE: "cpu", NODE_HTTP_INTENSITY: "9" }, "intensity must be an integer from 1 to 8");
  expectInvalid({ NODE_HTTP_MODE: "cpu", NODE_HTTP_INTENSITY: "-1" }, "intensity must be an integer from 1 to 8");
  expectInvalid({ NODE_HTTP_MODE: "cpu", NODE_HTTP_INTENSITY: "1.5" }, "must be an integer");
  expectInvalid({ NODE_HTTP_MODE: "cpu", NODE_HTTP_INTENSITY: "NaN" }, "must be an integer");
  expectInvalid({ NODE_HTTP_MODE: "cpu", NODE_HTTP_INTENSITY: "Infinity" }, "must be an integer");
  expectInvalid({ NODE_HTTP_MODE: "cpu", NODE_HTTP_INTENSITY: "" }, "must be an integer");
  expectInvalid({ NODE_HTTP_MODE: "cpu", NODE_HTTP_INTENSITY: " 1" }, "without whitespace");
  expectInvalid({ NODE_HTTP_INTENSITY: "1" }, "baseline intensity must be 0");
  expectInvalid({ NODE_HTTP_MODE: "baseline", NODE_HTTP_INTENSITY: "-3" }, "baseline intensity must be 0");
  expectInvalid({ PORT: "70000" }, "PORT");
  expectInvalid({ NODE_HTTP_SHUTDOWN_GRACE_MS: "10" }, "NODE_HTTP_SHUTDOWN_GRACE_MS");
});
