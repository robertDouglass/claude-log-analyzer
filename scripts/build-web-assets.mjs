import { createHash } from "node:crypto";
import { cp, mkdir, readdir, readFile, writeFile } from "node:fs/promises";
import { basename, extname, join } from "node:path";

const passthroughAssets = [
  "app.js",
  "images",
  "report-actions.js",
  "site-header.js",
  "styles.css",
  "tooltips.js",
  "robots.txt",
  "sitemap.xml",
  "vendor",
];

await mkdir("web-dist", { recursive: true });

for (const asset of passthroughAssets) {
  await cp(join("web", asset), join("web-dist", asset), {
    recursive: true,
  });
}

await mkdir(join("web-dist", "proof"), { recursive: true });
await cp(join("web", "proof", "results.json"), join("web-dist", "proof", "results.json"));
await cp(join("web", "proof", "reports"), join("web-dist", "proof", "reports"), {
  recursive: true,
});

const fingerprintAssets = [
  "app.js",
  "report-actions.js",
  "site-header.js",
  "tooltips.js",
  "vendor/tippy/popper.min.js",
  "vendor/tippy/tippy-bundle.umd.min.js",
];

const replacements = new Map();
await mkdir(join("web-dist", "assets"), { recursive: true });

for (const asset of fingerprintAssets) {
  const data = await readFile(join("web", asset));
  const hash = createHash("sha256").update(data).digest("hex").slice(0, 8);
  const fileBase = basename(asset);
  const ext = extname(fileBase);
  const stem = fileBase.slice(0, -ext.length);
  const hashedPath = `/assets/${stem}-${hash}${ext}`;
  await writeFile(join("web-dist", hashedPath), data);
  replacements.set(`/${asset}`, hashedPath);
  replacements.set(`../${asset}`, hashedPath);
}

async function rewriteHTML(dir) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const fullPath = join(dir, entry.name);
    if (entry.isDirectory()) {
      await rewriteHTML(fullPath);
      continue;
    }
    if (!entry.isFile() || !entry.name.endsWith(".html")) {
      continue;
    }

    let html = await readFile(fullPath, "utf8");
    for (const [from, to] of replacements) {
      html = html.split(from).join(to);
    }
    html = html.replace(
      /<link rel="stylesheet" crossorigin href="([^"]*\/assets\/tippy-[^"]+\.css)">/g,
      `<link rel="preload" href="$1" as="style" onload="this.onload=null;this.rel='stylesheet'"><noscript><link rel="stylesheet" href="$1"></noscript>`,
    );
    await writeFile(fullPath, html);
  }
}

await rewriteHTML("web-dist");
