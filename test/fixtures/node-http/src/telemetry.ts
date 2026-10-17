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

type OldSpaceState = "known" | "unset" | "invalid" | "missing_signal";
type OldSpaceSource = "process.execArgv" | "NODE_OPTIONS" | "absent";

interface OldSpaceFlag {
  state: OldSpaceState;
  flag: string;
  source: OldSpaceSource;
  unit?: "mebibytes" | "percent";
  value?: number;
  bytes?: number;
  byteConversion?: "mebibytes * 1048576";
  appliesTo: "v8_old_space";
  notContainerMemory: true;
  v8ZeroMeansDefault?: true;
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

const OLD_SPACE_PRECEDENCE =
  "Node.js 24.22.0 applies NODE_OPTIONS before command-line options. The last value of one single-value flag wins, and dash and underscore spellings are the same flag. --max-old-space-size-percentage takes precedence over --max-old-space-size when both are set, in either order. The percentage is not converted to bytes. Neither setting is copied from v8HeapSizeLimit.";

const SUPPORTED_OLD_SPACE_FLAGS = ["max-old-space-size", "max-old-space-size-percentage"] as const;
type SupportedOldSpaceFlag = (typeof SUPPORTED_OLD_SPACE_FLAGS)[number];

export interface OldSpaceSources {
  execArgv: readonly string[];
  nodeOptions?: string | undefined;
}

export type NodeOptionsParse =
  | { ok: true; tokens: string[] }
  | { ok: false; reason: "node_options_unterminated_quote" | "node_options_invalid_escape" };

interface SeenFlag {
  source: Exclude<OldSpaceSource, "absent">;
  rawValue: string | null;
}

export function parseNodeOptions(input: string): NodeOptionsParse {
  const tokens: string[] = [];
  let current = "";
  let inString = false;
  let started = false;
  for (let index = 0; index < input.length; index += 1) {
    const character = input[index];
    if (character === undefined) {
      continue;
    }
    if (character === "\\" && inString) {
      const escaped = input[index + 1];
      if (escaped === undefined) {
        return { ok: false, reason: "node_options_invalid_escape" };
      }
      current += escaped;
      started = true;
      index += 1;
      continue;
    }
    if (character === " " && !inString) {
      if (started) {
        tokens.push(current);
        current = "";
        started = false;
      }
      continue;
    }
    if (character === '"') {
      inString = !inString;
      continue;
    }
    current += character;
    started = true;
  }
  if (inString) {
    return { ok: false, reason: "node_options_unterminated_quote" };
  }
  if (started) {
    tokens.push(current);
  }
  return { ok: true, tokens };
}

function absentFlag(flag: string): OldSpaceFlag {
  return { state: "unset", flag, source: "absent", appliesTo: "v8_old_space", notContainerMemory: true };
}

function unparsedFlag(flag: string, reason: string): OldSpaceFlag {
  return {
    state: "missing_signal",
    flag,
    source: "NODE_OPTIONS",
    appliesTo: "v8_old_space",
    notContainerMemory: true,
    reason,
  };
}

function baseFlag(flag: string, source: OldSpaceSource, state: OldSpaceState, reason?: string): OldSpaceFlag {
  return {
    state,
    flag,
    source,
    appliesTo: "v8_old_space",
    notContainerMemory: true,
    ...(reason === undefined ? {} : { reason }),
  };
}

function parseMebibytes(raw: string): { state: "known"; value: number; v8ZeroMeansDefault?: true } | { state: "invalid" | "missing_signal"; reason: string } {
  if (raw.length === 0) {
    return { state: "invalid", reason: "empty_value" };
  }
  // Node 24.22.0 accepts an optional leading space and plus, then decimal digits.
  // A separated argv token is not a value for this flag. 0 selects V8's default.
  if (!/^ *\+?\d+$/.test(raw)) {
    return { state: "missing_signal", reason: "unsupported_numeric_syntax" };
  }
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < 0) {
    return { state: "missing_signal", reason: "unsupported_numeric_syntax" };
  }
  if (value === 0) {
    return { state: "known", value: 0, v8ZeroMeansDefault: true };
  }
  return { state: "known", value };
}

function parsePercentage(raw: string): { state: "known"; value: number } | { state: "invalid" | "missing_signal"; reason: string } {
  if (raw.length === 0) {
    return { state: "invalid", reason: "empty_value" };
  }
  const decimal = /^ *\+?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$/;
  const hexadecimal = /^ *0[xX][0-9a-fA-F]+$/;
  if (!decimal.test(raw) && !hexadecimal.test(raw)) {
    return { state: "missing_signal", reason: "unsupported_numeric_syntax" };
  }
  const value = Number(raw);
  if (!Number.isFinite(value)) {
    return { state: "missing_signal", reason: "unsupported_numeric_syntax" };
  }
  if (value <= 0 || value > 100) {
    return { state: "invalid", reason: "percentage_out_of_range" };
  }
  return { state: "known", value };
}

