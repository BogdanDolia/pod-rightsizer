import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";
import { createBuildManifest, parseBuildInputs } from "./build-manifest.js";

function listFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...listFiles(full));
    } else if (entry.isFile()) {
      out.push(full);
    }
  }
  return out;
}

const root = process.cwd();
const dist = join(root, "dist");
const files = new Map<string, Buffer>();
for (const full of listFiles(dist)) {
  const rel = relative(root, full).split("\\").join("/");
  if (rel === "dist/build-manifest.json") {
    continue;
  }
  if (rel.endsWith(".js") || rel.endsWith(".js.map")) {
    files.set(rel, readFileSync(full));
  }
}

const inputs = parseBuildInputs(process.env);
const manifest = createBuildManifest({
  files,
  revision: inputs.revision,
  treeState: inputs.treeState,
});
writeFileSync(join(dist, "build-manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
