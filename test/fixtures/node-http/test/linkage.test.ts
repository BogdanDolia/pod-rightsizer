import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { createBuildManifest, loadBuildManifest, type BuildManifest } from "../src/build-manifest.js";
import { assessLinkage } from "../src/linkage.js";
import { fixtureRoot } from "./paths.js";

function loadDist(): { manifest: BuildManifest; files: Map<string, Buffer> } {
  const manifest = loadBuildManifest(readFileSync(join(fixtureRoot, "dist/build-manifest.json")));
  const files = new Map<string, Buffer>();
  for (const artifact of manifest.artifacts) {
    files.set(artifact.path, readFileSync(join(fixtureRoot, artifact.path)));
    files.set(artifact.sourceMap.path, readFileSync(join(fixtureRoot, artifact.sourceMap.path)));
  }
  return { manifest, files };
}

test("the same bytes and inputs produce the same build id", () => {
  const source = "export const value = 1;\n";
  const map = {
    version: 3,
    file: "placeholder.js",
    sources: ["../src/placeholder.ts"],
    sourcesContent: [source],
    mappings: "",
  };
  const files = new Map<string, Buffer>([
    ["dist/placeholder.js", Buffer.from("export const value = 1;\n")],
    ["dist/placeholder.js.map", Buffer.from(JSON.stringify(map))],
  ]);
  const first = createBuildManifest({ files, revision: "unknown", treeState: "unknown" });
  const second = createBuildManifest({ files, revision: "unknown", treeState: "unknown" });
  assert.equal(first.buildId, second.buildId);
  assert.equal(first.source.verifiedLinkage, false);
  const otherRevision = createBuildManifest({
    files,
    revision: "0123456789abcdef0123456789abcdef01234567",
    treeState: "unknown",
  });
  assert.equal(otherRevision.artifacts[0]?.sha256, first.artifacts[0]?.sha256);
  assert.notEqual(otherRevision.buildId, first.buildId);
  assert.equal(assessLinkage({ manifest: first, files }).matches, true);
});

test("compiled stack traces resolve to this build's TypeScript line", () => {
  const { manifest, files } = loadDist();
  assert.equal(assessLinkage({ manifest, files }).matches, true);
  const probe = manifest.artifacts.find((artifact) => artifact.path === "dist/stacktrace-probe.js");
  assert.ok(probe);
  const map = JSON.parse(files.get(probe.sourceMap.path)?.toString("utf8") ?? "") as { sourcesContent: string[] };
  const source = map.sourcesContent[0] ?? "";
  const line = source.split("\n").findIndex((entry) => entry.includes("controlled-stack-probe")) + 1;
  assert.ok(line > 0);
  const result = spawnSync(process.execPath, ["--enable-source-maps", "dist/stacktrace-probe.js"], {
    cwd: fixtureRoot,
    encoding: "utf8",
  });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, new RegExp(`stacktrace-probe\\.ts:${line}`));
});

test("a changed or foreign source map is not this build", () => {
  const { manifest, files } = loadDist();
  const tampered = new Map(files);
  const probe = manifest.artifacts.find((artifact) => artifact.path === "dist/stacktrace-probe.js");
  assert.ok(probe);
  const map = JSON.parse(tampered.get(probe.sourceMap.path)?.toString("utf8") ?? "") as {
    sourcesContent: string[];
  };
  map.sourcesContent = ["export const foreign = 1;\n"];
  tampered.set(probe.sourceMap.path, Buffer.from(JSON.stringify(map)));
  const verdict = assessLinkage({ manifest, files: tampered });
  assert.equal(verdict.matches, false);
  assert.ok(verdict.reasons.includes("content_hash_mismatch") || verdict.reasons.includes("source_map_unreadable") || verdict.reasons.includes("build_id_mismatch"));

  const js = Buffer.from(tampered.get(probe.path) ?? Buffer.alloc(0));
  js[0] = js[0] === 0x65 ? 0x66 : 0x65;
  const changedJs = new Map(files);
  changedJs.set(probe.path, js);
  const jsVerdict = assessLinkage({ manifest, files: changedJs });
  assert.equal(jsVerdict.matches, false);
  assert.ok(jsVerdict.reasons.includes("content_hash_mismatch") || jsVerdict.reasons.includes("build_id_mismatch"));
});

test("metadata does not treat a git sha as verified linkage", () => {
  const source = "export const value = 1;\n";
  const map = {
    version: 3,
    file: "placeholder.js",
    sources: ["../src/placeholder.ts"],
    sourcesContent: [source],
    mappings: "",
  };
  const files = new Map<string, Buffer>([
    ["dist/placeholder.js", Buffer.from(source)],
    ["dist/placeholder.js.map", Buffer.from(JSON.stringify(map))],
  ]);
  const manifest = createBuildManifest({
    files,
    revision: "0123456789abcdef0123456789abcdef01234567",
    treeState: "clean",
  });
  assert.equal(manifest.source.method, "explicit_build_input");
  assert.equal(manifest.source.verifiedLinkage, false);
  const directory = mkdtempSync(join(tmpdir(), "node-http-manifest-"));
  try {
    writeFileSync(join(directory, "manifest.json"), JSON.stringify(manifest));
    assert.equal(loadBuildManifest(readFileSync(join(directory, "manifest.json"))).buildId, manifest.buildId);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
