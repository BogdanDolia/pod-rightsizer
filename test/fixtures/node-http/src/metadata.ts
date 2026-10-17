import { randomUUID } from "node:crypto";
import type { AppConfig } from "./config.js";
import type { BuildManifest } from "./build-manifest.js";
import {
  BUFFER_RETENTION_MAX_BYTES,
  DELAY_MS_PER_INTENSITY,
  HEAP_RETENTION_MAX_BYTES,
  MAX_IN_FLIGHT,
} from "./limits.js";

export const METADATA_FORMAT = "node-http-metadata/v1";

export interface ProcessIdentity {
  processId: string;
  pid: number;
  startedAt: string;
}

export function createProcessIdentity(): ProcessIdentity {
  const origin = performance.timeOrigin;
  const startedAt = Number.isFinite(origin) && origin > 0 ? new Date(origin).toISOString() : new Date().toISOString();
  return { processId: randomUUID(), pid: process.pid, startedAt };
}

export function buildMetadata(input: {
  config: AppConfig;
  identity: ProcessIdentity;
  manifest: BuildManifest;
  maxInFlight: number;
  heapRetentionMaxBytes: number;
  bufferRetentionMaxBytes: number;
}): Record<string, unknown> {
  return {
    formatVersion: METADATA_FORMAT,
    logicalService: input.manifest.logicalService,
    appRoot: input.manifest.appRoot,
    node: {
      version: process.version,
      v8: process.versions.v8,
      lts: process.release.lts ?? null,
      platform: process.platform,
      arch: process.arch,
    },
    process: {
      id: input.identity.processId,
      pid: input.identity.pid,
      startedAt: input.identity.startedAt,
      identity: "process id, pid, and start time together; pid alone is not an identity",
    },
    regression: {
      mode: input.config.mode,
      intensity: input.config.intensity,
      settings: {
        maxInFlight: input.maxInFlight,
        heapRetentionMaxBytes: input.heapRetentionMaxBytes,
        bufferRetentionMaxBytes: input.bufferRetentionMaxBytes,
        delayMsPerIntensity: DELAY_MS_PER_INTENSITY,
        shutdownGraceMs: input.config.shutdownGraceMs,
        port: input.config.port,
      },
    },
    build: {
      buildId: input.manifest.buildId,
      buildIdAlgorithm: input.manifest.buildIdAlgorithm,
      sourceRevision: input.manifest.source.revision,
      treeState: input.manifest.source.treeState,
      sourceMethod: input.manifest.source.method,
      verifiedLinkage: input.manifest.source.verifiedLinkage,
      manifestPath: "dist/build-manifest.json",
    },
    artifacts: input.manifest.artifacts.map((artifact) => ({
      path: artifact.path,
      sha256: artifact.sha256,
      sourceMapPath: artifact.sourceMap.path,
      sourceMapSha256: artifact.sourceMap.sha256,
      sources: artifact.sources,
    })),
  };
}

export const defaultRetention = {
  maxInFlight: MAX_IN_FLIGHT,
  heapRetentionMaxBytes: HEAP_RETENTION_MAX_BYTES,
  bufferRetentionMaxBytes: BUFFER_RETENTION_MAX_BYTES,
};
