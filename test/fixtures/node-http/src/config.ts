import {
  DEFAULT_PORT,
  DEFAULT_SHUTDOWN_GRACE_MS,
  MAX_INTENSITY,
  MAX_SHUTDOWN_GRACE_MS,
  MIN_SHUTDOWN_GRACE_MS,
} from "./limits.js";

export const MODES = ["baseline", "cpu", "heap", "buffer", "latency"] as const;
export type Mode = (typeof MODES)[number];

export interface AppConfig {
  mode: Mode;
  intensity: number;
  port: number;
  shutdownGraceMs: number;
}

export class ConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ConfigError";
  }
}

function readInteger(name: string, raw: string | undefined, fallback: number): number {
  if (raw === undefined) {
    return fallback;
  }
  if (raw !== raw.trim() || raw.length === 0) {
    throw new ConfigError(`${name} must be an integer without whitespace`);
  }
  if (!/^-?\d+$/.test(raw)) {
    throw new ConfigError(`${name} must be an integer`);
  }
  const value = Number(raw);
  if (!Number.isSafeInteger(value)) {
    throw new ConfigError(`${name} must be an integer`);
  }
  return value;
}

export function parseConfig(env: NodeJS.ProcessEnv): AppConfig {
  const modeRaw = env.NODE_HTTP_MODE ?? "baseline";
  if (!(MODES as readonly string[]).includes(modeRaw)) {
    throw new ConfigError(`unknown mode ${JSON.stringify(modeRaw)}`);
  }
  const mode = modeRaw as Mode;
  const intensity = readInteger("NODE_HTTP_INTENSITY", env.NODE_HTTP_INTENSITY, 0);
  if (mode === "baseline" && intensity !== 0) {
    throw new ConfigError("baseline intensity must be 0");
  }
  if (mode !== "baseline" && (intensity < 1 || intensity > MAX_INTENSITY)) {
    throw new ConfigError(`intensity must be an integer from 1 to ${MAX_INTENSITY}`);
  }
  const port = readInteger("PORT", env.PORT, DEFAULT_PORT);
  if (port !== 0 && (port < 1 || port > 65535)) {
    throw new ConfigError("PORT must be 0 or an integer from 1 to 65535");
  }
  const shutdownGraceMs = readInteger(
    "NODE_HTTP_SHUTDOWN_GRACE_MS",
    env.NODE_HTTP_SHUTDOWN_GRACE_MS,
    DEFAULT_SHUTDOWN_GRACE_MS,
  );
  if (shutdownGraceMs < MIN_SHUTDOWN_GRACE_MS || shutdownGraceMs > MAX_SHUTDOWN_GRACE_MS) {
    throw new ConfigError(
      `NODE_HTTP_SHUTDOWN_GRACE_MS must be an integer from ${MIN_SHUTDOWN_GRACE_MS} to ${MAX_SHUTDOWN_GRACE_MS}`,
    );
  }
  return { mode, intensity, port, shutdownGraceMs };
}
