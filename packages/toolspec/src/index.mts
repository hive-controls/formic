import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020.js";
import addFormatsModule from "ajv-formats";
import type { FormatsPlugin } from "ajv-formats";

export type { ToolManifest } from "./types.generated.mts";

// ajv-formats is CommonJS: its default export is callable at runtime but types as
// the module namespace under NodeNext resolution.
const addFormats = addFormatsModule as unknown as FormatsPlugin;

const SCHEMA_PATH = join(
  dirname(fileURLToPath(import.meta.url)),
  "../schema/toolspec.schema.json",
);
const schema = JSON.parse(readFileSync(SCHEMA_PATH, "utf8"));

const ajv = new Ajv2020({ allErrors: true });
addFormats(ajv);
const validateManifest = ajv.compile(schema);

export interface ValidationResult {
  valid: boolean;
  errors: string[];
}

export function validate(manifest: unknown): ValidationResult {
  const valid = validateManifest(manifest);
  if (valid) {
    return { valid: true, errors: [] };
  }
  const errors = (validateManifest.errors ?? []).map((error) => {
    const offendingProperty: unknown = error.params?.additionalProperty;
    const suffix =
      typeof offendingProperty === "string" ? `: ${offendingProperty}` : "";
    return `${error.instancePath || "/"} ${error.message}${suffix}`;
  });
  return { valid: false, errors };
}
