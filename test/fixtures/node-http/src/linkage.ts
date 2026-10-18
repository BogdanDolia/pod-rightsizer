import { canonicalJson, createBuildManifest, sha256, type BuildManifest } from "./build-manifest.js";

export interface LinkageVerdict {
  matches: boolean;
  reasons: string[];
}

export function assessLinkage(input: {
  manifest: BuildManifest;
  files: ReadonlyMap<string, Buffer>;
}): LinkageVerdict {
  const reasons: string[] = [];
  const { buildId, ...body } = input.manifest;
  if (sha256(canonicalJson(body)) !== buildId) {
    reasons.push("build_id_mismatch");
  }
  let recomputed: BuildManifest;
  try {
    recomputed = createBuildManifest({
      files: input.files,
      revision: input.manifest.source.revision,
      treeState: input.manifest.source.treeState,
    });
  } catch {
    reasons.push("source_map_unreadable");
    return { matches: false, reasons: [...new Set(reasons)] };
  }
  if (recomputed.buildId !== input.manifest.buildId) {
    reasons.push("build_id_mismatch");
  }
  const expected = new Map(input.manifest.artifacts.map((artifact) => [artifact.path, artifact]));
  if (recomputed.artifacts.length !== expected.size) {
    reasons.push("content_hash_mismatch");
  }
  for (const artifact of recomputed.artifacts) {
    const declared = expected.get(artifact.path);
    if (declared === undefined) {
      reasons.push("missing_file");
      continue;
    }
    if (artifact.sha256 !== declared.sha256 || artifact.sourceMap.sha256 !== declared.sourceMap.sha256) {
      reasons.push("content_hash_mismatch");
    }
    if (artifact.sources.length !== declared.sources.length) {
      reasons.push("source_content_hash_mismatch");
      continue;
    }
    for (let index = 0; index < artifact.sources.length; index += 1) {
      const actual = artifact.sources[index];
      const wanted = declared.sources[index];
      if (actual === undefined || wanted === undefined || actual.sha256 !== wanted.sha256 || actual.path !== wanted.path) {
        reasons.push("source_content_hash_mismatch");
      }
    }
  }
  const unique = [...new Set(reasons)];
  return { matches: unique.length === 0, reasons: unique };
}
