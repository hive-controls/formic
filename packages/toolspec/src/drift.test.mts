import { readFileSync } from "node:fs";
import { test } from "node:test";
import assert from "node:assert/strict";
import { generateTypes, GENERATED_PATH } from "../scripts/codegen.mts";

test("committed src/types.generated.mts matches the schema", async () => {
  const fresh = await generateTypes();
  const committed = readFileSync(GENERATED_PATH, "utf8");
  assert.equal(
    committed,
    fresh,
    "src/types.generated.mts is out of date — run `npm run codegen -w packages/toolspec` and commit the result",
  );
});
