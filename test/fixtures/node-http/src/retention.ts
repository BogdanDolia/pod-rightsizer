export interface RetentionStats {
  count: number;
  accountedBytes: number;
  evictions: number;
  rejected: number;
}

export interface RetainResult {
  retained: boolean;
  evicted: number;
  reason: string | null;
}

interface Entry<T> {
  bytes: number;
  value: T;
}

export class BoundedRetainer<T> {
  private readonly entries: Entry<T>[] = [];
  private accountedBytes = 0;
  private evictionCount = 0;
  private rejectedCount = 0;

  constructor(readonly maxBytes: number) {
    if (!Number.isSafeInteger(maxBytes) || maxBytes < 1) {
      throw new Error("retention cap must be a positive integer");
    }
  }

  retain(value: T, bytes: number): RetainResult {
    if (!Number.isSafeInteger(bytes) || bytes < 1) {
      throw new Error("retained block size must be a positive integer");
    }
    if (bytes > this.maxBytes) {
      this.rejectedCount += 1;
      return { retained: false, evicted: 0, reason: "single_block_exceeds_cap" };
    }
    let evicted = 0;
    while (this.accountedBytes + bytes > this.maxBytes && this.entries.length > 0) {
      const removed = this.entries.shift();
      if (removed === undefined) {
        break;
      }
      this.accountedBytes -= removed.bytes;
      this.evictionCount += 1;
      evicted += 1;
    }
    this.entries.push({ bytes, value });
    this.accountedBytes += bytes;
    return { retained: true, evicted, reason: null };
  }

  stats(): RetentionStats {
    return {
      count: this.entries.length,
      accountedBytes: this.accountedBytes,
      evictions: this.evictionCount,
      rejected: this.rejectedCount,
    };
  }

  held(): readonly T[] {
    return this.entries.map((entry) => entry.value);
  }
}
