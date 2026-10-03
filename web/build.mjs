// SPDX-License-Identifier: AGPL-3.0-only
import { build, context } from "esbuild";
import { cp, mkdir } from "node:fs/promises";
await mkdir("dist", { recursive: true });
await cp("public", "dist", { recursive: true });
const options = {
  entryPoints: process.argv.includes("--demo")
    ? { app: "src/demo.ts" }
    : { app: "src/main.tsx", local: "src/local.tsx" },
  define: { __NEX_DEMO__: String(process.argv.includes("--demo")) },
  bundle: true,
  splitting: true,
  minify: true,
  outdir: "dist",
  chunkNames: "chunks/[name]-[hash]",
  entryNames: "[name]",
  target: "es2022",
  format: "esm",
  legalComments: "external",
  external: ["/nex-*.svg"],
};
if (process.argv.includes("--watch")) {
  await (await context(options)).watch();
} else {
  await build(options);
}
