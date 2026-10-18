import express, { type ErrorRequestHandler, type Express, type NextFunction, type Request, type Response } from "express";
import { InFlightGate } from "./capacity.js";
import type { AppConfig, Mode } from "./config.js";
import type { BuildManifest } from "./build-manifest.js";
import {
  BUFFER_BYTES_PER_INTENSITY,
  CPU_ITERATIONS_PER_INTENSITY,
  DELAY_MS_PER_INTENSITY,
  HEAP_ACCOUNTING_BYTES_PER_OBJECT,
  HEAP_OBJECTS_PER_INTENSITY,
  JSON_BODY_LIMIT,
} from "./limits.js";
import { buildMetadata, createProcessIdentity, defaultRetention, type ProcessIdentity } from "./metadata.js";
import { parseQuoteBody, quoteCart, QuoteError, type Quote } from "./quote.js";
import { LoadPhaseLedger, loadPhaseName } from "./load-phases.js";
import { BoundedRetainer } from "./retention.js";
import { TelemetryCollector } from "./telemetry.js";
import { allocateHeapObjects, allocateUsedBuffer, delay, runCpuWork, type HeapObject } from "./work.js";

export interface RegressionReport {
  mode: Mode;
  intensity: number;
  cpuIterations: number;
  cpuDigest: number | null;
  heapObjects: number;
  heapChecksum: number | null;
  bufferBytes: number;
  bufferChecksum: number | null;
  delayMs: number;
  retained: boolean;
  retentionReason: string | null;
  evicted: number;
}

export interface AppResources {
  app: Express;
  config: AppConfig;
  identity: ProcessIdentity;
  manifest: BuildManifest;
  telemetry: TelemetryCollector;
  state: { draining: boolean };
  gate: InFlightGate;
  heapRetainer: BoundedRetainer<HeapObject[]>;
  bufferRetainer: BoundedRetainer<Buffer>;
}

function emptyRegression(config: AppConfig): RegressionReport {
  return {
    mode: config.mode,
    intensity: config.intensity,
    cpuIterations: 0,
    cpuDigest: null,
    heapObjects: 0,
    heapChecksum: null,
    bufferBytes: 0,
    bufferChecksum: null,
    delayMs: 0,
    retained: false,
    retentionReason: null,
    evicted: 0,
  };
}

