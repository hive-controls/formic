import { readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { generateTypes, GENERATED_PATH } from "./codegen.mts";

async function main(): Promise<void> {
  const fresh = await generateTypes();
  const tempPath = join(tmpdir(), `toolspec-types-check-${process.pid}.mts`);
  writeFileSync(tempPath, fresh);

  const committed = readFileSync(GENERATED_PATH, "utf8");
  if (fresh !== committed) {
    console.error(
      `Drift detected: ${GENERATED_PATH} does not match schema/toolspec.schema.json.\n` +
        `Regenerated output written to ${tempPath} for comparison.\n` +
        `Run "npm run codegen -w packages/toolspec" and commit the result.`,
    );
    process.exit(1);
  }
}

await main();
