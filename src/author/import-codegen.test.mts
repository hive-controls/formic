/**
 * The golden fixture below is a hand-verified example of what
 * `npx playwright codegen --target playwright-test` emits (checked against `--help`'s
 * default target and Playwright's documented output shape) rather than a captured sample.
 *
 * The through-line of these tests is that the importer never PARTIALLY understands a line:
 * every expression it cannot read in full is reported with its source line, and a spec is
 * either complete enough for `loadSpec` or reported as incomplete — never written half-way.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { importCodegen } from "./import-codegen.mts";
import { loadSpec, saveSpec } from "../spec/parse.mts";

const GOLDEN = `import { test, expect } from '@playwright/test';

test('approve an order', async ({ page }) => {
  await page.goto('http://127.0.0.1:4173/');
  await page.locator('#email').fill('ops@forgedepot.test');
  await expect(page.locator('#email')).toBeVisible();
  await page.getByTestId('signin-button').click();
  await expect(page.getByTestId('current-user')).toContainText('ops@forgedepot.test');
  await page.locator('#approve-button').click();
  await expect(page.getByRole('button', { name: 'Approve', exact: true })).toBeVisible();
});
`;

test("golden — a full codegen script imports to a spec that round-trips through spec/parse.mts", () => {
  const { spec, unsupported } = importCodegen(GOLDEN);
  assert.deepEqual(unsupported, []);
  assert.equal(spec.name, "approve an order");
  assert.equal(spec.startUrl, "http://127.0.0.1:4173/");
  assert.equal(spec.steps.length, 4);

  assert.equal(spec.steps[0].action, "goto");
  assert.equal(spec.steps[0].target, "http://127.0.0.1:4173/");

  assert.equal(spec.steps[1].action, "fill");
  assert.equal(spec.steps[1].target, "#email");
  assert.equal(spec.steps[1].value, "ops@forgedepot.test");
  assert.deepEqual(spec.steps[1].assert, { selector: "#email", visible: true });

  assert.equal(spec.steps[2].action, "click");
  assert.equal(spec.steps[2].target, '[data-testid="signin-button"]');
  assert.deepEqual(spec.steps[2].assert, {
    testId: "current-user",
    containsText: "ops@forgedepot.test",
  });

  assert.equal(spec.steps[3].action, "click");
  assert.equal(spec.steps[3].target, "#approve-button");
  // Structured fields, not a `role=button[name="Approve"]` selector string: the replay
  // runner builds the locator from these, so nothing has to survive interpolation.
  assert.deepEqual(spec.steps[3].assert, {
    role: "button",
    name: "Approve",
    exact: true,
    visible: true,
  });

  const reloaded = loadSpec(saveSpec(spec));
  assert.deepEqual(reloaded, spec);
});

test("unsupported constructs are reported with their line, never silently dropped", () => {
  const source = `import { test, expect } from '@playwright/test';

test('unsupported bits', async ({ page }) => {
  await page.goto('http://127.0.0.1:4173/');
  await page.getByLabel('Email').fill('a@b.test');
  await page.mouse.move(10, 10);
  await page.waitForTimeout(500);
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.equal(spec.steps.length, 1, "only the goto is a recognized step");
  assert.equal(unsupported.length, 3);
  assert.match(unsupported[0], /^line 5: unsupported action locator/);
  assert.match(unsupported[1], /^line 6: unsupported construct/);
  assert.match(unsupported[2], /^line 7: unsupported construct/);
});

test("an expect() with nothing preceding it is reported, not silently attached", () => {
  const source = `test('t', async ({ page }) => {
  await expect(page.getByTestId('x')).toBeVisible();
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.equal(spec.steps.length, 0);
  assert.match(unsupported[0], /expect\(\) with no preceding action/);
});

test("a plain (non-chained) action form is also supported", () => {
  const source = `test('plain', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#a');
  await expect(page.locator('#a')).toBeVisible();
  await page.fill('#b', 'v');
  await expect(page.locator('#b')).toBeVisible();
  await page.press('#c', 'Enter');
  await expect(page.locator('#c')).toBeVisible();
  await page.selectOption('#d', 'us');
  await expect(page.locator('#d')).toBeVisible();
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.deepEqual(unsupported, []);
  assert.equal(spec.steps.length, 5);
  assert.deepEqual(
    spec.steps.slice(1).map((s) => [s.action, s.target, s.value]),
    [
      ["click", "#a", undefined],
      ["fill", "#b", "v"],
      ["press", "#c", "Enter"],
      ["select", "#d", "us"],
    ],
  );
  assert.deepEqual(loadSpec(saveSpec(spec)), spec);
});

test("a state-changing step codegen left un-asserted is reported at its own line", () => {
  // The bug this pins: the importer used to emit this spec happily, and `loadSpec` then
  // refused the YAML the CLI had already written. Report it here, where the line number
  // still exists.
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.equal(unsupported.length, 1);
  assert.match(
    unsupported[0],
    /^line 3: "click" changes state and needs an assertion/,
  );
  assert.throws(() => loadSpec(saveSpec(spec)), /state-change rule/);
});

test("an argument that is not a plain string literal is reported, never partly interpreted", () => {
  // `process.env.URL ?? '...'` used to import as the FALLBACK url — a spec silently
  // pointing somewhere the script never necessarily goes.
  const dynamic = `test('t', async ({ page }) => {
  await page.goto(process.env.URL ?? 'http://fallback/');
});
`;
  const fromDynamic = importCodegen(dynamic);
  assert.equal(fromDynamic.spec.steps.length, 0);
  assert.match(
    fromDynamic.unsupported[0],
    /^line 2: goto\(\) argument is not a plain string literal/,
  );
  assert.equal(fromDynamic.spec.startUrl, "");

  // A regex matcher used to degrade to a bare locator assertion with no text predicate at
  // all — the step read as covered while asserting only that the element existed.
  const regex = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByTestId('status')).toHaveText(/approved/);
});
`;
  const fromRegex = importCodegen(regex);
  assert.match(
    fromRegex.unsupported[0],
    /^line 4: toHaveText\(\) argument is not a plain string literal/,
  );
  assert.equal(fromRegex.spec.steps[1].assert, undefined);

  // A template literal is a dynamic value too, and a `fill` cannot silently lose it.
  const template = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.fill('#b', \`hello \${name}\`);
});
`;
  assert.match(
    importCodegen(template).unsupported[0],
    /^line 3: fill\(\) value is not a plain string literal/,
  );
});

test("a comma inside a string literal does not split the argument list", () => {
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.fill('#name', 'Smith, John');
  await expect(page.locator('#name')).toBeVisible();
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.deepEqual(unsupported, []);
  assert.equal(spec.steps[1].value, "Smith, John");
});

test("a second expect() on one step is reported — the grammar carries one assertion", () => {
  // Overwriting silently discarded the FIRST assertion, so a script asserting two things
  // imported as a spec asserting one, with no report that anything was lost.
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByTestId('s')).toBeVisible();
  await expect(page.getByTestId('s')).toHaveText('ok');
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.equal(unsupported.length, 1);
  assert.match(
    unsupported[0],
    /^line 5: a second expect\(\) on the step from line 3/,
  );
  assert.deepEqual(spec.steps[1].assert, { testId: "s", visible: true });
});

test("an unsupported line breaks attachment — the following expect is orphaned, not misattached", () => {
  // Without explicit attachment tracking the expect hopped over the unreadable line and
  // landed on the click, asserting something the script never asserted there.
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByTestId('s')).toBeVisible();
  await page.waitForTimeout(50);
  await expect(page.getByTestId('later')).toBeVisible();
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.deepEqual(spec.steps[1].assert, { testId: "s", visible: true });
  assert.match(unsupported[0], /^line 5: unsupported construct/);
  assert.match(unsupported[1], /^line 6: expect\(\) with no preceding action/);
});

test("importing the same script twice produces the same bytes", () => {
  process.env.SOURCE_DATE_EPOCH = "1700000000";
  try {
    assert.equal(
      saveSpec(importCodegen(GOLDEN).spec),
      saveSpec(importCodegen(GOLDEN).spec),
    );
    assert.deepEqual(
      importCodegen(GOLDEN).spec.steps.map((step) => step.id),
      ["s1", "s2", "s3", "s4"],
    );
  } finally {
    delete process.env.SOURCE_DATE_EPOCH;
  }
});

test("an options object is reported for the call it sits on, never dropped", () => {
  // Dropping `{ button: 'right' }` imported a LEFT click, and dropping a matcher's
  // `{ timeout }` asserted on a different schedule than the script did. Both read green.
  const clickOptions = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go', { button: 'right' });
});
`;
  const fromClick = importCodegen(clickOptions);
  assert.equal(fromClick.spec.steps.length, 1, "the click is not imported");
  assert.match(
    fromClick.unsupported[0],
    /^line 3: click\(\) takes 0 argument\(s\)/,
  );

  const matcherOptions = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByTestId('s')).toBeVisible({ timeout: 1 });
});
`;
  assert.match(
    importCodegen(matcherOptions).unsupported[0],
    /^line 4: toBeVisible\(\) takes no argument the spec grammar can carry/,
  );

  const chainedOptions = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.locator('#go').click({ button: 'right' });
});
`;
  assert.match(
    importCodegen(chainedOptions).unsupported[0],
    /^line 3: click\(\) takes 0 argument\(s\)/,
  );
});

test("exact is the one locator option the grammar can carry", () => {
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByText('Done', { exact: true })).toBeVisible();
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.deepEqual(unsupported, []);
  assert.deepEqual(spec.steps[1].assert, {
    text: "Done",
    exact: true,
    visible: true,
  });

  const unknownOption = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByText('Done', { ignoreCase: true })).toBeVisible();
});
`;
  assert.match(
    importCodegen(unknownOption).unsupported[0],
    /option ignoreCase has no equivalent in the spec grammar/,
  );
});

test("an unreadable matcher argument clears attachment too", () => {
  // The narrower earlier fix cleared attachment on an unsupported locator and an
  // unsupported construct, but not on this return path — so the line-5 expect attached
  // backwards onto the line-3 click and asserted something the script never asserted there.
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByTestId('s')).toHaveText(/bad/);
  await expect(page.getByTestId('later')).toBeVisible();
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.equal(spec.steps[1].assert, undefined, "nothing attached backwards");
  assert.match(
    unsupported[0],
    /^line 4: toHaveText\(\) argument is not a plain/,
  );
  assert.match(unsupported[1], /^line 5: expect\(\) with no preceding action/);
});

test("a locator value is never interpolated into a selector string", () => {
  // `role=button[name="Say "yes""]` is a malformed selector that matched nothing. The
  // structured fields carry the same name losslessly, quote and all.
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByRole("button", { name: 'Say "yes"' })).toBeVisible();
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.deepEqual(unsupported, []);
  assert.deepEqual(spec.steps[1].assert, {
    role: "button",
    name: 'Say "yes"',
    visible: true,
  });
  assert.deepEqual(loadSpec(saveSpec(spec)), spec);
});

test("a role or text locator cannot address an action, and says so by name", () => {
  // A step target is a plain selector with no structured form, so these two locator kinds
  // describe an assertion but cannot be clicked without inventing a selector spelling.
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.getByRole('button', { name: 'Approve' }).click();
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.equal(spec.steps.length, 1);
  assert.match(
    unsupported[0],
    /^line 3: getByRole\(\) describes an assertion but cannot address an action/,
  );

  const text = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.getByText('Approve').click();
});
`;
  assert.match(
    importCodegen(text).unsupported[0],
    /^line 3: getByText\(\) describes an assertion but cannot address an action/,
  );
});

test("a test id carrying a quote is escaped into the selector; a control character refuses only the action target", () => {
  const quoted = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.getByTestId('a"b').click();
  await expect(page.getByTestId('a"b')).toBeVisible();
});
`;
  const { spec, unsupported } = importCodegen(quoted);
  assert.deepEqual(unsupported, []);
  assert.equal(spec.steps[1].target, '[data-testid="a\\"b"]');
  assert.deepEqual(spec.steps[1].assert, { testId: 'a"b', visible: true });

  const controlChar = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.getByTestId('a\\nb').click();
});
`;
  const control = importCodegen(controlChar);
  assert.equal(control.spec.steps.length, 1, "only the goto is imported");
  assert.match(
    control.unsupported[0],
    /^line 3: unsupported action locator — the test id contains the control character U\+000A, which a CSS attribute selector cannot carry/,
  );
});

test("an assertion-only test id needs no CSS spelling — a control character does not refuse it", () => {
  // resolveLocator used to build the `[data-testid="…"]` target eagerly for every resolve,
  // so an assertion that never touches a selector was refused over a constraint that
  // belongs to the action path alone. `{ testId: "a\nb" }` is losslessly representable.
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByTestId('a\\nb')).toBeVisible();
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.deepEqual(unsupported, []);
  assert.deepEqual(spec.steps[1].assert, { testId: "a\nb", visible: true });
});

test("a third expect() is reported as orphaned, not mislabeled as a second expect()", () => {
  // The second expect()'s refusal used to push to `unsupported` directly, bypassing
  // report() and its attachment-clearing side effect — so a THIRD expect on the same step
  // found attachment still set and reported as another "second expect()" instead of the
  // orphan it actually is.
  const source = `test('t', async ({ page }) => {
  await page.goto('http://x/');
  await page.click('#go');
  await expect(page.getByTestId('s')).toBeVisible();
  await expect(page.getByTestId('s')).toHaveText('ok');
  await expect(page.getByTestId('s')).toHaveText('later');
});
`;
  const { spec, unsupported } = importCodegen(source);
  assert.equal(unsupported.length, 2);
  assert.match(
    unsupported[0],
    /^line 5: a second expect\(\) on the step from line 3/,
  );
  assert.match(
    unsupported[1],
    /^line 6: expect\(\) with no preceding action to attach to/,
  );
  assert.deepEqual(spec.steps[1].assert, { testId: "s", visible: true });
});

test("REFUSAL — an imported fill whose value is a redaction placeholder", () => {
  // Codegen recorded against a page whose spec had already withheld this value, so what
  // it captured is the placeholder. Importing it produces a spec that replays by typing
  // "<secret:password>" into the login form and reports the failure as a broken locator.
  const { unsupported } = importCodegen(
    [
      'import { test, expect } from "@playwright/test";',
      "test('sign in', async ({ page }) => {",
      "  await page.fill('#password', '<secret:password>');",
      "});",
    ].join("\n"),
  );
  assert.equal(unsupported.length, 1, JSON.stringify(unsupported));
  assert.match(unsupported[0], /is a redaction placeholder, not a value/);
  assert.match(unsupported[0], /valueFrom: env\.<NAME>/);
});

test("a frameLocator chain imports as the step's frame, outermost first", () => {
  const { spec, unsupported } = importCodegen(
    [
      'import { test, expect } from "@playwright/test";',
      "",
      "test('checkout', async ({ page }) => {",
      "  await page.goto('http://127.0.0.1:4173/');",
      "  await page.frameLocator('#checkout').frameLocator('#payment').getByTestId('card').fill('4242');",
      "  await expect(page.frameLocator('#checkout').frameLocator('#payment').getByTestId('card')).toBeVisible();",
      "  await page.frameLocator('#checkout').locator('#pay').click();",
      "  await expect(page.frameLocator('#checkout').getByTestId('receipt')).toHaveText('Paid');",
      "});",
    ].join("\n"),
  );
  assert.deepEqual(unsupported, []);
  assert.deepEqual(spec.steps[1].frame, [
    { selector: "#checkout" },
    { selector: "#payment" },
  ]);
  assert.equal(spec.steps[1].target, '[data-testid="card"]');
  // Same chain on the expect as on the action: the assertion inherits it and says
  // nothing of its own.
  assert.equal(spec.steps[1].assert?.frame, undefined);
  assert.deepEqual(spec.steps[2].frame, [{ selector: "#checkout" }]);
  assert.equal(spec.steps[2].assert?.hasText, "Paid");
});

test("an expect on the HOST PAGE after a framed action is reported, never inherited", () => {
  // Frame scope is per expression: `expect(page.getByTestId(...))` is the top-level page
  // whatever the line before it did. Reading it as the step's frame changed what the
  // imported test checks, into a document the assertion was never written against — and
  // the grammar has no way to say "the page" on a framed step, because an assert with no
  // frame is checked in its step's. So it is reported, at the line the reader must fix.
  const { spec, unsupported } = importCodegen(
    [
      "test('checkout', async ({ page }) => {",
      "  await page.frameLocator('#checkout').locator('#pay').click();",
      "  await expect(page.getByTestId('host-banner')).toBeVisible();",
      "});",
    ].join("\n"),
  );
  assert.equal(unsupported.length, 2, unsupported.join("\n"));
  assert.match(
    unsupported[0],
    /on the top-level page while the step from line 2/,
  );
  assert.match(unsupported[0], /cannot say "the page" instead/);
  // …and the click, left with no assertion, is reported by the state-change rule too.
  assert.match(unsupported[1], /changes state and needs an assertion/);
  assert.equal(spec.steps[0].assert, undefined);
});

test("an expect in ANOTHER frame than its action names that frame", () => {
  const { spec, unsupported } = importCodegen(
    [
      "test('checkout', async ({ page }) => {",
      "  await page.frameLocator('#checkout').locator('#pay').click();",
      "  await expect(page.frameLocator('#receipt-frame').getByTestId('receipt')).toBeVisible();",
      "});",
    ].join("\n"),
  );
  assert.deepEqual(unsupported, []);
  assert.deepEqual(spec.steps[0].frame, [{ selector: "#checkout" }]);
  assert.deepEqual(spec.steps[0].assert?.frame, [
    { selector: "#receipt-frame" },
  ]);
});

test("page.frame() is REFUSED — it searches every frame, a chain names a direct child", () => {
  // Not a syntax gap: `page.frame({ name })` searches the whole page at any depth, while
  // a chain link names a direct child so that it identifies ONE frame. Importing one as
  // the other would address a different frame than the script did, silently, and only on
  // the pages where it matters. `url` also takes a glob, which the grammar cannot express
  // at all — `url` is a whole-string compare and `urlPrefix` a prefix.
  const { unsupported } = importCodegen(
    [
      "test('framed', async ({ page }) => {",
      "  await page.frame({ name: 'payment' }).locator('#pay').click();",
      "  await page.frame({ url: '**/embed/*' }).locator('#next').click();",
      "});",
    ].join("\n"),
  );
  assert.equal(unsupported.length, 2, unsupported.join("\n"));
  for (const line of unsupported) {
    assert.match(line, /searches every frame in the page at any depth/);
    assert.match(line, /use frameLocator\("<iframe selector>"\) instead/);
  }
});

test("a frameLocator this cannot address is REPORTED, never guessed at", () => {
  const { unsupported } = importCodegen(
    [
      "test('framed', async ({ page }) => {",
      "  await page.frameLocator(frameSelector).locator('#pay').click();",
      "});",
    ].join("\n"),
  );
  assert.equal(unsupported.length, 1, unsupported.join("\n"));
  assert.match(
    unsupported[0],
    /frameLocator\(\) argument .* is not a plain string/,
  );
});
