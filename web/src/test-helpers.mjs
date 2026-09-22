// SPDX-License-Identifier: AGPL-3.0-only
import { buildSync } from "esbuild";
import { mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

export const testCacheDir = fileURLToPath(new URL("../node_modules/.cache/nex", import.meta.url));

export function bundleTestModule(source, name, css = false) {
  mkdirSync(testCacheDir, { recursive: true });
  const outfile = `${testCacheDir}/${name}.test-bundle.mjs`;
  buildSync({
    entryPoints: [fileURLToPath(source)],
    bundle: true, format: "esm", jsx: "automatic", target: "es2022", outfile,
    external: ["react", "react/jsx-runtime", "react-dom", "react-dom/server"],
    ...(css ? { loader: { ".css": "empty" } } : {}),
  });
  return `file://${outfile}`;
}
