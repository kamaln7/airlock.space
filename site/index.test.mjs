import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

test("mobile layout keeps the SSH command on one line", async () => {
  const html = await readFile(new URL("./index.html", import.meta.url), "utf8");

  assert.match(
    html,
    /@media\s*\(max-width:\s*30rem\)\s*\{[\s\S]*?body\s*\{[\s\S]*?padding:\s*2rem\s+1rem;[\s\S]*?\}[\s\S]*?\.box\s*\{[\s\S]*?padding:\s*1\.25rem\s+1\.5rem;[\s\S]*?font-size:\s*1rem;[\s\S]*?white-space:\s*nowrap;/,
  );
});
