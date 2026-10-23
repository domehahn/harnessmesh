import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

const root = new URL("..", import.meta.url).pathname;
const dist = join(root, "dist");
const indexPath = join(dist, "index.html");
if (!existsSync(indexPath)) throw new Error("dist/index.html is missing; run npm run build first");
const index = readFileSync(indexPath, "utf8");
const assets = [...index.matchAll(/(?:src|href)="\/?(assets\/[^\"]+)"/g)].map(match => match[1]);
if (assets.length < 2) throw new Error("production index does not reference the expected JS and CSS bundles");
for (const asset of assets) if (!existsSync(join(dist, asset))) throw new Error(`missing production asset: ${asset}`);
const cssFiles = readdirSync(join(dist, "assets")).filter(name => name.endsWith(".css"));
const css = cssFiles.map(name => readFileSync(join(dist, "assets", name), "utf8")).join("\n");
if (/fonts\.googleapis\.com|fonts\.gstatic\.com/.test(css)) throw new Error("production CSS contains an external font dependency");
console.log(`FRONTEND_SMOKE=PASS assets=${assets.length}`);
