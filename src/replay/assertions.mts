/**
 * Assertion → Playwright expectation. The whole mapping, in one place.
 *
 * Each `Assertion` field maps to exactly one Playwright matcher, and every present
 * field must hold (conjunction). This is the runtime half of the state-change rule:
 * the spec forces a state-changing step to carry an assertion, and this is what makes
 * that assertion actually bite. Data drift (`swapped-data`) is caught HERE and nowhere
 * else — every locator in that breakage still resolves.
 *
 * `expect` from @playwright/test is usable outside its test runner (verified 2026-09-01
 * against 1.62.1). It auto-retries until the timeout, which is what a post-action
 * assertion needs: the action returns before its consequence renders.
 */
import { expect } from "@playwright/test";
import type { Locator, Page } from "playwright-core";
import type { Assertion } from "../spec/types.mts";
import { resolveFrameChain, type LocatorScope } from "./frames.mts";
import {
  LOCATOR_PRECEDENCE,
  type LocatorField,
} from "../spec/locator-precedence.mts";

/** How each field of the spec's locator contract resolves against a live page. The
 *  ORDER is not stated here — `LOCATOR_PRECEDENCE` owns it (spec/locator-precedence.mts)
 *  and this table is sorted by it below, so replay, export and capture can never
 *  disagree about which field wins. */
const LOCATOR_BUILDERS: Record<
  LocatorField,
  (scope: LocatorScope, assertion: Assertion) => Locator
> = {
  testId: (page, a) => page.getByTestId(a.testId as string),
  selector: (page, a) => page.locator(a.selector as string),
  role: (page, a) =>
    page.getByRole(a.role as Parameters<LocatorScope["getByRole"]>[0], {
      name: a.name,
      exact: a.exact ?? false,
    }),
  text: (page, a) =>
    page.getByText(a.text as string, { exact: a.exact ?? false }),
};

const LOCATOR_RESOLVERS: readonly {
  field: LocatorField;
  build: (scope: LocatorScope, assertion: Assertion) => Locator;
}[] = LOCATOR_PRECEDENCE.map((field) => ({
  field,
  build: LOCATOR_BUILDERS[field],
}));

/** Precedence when more than one locator field is given: `testId` > `selector` >
 *  `role` > `text` — the validator guarantees at least one is present when the
 *  assertion carries no `url`/`urlPrefix`/`urlPattern` field either (see
 *  `hasElementField`). `role`'s `name` and `text` are substring matches by default,
 *  matching Playwright's own default; `exact: true` asks for a whole-string match. */
export function locatorFor(scope: LocatorScope, assertion: Assertion): Locator {
  for (const resolver of LOCATOR_RESOLVERS) {
    if (assertion[resolver.field] !== undefined)
      return resolver.build(scope, assertion);
  }
  throw new Error("assertion has neither testId, selector, role, nor text");
}

/** Whether this assertion carries any element-shaped field — a locator or one of the
 *  predicates that only means something applied to one (`hasText`/`containsText`/
 *  `visible`). The validator refuses this alongside a `url`/`urlPrefix`/`urlPattern`
 *  field, but `compile`/`checkAssertion` can be called directly on a hand-built
 *  `Spec` that skipped validation — gating on the full field set, not just the four
 *  locator fields, means that bypass still resolves a locator (and gets `locatorFor`'s
 *  own refusal if none is present) instead of silently skipping the element check. */
function hasElementField(assertion: Assertion): boolean {
  return (
    assertion.testId !== undefined ||
    assertion.selector !== undefined ||
    assertion.role !== undefined ||
    assertion.name !== undefined ||
    assertion.text !== undefined ||
    assertion.exact !== undefined ||
    assertion.hasText !== undefined ||
    assertion.containsText !== undefined ||
    assertion.visible !== undefined
  );
}

/** `url`/`urlPrefix`/`urlPattern` against the current page URL, each independently
 *  auto-retrying to the timeout via `expect(page).toHaveURL(...)` — the same auto-wait
 *  every other assertion here gets. `urlPrefix` has no native string mode (a plain
 *  string is an EXACT match), so it goes through the predicate form instead.
 *
 *  The validator requires `url`/`urlPrefix` to be absolute (http/https), which is what
 *  makes `toHaveURL(url)` an exact string compare here whether or not this `expect`
 *  ever runs with a `baseURL` configured: `new URL(value, base)` only consults `base`
 *  when `value` is relative. */
async function checkUrlAssertion(
  page: Page,
  assertion: Assertion,
  timeout: { timeout: number },
): Promise<void> {
  if (assertion.url !== undefined) {
    await expect(page).toHaveURL(assertion.url, timeout);
  }
  if (assertion.urlPrefix !== undefined) {
    const prefix = assertion.urlPrefix;
    await expect(page).toHaveURL(
      (url) => url.toString().startsWith(prefix),
      timeout,
    );
  }
  if (assertion.urlPattern !== undefined) {
    await expect(page).toHaveURL(new RegExp(assertion.urlPattern), timeout);
  }
}

/**
 * Throws (with Playwright's own diagnostic message) when any field fails.
 *
 * VISIBILITY IS IMPLIED. Every assertion is about what a reviewer would see in the
 * replay, so the element must be visible unless `visible: false` says otherwise.
 * Measured (2026-09-01, breakage class 3 `changed-flow`): `toHaveText` matches HIDDEN
 * elements, and the sample app's confirmation heading is static text inside a hidden
 * section — so a bare `hasText` assertion passed while the user was still looking at
 * an interstitial. Text-only checks silently pass on what the DOM contains rather
 * than what the page shows; that is the wrong-artifact failure this repo exists to
 * refuse. It also means a locator with no predicate is never vacuous.
 */
export async function checkAssertion(
  page: Page,
  assertion: Assertion,
  timeoutMs: number,
  stepScope: LocatorScope = page,
): Promise<void> {
  const timeout = { timeout: timeoutMs };

  // The url predicates take the PAGE, always. There is one address bar however many
  // frames are open, and the validator refuses a `frame` beside them for that reason —
  // so no scope of any kind is consulted here.
  await checkUrlAssertion(page, assertion, timeout);
  if (!hasElementField(assertion)) return;

  // An assertion inherits the step's frame: the consequence of an action is almost
  // always in the document the action happened in. Its own `frame` is for the case that
  // is not — a click inside a payment iframe whose receipt renders on the host page.
  const scope =
    assertion.frame === undefined
      ? stepScope
      : await resolveFrameChain(page, assertion.frame, timeoutMs);
  const locator = locatorFor(scope, assertion);
  if (assertion.visible === false) {
    await expect(locator).toBeHidden(timeout);
  } else {
    await expect(locator).toBeVisible(timeout);
  }
  if (assertion.hasText !== undefined) {
    await expect(locator).toHaveText(assertion.hasText, timeout);
  }
  if (assertion.containsText !== undefined) {
    await expect(locator).toContainText(assertion.containsText, timeout);
  }
}
