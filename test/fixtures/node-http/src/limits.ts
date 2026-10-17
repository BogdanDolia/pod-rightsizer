// Bounds for one reference process. These are fixture controls, not production sizing advice.

export const MAX_INTENSITY = 8;
export const MAX_IN_FLIGHT = 8;

export const CPU_ITERATIONS_PER_INTENSITY = 8_000_000;
export const CPU_SEED = 0x12345678;
export const MAX_CPU_ITERATIONS = MAX_INTENSITY * CPU_ITERATIONS_PER_INTENSITY;

export const HEAP_OBJECTS_PER_INTENSITY = 1_000;
export const HEAP_PAYLOAD_CELLS = 16;
export const HEAP_ACCOUNTING_BYTES_PER_OBJECT = 256;
export const HEAP_RETENTION_MAX_BYTES = 2 * 1024 * 1024;

export const BUFFER_BYTES_PER_INTENSITY = 128 * 1024;
export const BUFFER_TOUCH_STRIDE = 4096;
export const BUFFER_RETENTION_MAX_BYTES = 2 * 1024 * 1024;

export const DELAY_MS_PER_INTENSITY = 40;

export const MAX_QUOTE_ITEMS = 8;
export const MAX_QUOTE_QUANTITY = 20;
export const JSON_BODY_LIMIT = "8kb";

export const DEFAULT_PORT = 8080;
export const DEFAULT_SHUTDOWN_GRACE_MS = 5_000;
export const MIN_SHUTDOWN_GRACE_MS = 100;
export const MAX_SHUTDOWN_GRACE_MS = 10_000;

export const ELU_INTERVAL_MS = 1_000;
export const EVENT_LOOP_DELAY_RESOLUTION_MS = 10;

export const LOGICAL_SERVICE = "node-http-reference";
export const APP_ROOT = "test/fixtures/node-http";
