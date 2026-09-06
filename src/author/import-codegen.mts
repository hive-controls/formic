/**
 * Import from Playwright codegen output (`npx playwright codegen --target playwright-test`,
 * its default target) — the adopter on-ramp: bring a script you already recorded with
 * Playwright's own Inspector, get a spec back. Deliberately a line-oriented parser over the
 * small, very regular statement shapes codegen itself emits (`await page.<action>(...)`,
 * `await <locator-chain>.<action>(...)`, `await expect(<locator-chain>).<matcher>(...)`),
 * not a TypeScript parser — codegen output is machine-generated and one statement per line,
 * so this covers the real corpus without a compiler dependency.
 *
 * The rule the whole module is built around: anything outside that shape is REPORTED, never
 * partially interpreted. An argument is accepted only when the entire expression is a plain
 * string literal — `page.goto(process.env.URL ?? "http://fallback/")` is reported, not
 * quietly imported as the fallback, and `toHaveText(/approved/)` is reported, not quietly
 * weakened to a visibility-only assertion. A half-understood line that still produces a step
 * is worse than no import: it emits a test that passes while meaning something else.
 *
 * An `expect(...)` line attaches its assertion to the PRECEDING action step — that is the
 * only ordering codegen produces (act, then assert what changed) and it is exactly the
 * step/assert pairing the spec grammar already expects. Attachment is tracked explicitly,
 * so an unsupported line between the action and the expect breaks the pairing and the
 * expect is reported as orphaned rather than attached to the wrong step. The grammar gives
 * a step ONE assertion, so a second expect on the same step is reported too.
 */
import {
  SECRET_PLACEHOLDER_PATTERN,
  VALUE_FROM_FORM,
  type ActionKind,
  type Assertion,
  type FrameChain,
  type FrameRef,
  type Spec,
  type SpecStep,
} from "../spec/types.mts";
import { authoredCapturedAt } from "./stamp.mts";

/** Actions the spec grammar requires an assertion on. Mirrors `spec/parse.mts`'s own rule;
 *  duplicated as a set here only so the importer can name the SOURCE LINE, which the
 *  validator (working on a parsed spec) has no way to know. */
const STATE_CHANGING: ReadonlySet<string> = new Set([
  "click",
  "fill",
  "press",
  "select",
]);

/** The id an imported step carries: its 1-based position. Same reasoning as authoring —
 *  importing the same script twice must produce the same bytes. */
function derivedStepId(index: number): string {
  return `s${index}`;
}

export interface ImportResult {
  spec: Spec;
  unsupported: string[];
}

/** The whole expression must BE one plain string literal — anchored at both ends, with
 *  escapes consumed so a literal quote inside the string cannot end it early. A template
 *  literal, an identifier, a `??` chain, a regex, or a concatenation all fall through to
 *  `undefined`, which every caller turns into an `unsupported` report. */
const STRING_LITERAL = /^(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)")$/;

const ESCAPE_REPLACEMENTS: Record<string, string> = {
  n: "\n",
  r: "\r",
  t: "\t",
  b: "\b",
  f: "\f",
  v: "\v",
  "0": "\0",
};

/** Resolves the escapes a JavaScript string literal may carry. `\uXXXX` and `\xXX` are
 *  deliberately NOT decoded: they are rare in codegen output and a wrong guess would put a
 *  different character in the spec than the script names, so they fall through to the
 *  literal-parse failure and get reported. */
function unescapeLiteral(body: string): string | undefined {
  if (/\\(?:u|x)/.test(body)) return undefined;
  return body.replace(/\\(.)/g, (_whole, char: string) =>
    Object.prototype.hasOwnProperty.call(ESCAPE_REPLACEMENTS, char)
      ? ESCAPE_REPLACEMENTS[char]
      : char,
  );
}

function unquoteArg(raw: string): string | undefined {
  const match = STRING_LITERAL.exec(raw.trim());
  if (!match) return undefined;
  return unescapeLiteral(match[1] ?? match[2]);
}

