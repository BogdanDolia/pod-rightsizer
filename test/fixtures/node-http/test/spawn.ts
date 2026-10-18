import { spawn, type ChildProcess } from "node:child_process";
import { once } from "node:events";
import { fixtureRoot } from "./paths.js";

export interface SpawnedServer {
  baseUrl: string;
  child: ChildProcess;
  stderr: () => string;
  stop: () => Promise<number | null>;
}

export function spawnReference(env: Record<string, string | undefined>, args: string[] = []): SpawnedServer {
  let stdout = "";
  let stderr = "";
  const childEnv: NodeJS.ProcessEnv = { ...process.env };
  for (const [key, value] of Object.entries(env)) {
    if (value === undefined) {
      delete childEnv[key];
    } else {
      childEnv[key] = value;
    }
  }
  if (!Object.prototype.hasOwnProperty.call(env, "NODE_OPTIONS")) {
    delete childEnv.NODE_OPTIONS;
  }
  const child = spawn(process.execPath, [...args, "--enable-source-maps", "dist/server.js"], {
    cwd: fixtureRoot,
    env: childEnv,
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk: string) => {
    stdout += chunk;
  });
  child.stderr.on("data", (chunk: string) => {
    stderr += chunk;
  });
  const ready = new Promise<number>((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(new Error(`server did not listen\n${stdout}\n${stderr}`));
    }, 5_000);
    const onData = (): void => {
      const match = stdout.match(/listening port=(\d+)/);
      if (match?.[1] !== undefined) {
        clearTimeout(timer);
        resolve(Number(match[1]));
      }
    };
    child.stdout.on("data", onData);
    child.once("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`server exited ${code} before listening\n${stdout}\n${stderr}`));
    });
  });
  let baseUrl = "";
  const opened = ready.then((port) => {
    baseUrl = `http://127.0.0.1:${port}`;
    return port;
  });
  return {
    get baseUrl() {
      return baseUrl;
    },
    child,
    stderr: () => stderr,
    stop: async () => {
      if (child.exitCode !== null || child.signalCode !== null) {
        return child.exitCode;
      }
      child.kill("SIGTERM");
      const [code] = (await once(child, "exit")) as [number | null];
      return code;
    },
    // The caller must await this before using baseUrl.
    ready: opened,
  } as SpawnedServer & { ready: Promise<number> };
}

export async function startReference(env: Record<string, string | undefined>, args: string[] = []): Promise<SpawnedServer> {
  const started = spawnReference(env, args) as SpawnedServer & { ready: Promise<number> };
  await started.ready;
  return started;
}
