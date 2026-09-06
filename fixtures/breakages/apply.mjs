/**
 * Apply one named breakage to a copy of the sample app.
 *
 * The pristine app is never mutated in place: each breakage is materialised into
 * its own served directory so a run is reproducible and a revert is an rm.
 *
 *   node breakages/apply.mjs renamed-selector .breakage-build/renamed-selector
 */
import {
  cpSync,
  readFileSync,
  writeFileSync,
  rmSync,
  mkdirSync,
} from "node:fs";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const SOURCE = join(HERE, "..", "sample-app");

/** Each breakage is a class of real-world UI drift, applied as an exact substitution. */
const BREAKAGES = {
  /** Class 1: an id is renamed. The locator no longer resolves; the element is
   *  still present and still the right target. The easy case. */
  "renamed-selector": [
    { file: "index.html", from: 'id="signin-button"', to: 'id="submit-login"' },
  ],

  /** Class 2: the DOM is restructured. `[data-order-id]` still MATCHES — but the
   *  identifier moved off the <button> that opens the order onto a plain reference
   *  label beside it, so the locator resolves to an element no handler sits under.
   *  The locator resolves, the click lands, and nothing happens. This is the
   *  wrong-element trap: a naive heal can "fix" this by waiting longer.
   *
   *  It must not hoist the identifier onto the <li> WRAPPING the row: the row button
   *  fills its wrapper, so the click still reached the handler and the whole replay
   *  stayed green — a breakage no replay can see exercises no healer. */
  "moved-element": [
    {
      file: "app.js",
      from: "    row.dataset.orderId = order.id;",
      to: "    // drift: identifier moved off the control during a refactor",
    },
    {
      file: "app.js",
      from: '    const li = document.createElement("li");\n    li.append(row);',
      to: `    const li = document.createElement("li");
    const reference = document.createElement("span");
    reference.className = "order-ref";
    reference.dataset.orderId = order.id;
    reference.textContent = order.id;
    li.append(reference, row);`,
    },
  ],

  /** Class 4: DATA drift. Every locator still resolves and every click lands —
   *  but SO-4472 now belongs to a different customer. Nothing is "broken" in the
   *  DOM sense, so a locator patcher sees nothing to fix. Only an assertion on
   *  business content catches it. This is the case that proves healing must
   *  pair with assertions. */
  "swapped-data": [
    {
      file: "app.js",
      from: '  { id: "SO-4472", customer: "Contoso Rail", total: "$3,905.50", items: 6 },',
      to: '  { id: "SO-4472", customer: "Fabrikam Metals", total: "$27,310.25", items: 31 },',
    },
  ],

  /** Class 3: a step is inserted into the flow. Approval now requires confirming
   *  an interstitial first. No locator is wrong — the test's MODEL of the flow is
   *  wrong. This is the case a locator patcher structurally cannot repair. */
  "changed-flow": [
    {
      file: "app.js",
      from: 'document.getElementById("approve-button").addEventListener("click", () => {',
      to: `document.getElementById("approve-button").addEventListener("click", () => {
  const banner = document.getElementById("confirm-banner");
  if (banner.hidden) {
    banner.hidden = false;
    return;
  }
  banner.hidden = true;`,
    },
    {
      file: "index.html",
      from: '      <button id="approve-button" type="button">Approve order</button>',
      to: `      <p id="confirm-banner" class="error" hidden>This order exceeds $2,500 — click Approve again to confirm.</p>
      <button id="approve-button" type="button">Approve order</button>`,
    },
  ],
};

const [name, outDir] = process.argv.slice(2);
const edits = BREAKAGES[name];
if (!edits) {
  console.error(
    `unknown breakage "${name}". known: ${Object.keys(BREAKAGES).join(", ")}`,
  );
  process.exit(1);
}

// Never in place: the pristine app is the source of every variant. Refuse before
// touching anything when the destination IS the source, or sits inside it, or contains it.
const source = resolve(SOURCE);
const destination = resolve(outDir);
/** `inner` is `outer` itself or lives under it — by path segments, so a filesystem
 *  root (where `root + sep` is not a prefix of anything) is handled like any parent. */
const contains = (outer, inner) => {
  const between = relative(outer, inner);
  return between === "" || (!between.startsWith("..") && !isAbsolute(between));
};
if (contains(source, destination) || contains(destination, source)) {
  console.error(
    `refusing: destination "${outDir}" is the sample app itself (or overlaps it) — pass a fresh directory, e.g. .breakage-build/${name}`,
  );
  process.exit(1);
}

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });
cpSync(SOURCE, outDir, { recursive: true });

for (const edit of edits) {
  const target = join(outDir, edit.file);
  const before = readFileSync(target, "utf8");
  if (!before.includes(edit.from)) {
    console.error(
      `FAILED: anchor not found in ${edit.file}: ${JSON.stringify(edit.from.slice(0, 60))}`,
    );
    process.exit(1);
  }
  writeFileSync(target, before.replace(edit.from, edit.to));
}
console.log(
  `applied "${name}" -> ${outDir} (${edits.length} edit${edits.length > 1 ? "s" : ""})`,
);
