import { createServer, type Server } from "node:http";
import { readFileSync, realpathSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { createApp } from "./app.js";
import { loadBuildManifest, ManifestError } from "./build-manifest.js";
import { ConfigError, parseConfig, type AppConfig } from "./config.js";
import type { BuildManifest } from "./build-manifest.js";
import type { ProcessIdentity } from "./metadata.js";
import { TelemetryCollector } from "./telemetry.js";

export interface RunningServer {
  baseUrl: string;
  port: number;
  identity: ProcessIdentity;
  telemetry: TelemetryCollector;
  state: { draining: boolean };
  activeConnections(): Promise<number>;
  closeIdleConnections(): void;
  close(): Promise<void>;
}

export async function startServer(input: {
  config: AppConfig;
  manifest: BuildManifest;
  host?: string;
  maxInFlight?: number;
  heapRetentionMaxBytes?: number;
  bufferRetentionMaxBytes?: number;
}): Promise<RunningServer> {
  const resources = createApp(input);
  const server = createServer(resources.app);
  const host = input.host ?? "127.0.0.1";
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(input.config.port, host, () => {
      server.off("error", reject);
      resolve();
    });
  });
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("expected a TCP port");
  }
  let closing: Promise<void> | undefined;
  return {
    baseUrl: `http://${host === "0.0.0.0" ? "127.0.0.1" : host}:${address.port}`,
    port: address.port,
    identity: resources.identity,
    telemetry: resources.telemetry,
    state: resources.state,
    activeConnections: () => new Promise((resolve, reject) => {
      server.getConnections((error, count) => {
        if (error) {
          reject(error);
          return;
        }
        resolve(count);
      });
    }),
    closeIdleConnections: () => {
      server.closeIdleConnections();
    },
    close: () => {
      if (closing !== undefined) {
        return closing;
      }
      resources.state.draining = true;
      resources.telemetry.stop();
      server.closeIdleConnections();
      closing = closeHttpServer(server);
      return closing;
    },
  };
}

function closeHttpServer(server: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error);
        return;
      }
      resolve();
    });
  });
}

export function installShutdown(running: RunningServer, graceMs: number): void {
  let stopping = false;
  const shutdown = (): void => {
    if (stopping) {
      return;
    }
    stopping = true;
    running.state.draining = true;
    let exiting = false;
    let watch: NodeJS.Timeout | undefined;
    const force = setTimeout(() => {
      process.exit(0);
    }, graceMs);
    force.unref();
    const finish = (): void => {
      if (exiting) {
        return;
      }
      exiting = true;
      if (watch !== undefined) {
        clearInterval(watch);
      }
      clearTimeout(force);
      void running.close().finally(() => {
        process.exit(0);
      });
    };
    watch = setInterval(() => {
      running.closeIdleConnections();
      void running.activeConnections().then((count) => {
        if (count === 0) {
          finish();
        }
      }, () => {
        finish();
      });
    }, 15);
    watch.unref();
  };
  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);
}

async function main(): Promise<void> {
  let config: AppConfig;
  try {
    config = parseConfig(process.env);
  } catch (error) {
    if (error instanceof ConfigError) {
      console.error(`invalid configuration: ${error.message}`);
      process.exit(2);
    }
    throw error;
  }
  let manifest: BuildManifest;
  try {
    manifest = loadBuildManifest(readFileSync(new URL("./build-manifest.json", import.meta.url)));
  } catch (error) {
    const message = error instanceof ManifestError ? error.message : "build manifest is missing";
    console.error(`invalid configuration: ${message}`);
    process.exit(2);
  }
  const running = await startServer({ config, manifest, host: "0.0.0.0" });
  console.log(`node-http-reference listening port=${running.port}`);
  installShutdown(running, config.shutdownGraceMs);
}

function isDirectRun(): boolean {
  const entry = process.argv[1];
  if (entry === undefined) {
    return false;
  }
  try {
    return import.meta.url === pathToFileURL(realpathSync(entry)).href;
  } catch {
    return false;
  }
}

if (isDirectRun()) {
  await main();
}