/**
 * A resolved locator. `assertBase` is always the grammar's own STRUCTURED fields — the
 * replay runner builds the Playwright locator from `role`/`name`/`text`/`exact`/`testId`
 * itself (replay/assertions.mts), so nothing is ever interpolated into a selector string.
 *
 * `resolveTarget` is the separate question of whether this locator can also address an
 * ACTION. A step's target is a plain selector string with no structured form, so only the
 * two selector-shaped locators can fill it: `getByTestId` (as a CSS attribute selector) and
 * `locator()` (the author's own selector, verbatim). A role or text locator has no
 * lossless selector spelling — the old code interpolated `role=button[name="…"]`, which a
 * name containing a quote turned into a malformed selector, silently — so it resolves for
 * an assertion and is refused by name for an action. Computing the selector spelling is
 * deferred behind this closure — for `getByTestId` it can itself be refused (a control
 * character no CSS attribute selector can carry) — so an assertion-only use of the same
 * locator, which never calls it, is never refused over a constraint that belongs to the
 * action path alone.
 */
interface ResolvedLocator {
  assertBase: Assertion;
  /** The frame chain the locator was addressed through, when codegen wrote one. */
  frame?: FrameChain;
  /** Present only when this locator can address an action step. */
  resolveTarget?: () => { target: string } | { refuse: string };
  /** How to name this locator in a refusal. */
  kind: string;
}

/** A CSS attribute selector carrying `value` losslessly, or a refusal naming why not.
 *  Control characters have no escape a CSS attribute value can carry; `"` and `\` do. */
function testIdSelector(
  value: string,
): { selector: string } | { refuse: string } {
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0;
    if (code < 0x20) {
      return {
        refuse: `the test id contains the control character U+${code
          .toString(16)
          .toUpperCase()
          .padStart(4, "0")}, which a CSS attribute selector cannot carry`,
      };
    }
  }
  return {
    selector: `[data-testid="${value.replace(/["\\]/g, (c) => `\\${c}`)}"]`,
  };
}

/** Reads a trailing `{ … }` locator option. Only `exact` maps onto the grammar; every
 *  other option (and any option we cannot read) is a refusal, because dropping it changes
 *  what the locator matches. */
function readLocatorOptions(
  raw: string,
): { options: { name?: string; exact?: boolean } } | { refuse: string } {
  const body = /^\{([\s\S]*)\}$/.exec(raw.trim());
  if (!body)
    return { refuse: `options ${raw.trim()} is not an object literal` };
  const options: { name?: string; exact?: boolean } = {};
  for (const entry of splitArgs(body[1])) {
    if (entry === "") continue;
    const pair = /^([A-Za-z_$][\w$]*)\s*:\s*([\s\S]*)$/.exec(entry);
    if (!pair)
      return { refuse: `options entry ${entry} is not a key/value pair` };
    const [, key, rawValue] = pair;
    if (key === "name") {
      const name = unquoteArg(rawValue);
      if (name === undefined) {
        return {
          refuse: `option name ${rawValue} is not a plain string literal`,
        };
      }
      options.name = name;
    } else if (key === "exact") {
      if (rawValue !== "true" && rawValue !== "false") {
        return { refuse: `option exact ${rawValue} is not true or false` };
      }
      options.exact = rawValue === "true";
    } else {
      return {
        refuse: `option ${key} has no equivalent in the spec grammar, and dropping it would change what the locator matches`,
      };
    }
  }
  return { options };
}

/**
 * Splits a leading frame chain off a codegen locator expression.
 *
 * Playwright's own recorder writes `page.frameLocator("#child").getByTestId("x")` for an
 * element inside an iframe, and `page.frame({ name: "x" })` is the other spelling a
 * hand-written script reaches for. Both are the grammar's frame chain, outermost first —
 * so they are READ rather than reported, and the rest of the expression resolves exactly
 * as it does on the page.
 *
 * `frameLocator()` takes one plain string literal; `frame()` takes an object naming the
 * frame by `name` or `url`. Anything else in either position is refused by name, never
 * guessed at: a frame this cannot address is a step addressed to the wrong document.
 */
function peelFrameChain(
  expr: string,
): { chain: FrameChain; rest: string } | { refuse: string } {
  const chain: FrameChain = [];
  let rest = expr;
  for (;;) {
    const call = /^page\.(frameLocator|frame)\(([^)]*)\)\.(.*)$/.exec(rest);
    if (call === null) return { chain, rest };
    const [, method, rawArgs, tail] = call;
    const link =
      method === "frameLocator"
        ? frameLocatorLink(rawArgs)
        : frameOptionsLink(rawArgs);
    if ("refuse" in link) return link;
    chain.push(link.link);
    rest = `page.${tail}`;
  }
}

