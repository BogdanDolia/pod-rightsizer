import { eventLoopUtilization, monitorEventLoopDelay, type ELDHistogram, type EventLoopUtilization } from "node:perf_hooks";
import { getHeapStatistics } from "node:v8";
import { ELU_INTERVAL_MS, EVENT_LOOP_DELAY_RESOLUTION_MS } from "./limits.js";
import type { RetentionStats } from "./retention.js";

export const TELEMETRY_FORMAT = "node-http-telemetry/v1";

const DISCLAIMER =
  "Fixture diagnostics only. A missing signal is not healthy, not a validated candidate, and not a telemetry_validated runtime profile.";

export interface DelayHistogramView {
  count: number;
  min: number;
  max: number;
  mean: number;
  percentile(percentile: number): number;
}

interface OldSpaceFlag {
  state: "known" | "unset" | "invalid";
  flag: string;
  source: "process.execArgv" | "absent";
  unit?: "mebibytes" | "percent";
  value?: number;
  bytes?: number;
  byteConversion?: "mebibytes * 1048576";
  appliesTo: "v8_old_space";
  notContainerMemory: true;
  reason?: string;
}

export interface TelemetryIdentity {
  processId: string;
  pid: number;
  startedAt: string;
}

export interface RetentionView {
  heap: RetentionStats;
  buffer: RetentionStats;
}

function finiteInteger(value: number): number | undefined {
  if (!Number.isFinite(value)) {
    return undefined;
  }
  const rounded = Math.round(value);
  if (!Number.isSafeInteger(rounded) || rounded < 0) {
    return undefined;
  }
  return rounded;
}

export function formatEventLoopDelay(histogram: DelayHistogramView, window: {
  startedAt: string;
  observedAt: string;
}): Record<string, unknown> {
  const base = {
    unit: "nanoseconds",
    sampling: "interval",
    resolutionMs: EVENT_LOOP_DELAY_RESOLUTION_MS,
    resetOnRead: false,
    window: { kind: "cumulative_since_enable", ...window },
  };
  if (histogram.count <= 0) {
    return { ...base, state: "missing_signal", reason: "histogram_empty" };
  }
  const minNs = finiteInteger(histogram.min);
  const maxNs = finiteInteger(histogram.max);
  const meanNs = finiteInteger(histogram.mean);
  const p50Ns = finiteInteger(histogram.percentile(50));
  const p99Ns = finiteInteger(histogram.percentile(99));
  if (
    minNs === undefined ||
    maxNs === undefined ||
    meanNs === undefined ||
    p50Ns === undefined ||
    p99Ns === undefined
  ) {
    return { ...base, state: "missing_signal", reason: "non_finite_histogram", count: histogram.count };
  }
  return {
    ...base,
    state: "known",
    count: histogram.count,
    statistics: "min, mean, p50, p99, max",
    minNs,
    meanNs,
    p50Ns,
    p99Ns,
    maxNs,
  };
}

function readFlag(argv: readonly string[], name: string): string | undefined {
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === undefined) {
      continue;
    }
    if (arg === name) {
      return argv[index + 1];
    }
    if (arg.startsWith(`${name}=`)) {
      return arg.slice(name.length + 1);
    }
  }
  return undefined;
}

function readPositiveIntegerFlag(argv: readonly string[], flag: string, unit: "mebibytes" | "percent"): OldSpaceFlag {
  const raw = readFlag(argv, flag);
  if (raw === undefined) {
    return { state: "unset", flag, source: "absent", appliesTo: "v8_old_space", notContainerMemory: true };
  }
  if (!/^[1-9]\d*$/.test(raw)) {
    return {
      state: "invalid",
      flag,
      source: "process.execArgv",
      appliesTo: "v8_old_space",
      notContainerMemory: true,
      reason: "not_a_positive_integer",
    };
  }
  const value = Number(raw);
  const known: OldSpaceFlag = {
    state: "known",
    flag,
    source: "process.execArgv",
    unit,
    value,
    appliesTo: "v8_old_space",
    notContainerMemory: true,
  };
  if (unit === "mebibytes") {
    known.bytes = value * 1024 * 1024;
    known.byteConversion = "mebibytes * 1048576";
  }
  return known;
}

export function readOldSpace(argv: readonly string[]): Record<string, unknown> {
  const maxOldSpaceSize = readPositiveIntegerFlag(argv, "--max-old-space-size", "mebibytes");
  const maxOldSpaceSizePercentage = readPositiveIntegerFlag(argv, "--max-old-space-size-percentage", "percent");
  return {
    maxOldSpaceSize,
    maxOldSpaceSizePercentage,
    precedence: "max-old-space-size-percentage overrides max-old-space-size when both are set",
    distinctFrom: "v8HeapSizeLimit",
  };
}