function absorbFlags(tokens: readonly string[], source: Exclude<OldSpaceSource, "absent">, seen: Map<SupportedOldSpaceFlag, SeenFlag>): void {
  for (const token of tokens) {
    if (!token.startsWith("--")) {
      continue;
    }
    const body = token.slice(2);
    const equals = body.indexOf("=");
    const rawName = equals === -1 ? body : body.slice(0, equals);
    const canonical = rawName.replaceAll("_", "-");
    if (canonical !== "max-old-space-size" && canonical !== "max-old-space-size-percentage") {
      continue;
    }
    // Node 24.22.0 does not accept the next argv element as the value.
    seen.set(canonical, { source, rawValue: equals === -1 ? null : body.slice(equals + 1) });
  }
}

function materialize(name: SupportedOldSpaceFlag, seen: SeenFlag | undefined): OldSpaceFlag {
  const flag = `--${name}`;
  if (seen === undefined) {
    return absentFlag(flag);
  }
  if (seen.rawValue === null) {
    return baseFlag(flag, seen.source, "invalid", "node_24_requires_equals");
  }
  if (name === "max-old-space-size") {
    const parsed = parseMebibytes(seen.rawValue);
    if (parsed.state !== "known") {
      return baseFlag(flag, seen.source, parsed.state, parsed.reason);
    }
    if (parsed.v8ZeroMeansDefault === true) {
      return {
        ...baseFlag(flag, seen.source, "known", "v8_zero_selects_the_default_old_space_size"),
        unit: "mebibytes",
        value: 0,
        v8ZeroMeansDefault: true,
      };
    }
    return {
      ...baseFlag(flag, seen.source, "known"),
      unit: "mebibytes",
      value: parsed.value,
      bytes: parsed.value * 1048576,
      byteConversion: "mebibytes * 1048576",
    };
  }
  const parsed = parsePercentage(seen.rawValue);
  if (parsed.state !== "known") {
    return baseFlag(flag, seen.source, parsed.state, parsed.reason);
  }
  return {
    ...baseFlag(flag, seen.source, "known"),
    unit: "percent",
    value: parsed.value,
  };
}

function selectEffective(size: OldSpaceFlag, percentage: OldSpaceFlag): Record<string, unknown> {
  if (size.state === "missing_signal" || percentage.state === "missing_signal") {
    return {
      state: "missing_signal",
      setting: null,
      source: size.state === "missing_signal" ? size.source : percentage.source,
      reason: size.reason ?? percentage.reason ?? "unparsed_old_space_input",
    };
  }
  if (percentage.state === "known") {
    return { state: "known", setting: "maxOldSpaceSizePercentage", source: percentage.source };
  }
  if (percentage.state === "invalid") {
    return { state: "invalid", setting: "maxOldSpaceSizePercentage", source: percentage.source, reason: percentage.reason };
  }
  if (size.state === "known") {
    return { state: "known", setting: "maxOldSpaceSize", source: size.source };
  }
  if (size.state === "invalid") {
    return { state: "invalid", setting: "maxOldSpaceSize", source: size.source, reason: size.reason };
  }
  return { state: "unset", setting: null, source: "absent" };
}

export function readOldSpace(sources: OldSpaceSources): Record<string, unknown> {
  const seen = new Map<SupportedOldSpaceFlag, SeenFlag>();
  if (sources.nodeOptions !== undefined && sources.nodeOptions.length > 0) {
    const parsed = parseNodeOptions(sources.nodeOptions);
    if (!parsed.ok) {
      const maxOldSpaceSize = unparsedFlag("--max-old-space-size", parsed.reason);
      const maxOldSpaceSizePercentage = unparsedFlag("--max-old-space-size-percentage", parsed.reason);
      return {
        maxOldSpaceSize,
        maxOldSpaceSizePercentage,
        effective: { state: "missing_signal", setting: null, source: "NODE_OPTIONS", reason: parsed.reason },
        precedence: OLD_SPACE_PRECEDENCE,
        distinctFrom: "v8HeapSizeLimit",
      };
    }
    absorbFlags(parsed.tokens, "NODE_OPTIONS", seen);
  }
  absorbFlags(sources.execArgv, "process.execArgv", seen);
  const maxOldSpaceSize = materialize("max-old-space-size", seen.get("max-old-space-size"));
  const maxOldSpaceSizePercentage = materialize("max-old-space-size-percentage", seen.get("max-old-space-size-percentage"));
  return {
    maxOldSpaceSize,
    maxOldSpaceSizePercentage,
    effective: selectEffective(maxOldSpaceSize, maxOldSpaceSizePercentage),
    precedence: OLD_SPACE_PRECEDENCE,
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
        oldSpace: readOldSpace({
          execArgv: process.execArgv,
          nodeOptions: process.env.NODE_OPTIONS,
        }),
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
