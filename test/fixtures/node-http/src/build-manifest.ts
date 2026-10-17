import { createHash } from "node:crypto";
import { APP_ROOT, LOGICAL_SERVICE } from "./limits.js";

export const BUILD_MANIFEST_FORMAT = "node-http-build-manifest/v1";

export interface SourceRecord {
  path: string;
  sha256: string;
}

export interface SourceMapRecord {
  path: string;
  sha256: string;
  bytes: number;
}

export interface ArtifactRecord {
  path: string;
  sha256: string;
  bytes: number;
  sourceMap: SourceMapRecord;
  sources: SourceRecord[];
}

export interface BuildSource {
  revision: string;
  treeState: "clean" | "dirty" | "unknown";
  method: "explicit_build_input";
  verifiedLinkage: false;
}

export interface BuildManifest {
  formatVersion: typeof BUILD_MANIFEST_FORMAT;
  buildId: string;
  buildIdAlgorithm: "sha256";
  logicalService: typeof LOGICAL_SERVICE;
  appRoot: typeof APP_ROOT;
  source: BuildSource;
  artifacts: ArtifactRecord[];
}

export class ManifestError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ManifestError";
  }
}

export function sha256(bytes: Buffer | string): string {
  return createHash("sha256").update(bytes).digest("hex");
}

export function canonicalJson(value: unknown): string {
  return `${JSON.stringify(sortValue(value))}\n`;
}

function sortValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((entry) => sortValue(entry));
  }
  if (value !== null && typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>).sort(([left], [right]) =>
      left.localeCompare(right),
    );
    return Object.fromEntries(entries.map(([key, entry]) => [key, sortValue(entry)]));
  }
  return value;
}

interface SourceMapFile {
  file?: unknown;
  sources?: unknown;
  sourcesContent?: unknown;
}

function fixtureSourcePath(source: string): string {
  const normalized = source.replaceAll("\\", "/").replace(/^(\.\.\/)+/, "").replace(/^\.\//, "");
  if (!normalized.startsWith("src/") || normalized.includes("..")) {
    throw new ManifestError(`source map source ${source} is outside src/`);
  }
  return normalized;
}

function readSourceMap(mapBytes: Buffer, jsPath: string): { fileName: string; sources: SourceRecord[] } {
  let parsed: SourceMapFile;
  try {
    parsed = JSON.parse(mapBytes.toString("utf8")) as SourceMapFile;
  } catch {
    throw new ManifestError(`source map for ${jsPath} is not JSON`);
  }
  if (typeof parsed.file !== "string" || parsed.file.length === 0) {
    throw new ManifestError(`source map for ${jsPath} has no file field`);
  }
  if (!Array.isArray(parsed.sources) || !Array.isArray(parsed.sourcesContent)) {
    throw new ManifestError(`source map for ${jsPath} is missing sourcesContent`);
  }
  if (parsed.sources.length === 0 || parsed.sources.length !== parsed.sourcesContent.length) {
    throw new ManifestError(`source map for ${jsPath} has no embedded sources`);
  }
  const sources: SourceRecord[] = [];
  for (let index = 0; index < parsed.sources.length; index += 1) {
    const source = parsed.sources[index];
    const content = parsed.sourcesContent[index];
    if (typeof source !== "string" || typeof content !== "string" || content.length === 0) {
      throw new ManifestError(`source map for ${jsPath} is missing sourcesContent`);
    }
    sources.push({ path: fixtureSourcePath(source), sha256: sha256(content) });
  }
  return { fileName: parsed.file, sources };
}

export function parseBuildInputs(env: NodeJS.ProcessEnv): { revision: string; treeState: BuildSource["treeState"] } {
  const revision = env.SOURCE_REVISION ?? "unknown";
  const treeState = env.SOURCE_TREE_STATE ?? "unknown";
  if (revision !== "unknown" && !/^[0-9a-f]{40}$/.test(revision)) {
    throw new ManifestError("SOURCE_REVISION must be unknown or 40 lowercase hex characters");
  }
  if (treeState !== "clean" && treeState !== "dirty" && treeState !== "unknown") {
    throw new ManifestError("SOURCE_TREE_STATE must be clean, dirty, or unknown");
  }
  return { revision, treeState };
}

export function createBuildManifest(input: {
  files: ReadonlyMap<string, Buffer>;
  revision: string;
  treeState: BuildSource["treeState"];
}): BuildManifest {
  const jsPaths = [...input.files.keys()].filter((path) => path.endsWith(".js")).sort();
  if (jsPaths.length === 0) {
    throw new ManifestError("build produced no JavaScript");
  }
  const artifacts: ArtifactRecord[] = [];
  for (const jsPath of jsPaths) {
    const jsBytes = input.files.get(jsPath);
    const mapPath = `${jsPath}.map`;
    const mapBytes = input.files.get(mapPath);
    if (jsBytes === undefined || mapBytes === undefined) {
      throw new ManifestError(`${jsPath} is missing its source map`);
    }
    const sourceMap = readSourceMap(mapBytes, jsPath);
    const jsName = jsPath.slice(jsPath.lastIndexOf("/") + 1);
    if (sourceMap.fileName !== jsName) {
      throw new ManifestError(`source map file ${sourceMap.fileName} does not match ${jsName}`);
    }
    artifacts.push({
      path: jsPath,
      sha256: sha256(jsBytes),
      bytes: jsBytes.length,
      sourceMap: { path: mapPath, sha256: sha256(mapBytes), bytes: mapBytes.length },
      sources: sourceMap.sources,
    });
  }
  const source: BuildSource = {
    revision: input.revision,
    treeState: input.treeState,
    method: "explicit_build_input",
    verifiedLinkage: false,
  };
  const manifest: BuildManifest = {
    formatVersion: BUILD_MANIFEST_FORMAT,
    buildIdAlgorithm: "sha256",
    logicalService: LOGICAL_SERVICE,
    appRoot: APP_ROOT,
    source,
    artifacts,
    buildId: "",
  };
  const { buildId: _buildId, ...body } = manifest;
  manifest.buildId = sha256(canonicalJson(body));
  return manifest;
}

export function loadBuildManifest(bytes: Buffer): BuildManifest {
  let parsed: unknown;
  try {
    parsed = JSON.parse(bytes.toString("utf8"));
  } catch {
    throw new ManifestError("build manifest is not JSON");
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new ManifestError("build manifest is not an object");
  }
  const record = parsed as Partial<BuildManifest>;
  if (record.formatVersion !== BUILD_MANIFEST_FORMAT) {
    throw new ManifestError("build manifest formatVersion is not supported");
  }
  if (typeof record.buildId !== "string" || !/^[a-f0-9]{64}$/.test(record.buildId)) {
    throw new ManifestError("build manifest buildId is not sha256");
  }
  const { buildId, ...body } = record as BuildManifest;
  const recomputed = sha256(canonicalJson(body));
  if (recomputed !== buildId) {
    throw new ManifestError("build manifest buildId does not match its contents");
  }
  if (body.source.verifiedLinkage !== false || body.source.method !== "explicit_build_input") {
    throw new ManifestError("build manifest cannot claim verified source linkage");
  }
  return record as BuildManifest;
}