function formatElu(sample: EventLoopUtilization): Record<string, unknown> | undefined {
  if (!Number.isFinite(sample.idle) || !Number.isFinite(sample.active) || !Number.isFinite(sample.utilization)) {
    return undefined;
  }
  if (sample.idle < 0 || sample.active < 0 || sample.utilization < 0 || sample.utilization > 1) {
    return undefined;
  }
  if (sample.idle === 0 && sample.active === 0) {
    return undefined;
  }
  return {
    state: "known",
    unit: "ratio",
    idleActiveUnit: "milliseconds",
    idleMs: sample.idle,
    activeMs: sample.active,
    utilization: sample.utilization,
  };
}

export class TelemetryCollector {
  private readonly histogram: ELDHistogram;
  private readonly enabledAt: string;
  private readonly anchor: EventLoopUtilization;
  private previous: EventLoopUtilization;
  private intervalStartedAt: string;
  private lastInterval: Record<string, unknown> | null = null;
  private readonly timer: NodeJS.Timeout;

  constructor(private readonly retention: () => RetentionView) {
    this.histogram = monitorEventLoopDelay({ resolution: EVENT_LOOP_DELAY_RESOLUTION_MS });
    this.histogram.enable();
    this.enabledAt = new Date().toISOString();
    this.anchor = eventLoopUtilization();
    this.previous = this.anchor;
    this.intervalStartedAt = this.enabledAt;
    this.timer = setInterval(() => this.sampleInterval(), ELU_INTERVAL_MS);
    this.timer.unref();
  }

  stop(): void {
    clearInterval(this.timer);
    this.histogram.disable();
  }

  private sampleInterval(): void {
    const current = eventLoopUtilization();
    // utilization1 is the later sample. utilization2 is the earlier sample.
    const delta = eventLoopUtilization(current, this.previous);
    const endedAt = new Date().toISOString();
    const formatted = formatElu(delta);
    this.lastInterval = formatted
      ? {
          ...formatted,
          nominalIntervalMs: ELU_INTERVAL_MS,
          startedAt: this.intervalStartedAt,
          endedAt,
          resetOnRead: false,
        }
      : {
          state: "missing_signal",
          reason: "non_finite_interval",
          nominalIntervalMs: ELU_INTERVAL_MS,
          startedAt: this.intervalStartedAt,
          endedAt,
          resetOnRead: false,
        };
    this.previous = current;
    this.intervalStartedAt = endedAt;
  }

  snapshot(identity: TelemetryIdentity, buildId: string): Record<string, unknown> {
    const observedAt = new Date().toISOString();
    const memory = process.memoryUsage();
    const heap = getHeapStatistics();
    const retention = this.retention();
    const cumulative = formatElu(eventLoopUtilization(this.anchor));
    return {
      formatVersion: TELEMETRY_FORMAT,
      disclaimer: DISCLAIMER,
      observedAt,
      processUptimeSeconds: process.uptime(),
      identity,
      build: { buildId },
      snapshot: {
        memory: {
          kind: "instant",
          unit: "bytes",
          heapUsedBytes: memory.heapUsed,
          heapTotalBytes: memory.heapTotal,
          rssBytes: memory.rss,
          externalBytes: memory.external,
          arrayBuffersBytes: memory.arrayBuffers,
          arrayBuffersSubsetOfExternal: true,
          rssIsNotContainerMemory: true,
        },
        retention: {
          kind: "instant_gauge",
          unit: "application_accounted_bytes",
          heap: {
            blocks: retention.heap.count,
            accountedBytes: retention.heap.accountedBytes,
          },
          buffer: {
            blocks: retention.buffer.count,
            accountedBytes: retention.buffer.accountedBytes,
          },
        },
      },
      cumulative: {
        eventLoopDelay: formatEventLoopDelay(this.histogram, {
          startedAt: this.enabledAt,
          observedAt,
        }),
        eventLoopUtilization: cumulative
          ? {
              ...cumulative,
              kind: "delta_since_collector_start",
              startedAt: this.enabledAt,
              observedAt,
              resetOnRead: false,
            }
          : {
              state: "missing_signal",
              reason: "no_elapsed_time",
              kind: "delta_since_collector_start",
              startedAt: this.enabledAt,
              observedAt,
              resetOnRead: false,
            },
        retentionEvictions: {
          kind: "cumulative_since_start",
          heap: retention.heap.evictions,
          buffer: retention.buffer.evictions,
          heapRejectedBlocks: retention.heap.rejected,
          bufferRejectedBlocks: retention.buffer.rejected,
          resetOnRead: false,
        },
      },
      interval: {
        eventLoopUtilization: this.lastInterval ?? {
          state: "missing_signal",
          reason: "interval_not_complete",
          nominalIntervalMs: ELU_INTERVAL_MS,
          resetOnRead: false,
        },
      },
      settings: {
        oldSpace: readOldSpace(process.execArgv),
        v8HeapSizeLimit: {
          state: "known",
          source: "v8.getHeapStatistics().heap_size_limit",
          unit: "bytes",
          bytes: heap.heap_size_limit,
          distinctFrom: "maxOldSpaceSize",
          notContainerMemory: true,
        },
      },
      missingSignals: {
        gc: { state: "missing_signal", reason: "not_collected" },
        containerMemory: {
          state: "missing_signal",
          reason: "not_collected",
          rssIsNotASubstitute: true,
        },
      },
    };
  }
}
