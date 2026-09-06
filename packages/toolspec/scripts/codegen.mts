import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { compile } from "json-schema-to-typescript";

const PACKAGE_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const SCHEMA_PATH = join(PACKAGE_ROOT, "schema/toolspec.schema.json");
export const GENERATED_PATH = join(PACKAGE_ROOT, "src/types.generated.mts");

export async function generateTypes(): Promise<string> {
  const schema = JSON.parse(readFileSync(SCHEMA_PATH, "utf8"));
  const body = await compile(schema, "ToolManifest", {
    bannerComment: "",
    additionalProperties: false,
  });
  return `// DO NOT EDIT — generated from schema/toolspec.schema.json by scripts/codegen.mts\n\n${body}`;
}

async function main(): Promise<void> {
  const output = await generateTypes();
  writeFileSync(GENERATED_PATH, output);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  await main();
}
