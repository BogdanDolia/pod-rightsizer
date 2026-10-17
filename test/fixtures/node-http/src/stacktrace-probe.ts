import { pathToFileURL } from "node:url";
import { realpathSync } from "node:fs";

export function throwControlled(): never {
  throw new Error("controlled-stack-probe");
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
  throwControlled();
}
