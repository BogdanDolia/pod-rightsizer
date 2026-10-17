export const LOAD_PHASES_FORMAT = "node-http-load-phases/v1";

export type LoadPhaseName = "warmup" | "measured";

interface PhaseTotals {
  count: number;
  firstStartedOffsetMs: number | null;
  lastWorkFinishedOffsetMs: number | null;
  shortestWorkMs: number | null;
}

interface ActiveWork {
  phase: LoadPhaseName;
  startedOffsetMs: number;
}

function emptyPhase(): PhaseTotals {
  return {
    count: 0,
    firstStartedOffsetMs: null,
    lastWorkFinishedOffsetMs: null,
    shortestWorkMs: null,
  };
}

function stamp(offsetMs: number): string {
  return new Date(performance.timeOrigin + offsetMs).toISOString();
}

function view(phase: PhaseTotals): Record<string, unknown> {
  return {
    count: phase.count,
    firstStartedAt: phase.firstStartedOffsetMs === null ? null : stamp(phase.firstStartedOffsetMs),
    firstStartedOffsetMs: phase.firstStartedOffsetMs,
    lastWorkFinishedAt: phase.lastWorkFinishedOffsetMs === null ? null : stamp(phase.lastWorkFinishedOffsetMs),
    lastWorkFinishedOffsetMs: phase.lastWorkFinishedOffsetMs,
    shortestWorkMs: phase.shortestWorkMs,
  };
}

export function loadPhaseName(header: string | undefined): LoadPhaseName | null {
  return header === "warmup" || header === "measured" ? header : null;
}

export class LoadPhaseLedger {
  private inFlight = 0;
  private nextId = 1;
  private readonly active = new Map<number, ActiveWork>();
  private readonly phases: Record<LoadPhaseName, PhaseTotals> = {
    warmup: emptyPhase(),
    measured: emptyPhase(),
  };
  private measuredOpen = false;
  private measuredOpenedOffsetMs: number | null = null;
  private warmupInFlightAtFirstMeasuredStart: number | null = null;

  begin(phase: LoadPhaseName | null): number | null {
    if (phase === null) {
      return null;
    }
    const startedOffsetMs = performance.now();
    const warmupInFlight = this.count("warmup");
    if (phase === "measured" && this.phases.measured.firstStartedOffsetMs === null) {
      this.warmupInFlightAtFirstMeasuredStart = warmupInFlight;
    }
    const id = this.nextId;
    this.nextId += 1;
    this.inFlight += 1;
    this.active.set(id, { phase, startedOffsetMs });
    const bucket = this.phases[phase];
    bucket.count += 1;
    if (bucket.firstStartedOffsetMs === null) {
      bucket.firstStartedOffsetMs = startedOffsetMs;
    }
    return id;
  }

  finish(id: number | null): void {
    if (id === null) {
      return;
    }
    const work = this.active.get(id);
    if (work === undefined) {
      return;
    }
    this.active.delete(id);
    this.inFlight -= 1;
    const finishedOffsetMs = performance.now();
    const bucket = this.phases[work.phase];
    bucket.lastWorkFinishedOffsetMs = finishedOffsetMs;
    const duration = finishedOffsetMs - work.startedOffsetMs;
    bucket.shortestWorkMs = bucket.shortestWorkMs === null ? duration : Math.min(bucket.shortestWorkMs, duration);
  }

  count(phase: LoadPhaseName): number {
    let count = 0;
    for (const work of this.active.values()) {
      if (work.phase === phase) {
        count += 1;
      }
    }
    return count;
  }

  tryOpenMeasured(): boolean {
    if (this.count("warmup") > 0) {
      return false;
    }
    if (!this.measuredOpen) {
      this.measuredOpen = true;
      this.measuredOpenedOffsetMs = performance.now();
    }
    return true;
  }

  snapshot(): Record<string, unknown> {
    return {
      formatVersion: LOAD_PHASES_FORMAT,
      inFlight: this.inFlight,
      warmupInFlight: this.count("warmup"),
      measuredOpen: this.measuredOpen,
      measuredOpenedAt: this.measuredOpenedOffsetMs === null ? null : stamp(this.measuredOpenedOffsetMs),
      measuredOpenedOffsetMs: this.measuredOpenedOffsetMs,
      warmupInFlightAtFirstMeasuredStart: this.warmupInFlightAtFirstMeasuredStart,
      warmup: view(this.phases.warmup),
      measured: view(this.phases.measured),
    };
  }
}
