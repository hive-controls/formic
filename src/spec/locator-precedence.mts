/**
 * Locator precedence — the contract, owned by the spec package.
 *
 * Which field wins when a locator carries more than one is a property of the SPEC, not
 * of any one consumer. Three of them read it: the replay runner resolves an assertion
 * with it, the export compiler translates it, and capture derives one from a clicked
 * element with it. When each kept its own copy the order could drift silently, and a
 * derivation that preferred a different field than the resolver would write specs whose
 * locator is not the one the runner uses.
 *
 * The escaping helpers live here for the same reason: a locator VALUE comes from the
 * page under test, so it is untrusted text, and every consumer that puts it into a
 * selector has to escape it identically or resolve a different element than the one the
 * spec names.
 */

/** The spec's four locator forms, in resolution order. */
export type LocatorField = "testId" | "selector" | "role" | "text";

/** `testId` > `selector` > `role` > `text`. The one source of that order. */
export const LOCATOR_PRECEDENCE: readonly LocatorField[] = [
  "testId",
  "selector",
  "role",
  "text",
];

/**
 * Names the first control character in `value`, or null when there is none.
 *
 * Control characters below 0x20 — CR and LF among them — have no lossless escape in
 * every position a locator value lands in: a CSS attribute value cannot carry them at
 * all, and a regex literal is terminated by a line break. Everything else is escapable,
 * so this is the one class refused outright rather than approximated.
 */
export function controlCharacterIn(value: string): string | null {
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0;
    if (code < 0x20) {
      return `U+${code.toString(16).toUpperCase().padStart(4, "0")}`;
    }
  }
  return null;
}

/**
 * A value escaped for the inside of a double-quoted CSS attribute selector.
 *
 * `"` and `\` are the only two characters such a value cannot carry raw, and both have
 * a lossless backslash escape. Unescaped, a `"` closes the attribute early and the rest
 * of the value becomes selector syntax: `[data-testid="x"], body` would match the whole
 * page and quietly satisfy an assertion the spec never made.
 *
 * Control characters are the caller's to refuse — `controlCharacterIn` names them, and
 * a caller that skipped the check would silently emit a selector that cannot match.
 */
export function escapeCssAttributeValue(value: string): string {
  return value.replace(/["\\]/g, "\\$&");
}
