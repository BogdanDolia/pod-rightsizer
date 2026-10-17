import {
  BUFFER_TOUCH_STRIDE,
  CPU_SEED,
  HEAP_PAYLOAD_CELLS,
  MAX_CPU_ITERATIONS,
} from "./limits.js";

export interface CpuWork {
  iterations: number;
  digest: number;
}

export interface HeapObject {
  index: number;
  payload: number[];
}

export interface HeapWork {
  objects: HeapObject[];
  checksum: number;
}

export interface BufferWork {
  buffer: Buffer;
  checksum: number;
}

function assertCount(name: string, count: number, max: number): void {
  if (!Number.isSafeInteger(count) || count < 0 || count > max) {
    throw new Error(`${name} exceeds the per-request cap`);
  }
}

export function runCpuWork(iterations: number, seed: number = CPU_SEED): CpuWork {
  assertCount("cpu iterations", iterations, MAX_CPU_ITERATIONS);
  const table = new Uint32Array(1024);
  let x = seed >>> 0;
  for (let i = 0; i < iterations; i += 1) {
    const index = x & 1023;
    x = Math.imul(x ^ (table[index] ?? 0) ^ i, 0x9e3779b1) >>> 0;
    table[index] = x;
  }
  return { iterations, digest: x };
}

export function allocateHeapObjects(count: number): HeapWork {
  assertCount("heap objects", count, 1_000_000);
  const objects: HeapObject[] = [];
  let checksum = 0;
  for (let index = 0; index < count; index += 1) {
    const payload = new Array<number>(HEAP_PAYLOAD_CELLS);
    for (let cell = 0; cell < HEAP_PAYLOAD_CELLS; cell += 1) {
      payload[cell] = (index + cell) & 0xff;
    }
    const object = { index, payload };
    checksum = (checksum + object.index + (object.payload[0] ?? 0)) >>> 0;
    objects.push(object);
  }
  return { objects, checksum };
}

export function allocateUsedBuffer(bytes: number): BufferWork {
  assertCount("buffer bytes", bytes, 8 * 1024 * 1024);
  const buffer = Buffer.allocUnsafe(bytes);
  let checksum = 0;
  for (let offset = 0; offset < bytes; offset += BUFFER_TOUCH_STRIDE) {
    const value = offset & 0xff;
    buffer[offset] = value;
    checksum = (checksum + buffer[offset]) >>> 0;
  }
  if (bytes > 0) {
    buffer[bytes - 1] = 0x5a;
    checksum = (checksum + buffer[bytes - 1]) >>> 0;
  }
  return { buffer, checksum };
}

export function delay(ms: number): Promise<void> {
  assertCount("delay ms", ms, 1_000);
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}