function frameLocatorLink(
  rawArgs: string,
): { link: FrameRef } | { refuse: string } {
  const args = splitArgs(rawArgs);
  if (args.length !== 1) {
    return {
      refuse: `frameLocator() takes one string literal here, got ${args.length} argument(s)`,
    };
  }
  const selector = unquoteArg(args[0]);
  if (selector === undefined) {
    return {
      refuse: `frameLocator() argument ${args[0]} is not a plain string literal`,
    };
  }
  return { link: { selector } };
}

/**
 * `page.frame(...)` — refused, by name, and the reason is semantic rather than syntactic.
 *
 * `page.frame({ name })` searches EVERY frame in the page, at any depth. A frame chain
 * names a direct child at each link, on purpose: that is what lets a chain identify one
 * frame rather than whichever frame with that name the runtime happens to meet first.
 * The two are not the same question, and importing one as the other would address a
 * different frame than the script did — silently, and only on the pages where it matters.
 *
 * `page.frame({ url })` also accepts a GLOB, which the grammar has no form for at all:
 * `url` is a whole-string compare and `urlPrefix` a prefix, so a glob would import as a
 * literal that matches nothing.
 *
 * `frameLocator()` is the form that maps, and it is the form codegen writes.
 */
function frameOptionsLink(rawArgs: string): { refuse: string } {
  return {
    refuse: `frame(${rawArgs.trim()}) searches every frame in the page at any depth, while a spec's frame chain names a direct child at each link — importing it could address a different frame; use frameLocator("<iframe selector>") instead`,
  };
}

/** Splits a locator expression into the grammar's structured fields, or a refusal naming
 *  the construct. Handles getByTestId / getByRole / getByText / locator; getByLabel,
 *  getByPlaceholder, getByAltText, getByTitle and anything not built on `page` have no
 *  equivalent and are refused by name. */
function resolveLocator(expr: string): ResolvedLocator | { refuse: string } {
  const peeled = peelFrameChain(expr.trim());
  if ("refuse" in peeled) return peeled;
  const call = /^page\.(\w+)\(([\s\S]*)\)$/.exec(peeled.rest);
  if (!call)
    return { refuse: `locator ${expr} is not a page.<locator>() call` };
  const [, method, rawArgs] = call;
  const args = splitArgs(rawArgs);
  const frame = peeled.chain.length === 0 ? {} : { frame: peeled.chain };

  if (method === "getByTestId" || method === "locator") {
    if (args.length !== 1) {
      return {
        refuse: `${method}() takes one string literal here, got ${args.length} argument(s)`,
      };
    }
    const value = unquoteArg(args[0]);
    if (value === undefined) {
      return {
        refuse: `${method}() argument ${args[0]} is not a plain string literal`,
      };
    }
    if (method === "locator") {
      return {
        assertBase: { selector: value },
        ...frame,
        resolveTarget: () => ({ target: value }),
        kind: "locator",
      };
    }
    return {
      assertBase: { testId: value },
      ...frame,
      resolveTarget: () => {
        const selector = testIdSelector(value);
        return "refuse" in selector ? selector : { target: selector.selector };
      },
      kind: "getByTestId",
    };
  }

  if (method === "getByRole" || method === "getByText") {
    if (args.length < 1 || args.length > 2) {
      return {
        refuse: `${method}() takes one or two arguments, got ${args.length}`,
      };
    }
    const first = unquoteArg(args[0]);
    if (first === undefined) {
      return {
        refuse: `${method}() argument ${args[0]} is not a plain string literal`,
      };
    }
    let options: { name?: string; exact?: boolean } = {};
    if (args.length === 2) {
      const read = readLocatorOptions(args[1]);
      if ("refuse" in read) return read;
      options = read.options;
    }
    if (method === "getByText" && options.name !== undefined) {
      return { refuse: "getByText() has no name option" };
    }
    const assertBase: Assertion =
      method === "getByRole" ? { role: first } : { text: first };
    if (options.name !== undefined) assertBase.name = options.name;
    if (options.exact !== undefined) assertBase.exact = options.exact;
    // No target: a role or text locator has no lossless selector spelling, so it can
    // describe what an assertion checks but cannot address what an action clicks.
    return { assertBase, ...frame, kind: method };
  }

  return {
    refuse: `${method}() has no equivalent in the spec grammar`,
  };
}