export function createApp(input: {
  config: AppConfig;
  manifest: BuildManifest;
  identity?: ProcessIdentity;
  telemetry?: TelemetryCollector;
  maxInFlight?: number;
  heapRetentionMaxBytes?: number;
  bufferRetentionMaxBytes?: number;
  state?: { draining: boolean };
}): AppResources {
  const identity = input.identity ?? createProcessIdentity();
  const state = input.state ?? { draining: false };
  const heapRetainer = new BoundedRetainer<HeapObject[]>(input.heapRetentionMaxBytes ?? defaultRetention.heapRetentionMaxBytes);
  const bufferRetainer = new BoundedRetainer<Buffer>(input.bufferRetentionMaxBytes ?? defaultRetention.bufferRetentionMaxBytes);
  const telemetry = input.telemetry ?? new TelemetryCollector(() => ({
    heap: heapRetainer.stats(),
    buffer: bufferRetainer.stats(),
  }));
  const gate = new InFlightGate(input.maxInFlight ?? defaultRetention.maxInFlight);
  const loadPhases = new LoadPhaseLedger();
  const app = express();
  app.disable("x-powered-by");
  app.use(express.json({ limit: JSON_BODY_LIMIT }));

  app.get("/healthz", (_req, res) => {
    res.json({ status: "ok" });
  });

  app.get("/readyz", (_req, res) => {
    if (state.draining) {
      res.status(503).json({ status: "draining" });
      return;
    }
    res.json({ status: "ready" });
  });

  app.get("/metadata", (_req, res) => {
    res.set("Cache-Control", "no-store");
    res.json(buildMetadata({
      config: input.config,
      identity,
      manifest: input.manifest,
      maxInFlight: gate.limit,
      heapRetentionMaxBytes: heapRetainer.maxBytes,
      bufferRetentionMaxBytes: bufferRetainer.maxBytes,
    }));
  });

  app.get("/telemetry", (_req, res) => {
    res.set("Cache-Control", "no-store");
    res.json(telemetry.snapshot(
      { processId: identity.processId, pid: identity.pid, startedAt: identity.startedAt },
      input.manifest.buildId,
    ));
  });

  app.get("/load-phases", (_req, res) => {
    res.set("Cache-Control", "no-store");
    res.json(loadPhases.snapshot());
  });

  app.post("/load-phases/open-measured", (_req, res) => {
    res.set("Cache-Control", "no-store");
    const opened = loadPhases.tryOpenMeasured();
    res.status(opened ? 200 : 409).json(loadPhases.snapshot());
  });

  app.post("/v1/quote", async (req, res, next) => {
    const workId = loadPhases.begin(loadPhaseName(req.header("x-load-phase")));
    try {
      await quoteRequest(req, res, next);
    } finally {
      loadPhases.finish(workId);
    }
  });

  async function quoteRequest(req: Request, res: Response, next: NextFunction): Promise<void> {
    let quote: Quote;
    try {
      quote = quoteCart(parseQuoteBody(req.body));
    } catch (error) {
      if (error instanceof QuoteError) {
        res.status(400).json({ error: "invalid_quote", message: error.message });
        return;
      }
      next(error);
      return;
    }
    const regression = emptyRegression(input.config);
    if (input.config.mode === "baseline") {
      res.json({ ...quote, regression });
      return;
    }
    if (!gate.tryEnter()) {
      res.status(429).json({
        error: "capacity_exceeded",
        limit: gate.limit,
        inFlight: gate.inFlight,
        behavior: "reject_immediately",
      });
      return;
    }
    try {
      if (input.config.mode === "cpu") {
        const work = runCpuWork(input.config.intensity * CPU_ITERATIONS_PER_INTENSITY);
        regression.cpuIterations = work.iterations;
        regression.cpuDigest = work.digest;
      } else if (input.config.mode === "heap") {
        const objects = input.config.intensity * HEAP_OBJECTS_PER_INTENSITY;
        const work = allocateHeapObjects(objects);
        const retained = heapRetainer.retain(work.objects, objects * HEAP_ACCOUNTING_BYTES_PER_OBJECT);
        regression.heapObjects = objects;
        regression.heapChecksum = work.checksum;
        regression.retained = retained.retained;
        regression.retentionReason = retained.reason;
        regression.evicted = retained.evicted;
      } else if (input.config.mode === "buffer") {
        const bytes = input.config.intensity * BUFFER_BYTES_PER_INTENSITY;
        const work = allocateUsedBuffer(bytes);
        const retained = bufferRetainer.retain(work.buffer, bytes);
        regression.bufferBytes = bytes;
        regression.bufferChecksum = work.checksum;
        regression.retained = retained.retained;
        regression.retentionReason = retained.reason;
        regression.evicted = retained.evicted;
      } else {
        const delayMs = input.config.intensity * DELAY_MS_PER_INTENSITY;
        await delay(delayMs);
        regression.delayMs = delayMs;
      }
      res.json({ ...quote, regression });
    } catch (error) {
      next(error);
    } finally {
      gate.leave();
    }
  }

  app.use((_req, res) => {
    res.status(404).json({ error: "not_found" });
  });

  const handleError: ErrorRequestHandler = (error, _req, res, next) => {
    if (res.headersSent) {
      next(error);
      return;
    }
    const status = typeof error === "object" && error !== null && "status" in error && typeof (error as { status: unknown }).status === "number"
      ? (error as { status: number }).status
      : 500;
    res.status(status >= 400 && status <= 599 ? status : 500).json({
      error: status === 400 ? "invalid_json" : "internal",
    });
  };
  app.use(handleError);

  return { app, config: input.config, identity, manifest: input.manifest, telemetry, state, gate, heapRetainer, bufferRetainer };
}
