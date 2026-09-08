// SPDX-License-Identifier: AGPL-3.0-only
import { build, context } from "esbuild";
import { cp, mkdir } from "node:fs/promises";
await mkdir("dist", { recursive: true });
await cp("public", "dist", { recursive: true });
const options = {
  entryPoints: { app: "src/main.tsx", local: "src/local.tsx" },
  bundle: true,
  minify: true,
  outdir: "dist",
  entryNames: "[name]",
  target: "es2022",
  legalComments: "external",
  external: ["/nex-brand.png"],
};
if (process.argv.includes("--watch")) {
  await (await context(options)).watch();
} else {
  await build(options);
}