const LOCATOR_EXPRESSION =
  "page(?:\\.(?:frameLocator|frame)\\([^)]*\\))*\\.\\w+\\([^)]*\\)";
const CHAIN_ACTION = new RegExp(
  `^await (${LOCATOR_EXPRESSION})\\.(click|fill|press|selectOption)\\((.*)\\);$`,
);
const PLAIN_ACTION = /^await page\.(click|fill|press|selectOption)\((.*)\);$/;
const GOTO = /^await page\.goto\((.*)\);$/;
const EXPECT = new RegExp(
  `^await expect\\((${LOCATOR_EXPRESSION})\\)\\.(toBeVisible|toContainText|toHaveText)\\((.*)\\);$`,
);
const TEST_TITLE = /^test\(\s*(['"][^'"]*['"])/;

const ACTION_TO_KIND: Record<string, ActionKind> = {
  click: "click",
  fill: "fill",
  press: "press",
  selectOption: "select",
};

/** Splits an argument list on top-level commas only. A naive `split(",")` cut string
 *  literals containing a comma in half, and each half then failed to parse as a literal —
 *  turning a perfectly supported `fill('#a', 'Smith, John')` into a bogus report (or, with
 *  the old loose matcher, a truncated value). Quotes and their escapes are tracked; nesting
 *  depth keeps an options object's own commas out of the split. */
function splitArgs(argsStr: string): string[] {
  const trimmed = argsStr.trim();
  if (trimmed === "") return [];
  const args: string[] = [];
  let current = "";
  let quote: string | undefined;
  let depth = 0;
  for (let i = 0; i < trimmed.length; i++) {
    const char = trimmed[i];
    if (quote !== undefined) {
      current += char;
      if (char === "\\" && i + 1 < trimmed.length) {
        current += trimmed[++i];
      } else if (char === quote) {
        quote = undefined;
      }
      continue;
    }
    if (char === "'" || char === '"' || char === "`") {
      quote = char;
      current += char;
      continue;
    }
    if (char === "(" || char === "[" || char === "{") depth++;
    if (char === ")" || char === "]" || char === "}") depth--;
    if (char === "," && depth === 0) {
      args.push(current.trim());
      current = "";
      continue;
    }
    current += char;
  }
  args.push(current.trim());
  return args;
}

function isBoilerplate(line: string): boolean {
  return (
    line === "" ||
    line.startsWith("import ") ||
    /^test\(/.test(line) ||
    line === "});" ||
    line === "})" ||
    line === "}" ||
    line.startsWith("//")
  );
}

export function importCodegen(source: string): ImportResult {
  const lines = source.split(/\r?\n/);
  const steps: SpecStep[] = [];
  const unsupported: string[] = [];
  let name: string | undefined;
  // The step a following `expect()` may attach to, and the line that produced it. Cleared
  // by anything unsupported, so an expect can never hop over a line we failed to
  // understand and land on an earlier, unrelated step.
  let attachable: { step: SpecStep; line: number } | undefined;
  // Source line per step, so a step that ends up missing a required assertion can be
  // reported at the line the reader has to go and fix.
  const stepLines = new Map<SpecStep, number>();

  /**
   * The single way anything is reported. Reporting ALWAYS clears attachment: once a line
   * has not been understood, the importer no longer knows what the next `expect()` was
   * meant to describe, and attaching it to the last step it happens to remember is how an
   * assertion silently lands on the wrong action. The one exception is an `expect()` that
   * had nothing to attach to in the first place — there is nothing to invalidate.
   */
  function report(lineNo: number, message: string): void {
    unsupported.push(`line ${lineNo}: ${message}`);
    attachable = undefined;
  }

  lines.forEach((rawLine, i) => {
    const line = rawLine.trim();
    const lineNo = i + 1;

    const titleMatch = TEST_TITLE.exec(line);
    if (titleMatch) {
      name = unquoteArg(titleMatch[1]);
      return;
    }
    if (isBoilerplate(line)) return;

    const gotoMatch = GOTO.exec(line);
    if (gotoMatch) {
      const gotoArgs = splitArgs(gotoMatch[1]);
      if (gotoArgs.length !== 1) {
        report(
          lineNo,
          `goto() takes one string literal here, got ${gotoArgs.length} argument(s) — ${gotoMatch[1].trim()}`,
        );
        return;
      }
      const target = unquoteArg(gotoArgs[0]);
      if (target === undefined) {
        report(
          lineNo,
          `goto() argument is not a plain string literal — ${gotoArgs[0]}`,
        );
        return;
      }
      const step: SpecStep = {
        id: derivedStepId(steps.length + 1),
        index: steps.length + 1,
        action: "goto",
        target,
      };
      steps.push(step);
      stepLines.set(step, lineNo);
      attachable = { step, line: lineNo };
      return;
    }

    const expectMatch = EXPECT.exec(line);
    if (expectMatch) {
      const resolved = resolveLocator(expectMatch[1]);
      if ("refuse" in resolved) {
        report(lineNo, `unsupported assertion locator — ${resolved.refuse}`);
        return;
      }
      const matcher = expectMatch[2];
      const matcherArgs = splitArgs(expectMatch[3]);
      // A matcher option (`{ timeout: 1 }`) has no equivalent in the grammar, and an
      // import that dropped it would assert on a different schedule than the script did.
      let extra: Assertion;
      if (matcher === "toBeVisible") {
        if (matcherArgs.length > 0) {
          report(
            lineNo,
            `${matcher}() takes no argument the spec grammar can carry — ${expectMatch[3].trim()}`,
          );
          return;
        }
        extra = { visible: true };
      } else {
        if (matcherArgs.length !== 1) {
          report(
            lineNo,
            `${matcher}() takes one string literal here, got ${matcherArgs.length} argument(s) — ${expectMatch[3].trim()}`,
          );
          return;
        }
        const matcherArg = unquoteArg(matcherArgs[0]);
        if (matcherArg === undefined) {
          report(
            lineNo,
            `${matcher}() argument is not a plain string literal — ${matcherArgs[0]}`,
          );
          return;
        }
        extra =
          matcher === "toContainText"
            ? { containsText: matcherArg }
            : { hasText: matcherArg };
      }
      // Only once the whole line is understood does attachment matter. These two are not
      // failures to READ the line, so they do not invalidate the pending step.
      if (attachable === undefined) {
        report(lineNo, "expect() with no preceding action to attach to");
        return;
      }
      if (attachable.step.assert !== undefined) {
        // The grammar gives a step exactly one Assertion, so the second expect has
        // nowhere to go. Overwriting silently discarded the first one. Routed through
        // report() (not a direct push) so its attachment-clearing side effect applies here
        // too — otherwise a THIRD expect on the same step finds attachment still set and
        // reports as another "second expect()" instead of the orphan it actually is.
        report(
          lineNo,
          `a second expect() on the step from line ${attachable.line} — a step carries one assertion`,
        );
        return;
      }
      // Frame scope is PER EXPRESSION. `expect(page.getByTestId(...))` is the host page
      // whatever the line before it did, and reading it as the step's frame changed what
      // the imported test checks — silently, into a document the assertion was never
      // written against.
      //
      // An assert with no frame is checked in its step's frame, so a host-page assertion
      // on a framed step is not expressible in the grammar at all. Reported, not
      // approximated: the reader is told which line and why, and no spec is written that
      // means something other than the script it came from.
      const stepFrame = attachable.step.frame;
      const assertFrame = resolved.frame;
      const sameFrame =
        JSON.stringify(assertFrame ?? null) ===
        JSON.stringify(stepFrame ?? null);
      if (!sameFrame && assertFrame === undefined) {
        report(
          lineNo,
          `this expect() is on the top-level page while the step from line ${attachable.line} is inside a frame — an assert with no frame of its own is checked in its step's frame, and the grammar cannot say "the page" instead`,
        );
        return;
      }
      attachable.step.assert = {
        ...resolved.assertBase,
        ...(sameFrame ? {} : { frame: assertFrame }),
        ...extra,
      };
      return;
    }

    const chainMatch = CHAIN_ACTION.exec(line);
    const plainMatch = PLAIN_ACTION.exec(line);
    if (chainMatch || plainMatch) {
      const method = chainMatch ? chainMatch[2] : plainMatch![1];
      const action = ACTION_TO_KIND[method];
      // click takes only its locator; fill/press/select take the thing to type, press or
      // pick. Anything beyond that is an options object (`{ button: 'right' }`), which the
      // grammar cannot carry — importing the call without it would replay a different act.
      const expectedArgs = action === "click" ? 0 : 1;

      let target: string | undefined;
      let valueArgs: string[];
      let frame: FrameChain | undefined;
      if (chainMatch) {
        const resolved = resolveLocator(chainMatch[1]);
        if ("refuse" in resolved) {
          report(lineNo, `unsupported action locator — ${resolved.refuse}`);
          return;
        }
        if (resolved.resolveTarget === undefined) {
          report(
            lineNo,
            `${resolved.kind}() describes an assertion but cannot address an action: a step target is a plain selector, and this locator has no lossless selector spelling`,
          );
          return;
        }
        const targetResult = resolved.resolveTarget();
        if ("refuse" in targetResult) {
          report(lineNo, `unsupported action locator — ${targetResult.refuse}`);
          return;
        }
        target = targetResult.target;
        frame = resolved.frame;
        valueArgs = splitArgs(chainMatch[3]);
      } else {
        const args = splitArgs(plainMatch![2]);
        if (args.length < 1) {
          report(lineNo, `${method}() needs a selector`);
          return;
        }
        target = unquoteArg(args[0]);
        if (target === undefined) {
          report(
            lineNo,
            `${method}() target is not a plain string literal — ${args[0]}`,
          );
          return;
        }
        valueArgs = args.slice(1);
      }

      if (valueArgs.length !== expectedArgs) {
        report(
          lineNo,
          `${method}() takes ${expectedArgs} argument(s) after its locator that the spec grammar can carry, got ${valueArgs.length} — ${valueArgs.join(", ")}`,
        );
        return;
      }
      let value: string | undefined;
      if (expectedArgs === 1) {
        value = unquoteArg(valueArgs[0]);
        // Dropping an unreadable value produced a step the grammar rejects, or worse a
        // truncated one that replayed as something else.
        if (value === undefined) {
          report(
            lineNo,
            `${method}() value is not a plain string literal — ${valueArgs[0]}`,
          );
          return;
        }
        // Codegen recorded against a page whose spec had already withheld this value,
        // so what it captured is the PLACEHOLDER. Importing it produces a spec that
        // replays by typing "<secret:password>" into the login form — refused by name,
        // with the form that works, rather than imported and discovered at replay.
        if (SECRET_PLACEHOLDER_PATTERN.test(value)) {
          report(
            lineNo,
            `${method}() value ${valueArgs[0]} is a redaction placeholder, not a value — put the real one in the environment and write valueFrom: ${VALUE_FROM_FORM} on this step`,
          );
          return;
        }
      }
      const step: SpecStep = {
        id: derivedStepId(steps.length + 1),
        index: steps.length + 1,
        action,
        target,
      };
      if (frame !== undefined) step.frame = frame;
      if (value !== undefined) step.value = value;
      steps.push(step);
      stepLines.set(step, lineNo);
      attachable = { step, line: lineNo };
      return;
    }

    report(lineNo, `unsupported construct — ${line}`);
  });

  // The state-change rule is the spec grammar's, and an import that quietly produced a
  // spec breaking it wrote YAML the product's own loadSpec refuses. Report it here, at the
  // line the reader must edit, rather than leaving it to a validation error with no source
  // position — and never invent the missing assertion.
  for (const step of steps) {
    if (STATE_CHANGING.has(step.action) && step.assert === undefined) {
      report(
        stepLines.get(step)!,
        `"${step.action}" changes state and needs an assertion — codegen emitted no expect() for it`,
      );
    }
  }

  const spec: Spec = {
    name: name ?? "imported",
    startUrl: steps.find((s) => s.action === "goto")?.target ?? "",
    capturedBy: "import-codegen",
    capturedAt: authoredCapturedAt(),
    steps,
  };
  return { spec, unsupported };
}
