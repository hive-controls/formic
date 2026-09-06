/**
 * Which form fields hold something that must never be written into a spec — one table,
 * one matcher.
 *
 * A recorded spec is committed to a repository, read in pull requests, and attached to
 * evidence bundles. A password is the obvious thing that must not travel that way, but
 * it is not the only one: an email address, a phone number, a home address and a card
 * number are all personal data that a test fixture has no business carrying, and all of
 * them are typed into ordinary text inputs that look like any other.
 *
 * So the classification is DATA, in one place, and the matcher is one self-contained
 * function. The function is self-contained on purpose: `capture/events.mts` injects its
 * source into the page, because the value has to be withheld BEFORE it crosses the
 * binding — a secret that reached the host is already in another process's memory. One
 * implementation runs in both places; there is no second matcher to drift.
 *
 * Three routes, tried in order, because they are ordered by how much the page actually
 * told us:
 *
 *  1. the input's `type` — the browser's own classification, and unambiguous;
 *  2. the `autocomplete` token — what the author declared the field is for;
 *  3. a word match on the field's name, id, label and placeholder — a guess, but the
 *     only signal an unannotated field gives, and the one most real forms leave.
 *
 * Being wrong in the safe direction costs a recorded value the human can supply again
 * (`--include-secrets`, or editing the spec). Being wrong in the other direction commits
 * someone's phone number to a git history. The keyword lists are therefore tight enough
 * not to swallow an ordinary field — an "Approval note" is not an address — and the
 * matcher is whole-word, so `mailing` is not `mail`.
 */

export type SensitiveCategory =
  | "password"
  | "payment"
  | "username"
  | "email"
  | "phone"
  | "address"
  | "identification"
  /**
   * Not a kind of data — the fact that the SPEC refused to carry this value.
   *
   * Every other category answers "will this field hold something sensitive?" from what
   * the page declared about it. A `valueFrom` step has already answered a different and
   * stronger question: whoever wrote the spec decided the value must not be written
   * down, whatever the field looks like. An unlabelled `<input type="text">` is exactly
   * the field every page-side route misses, so replay marks its target with this before
   * filling it and the replay stream masks it like any other classified field. It is
   * never produced by classification, and a recording never writes it into a spec.
   */
  | "referenced";

/** What the page can report about a field, in the shape both callers already have. */
export interface SensitiveFieldDescriptor {
  type?: string;
  autocomplete?: string;
  name?: string;
  id?: string;
  label?: string;
  placeholder?: string;
}

export interface SensitiveTable {
  /** `<input type=...>` — the browser's own classification. */
  byInputType: Record<string, SensitiveCategory>;
  /** A whole `autocomplete` token. */
  byAutocomplete: Record<string, SensitiveCategory>;
  /** An `autocomplete` token family, e.g. every `cc-*`. */
  autocompletePrefixes: { prefix: string; category: SensitiveCategory }[];
  /** Whole-word matches against name / id / label / placeholder, in order. */
  keywords: { category: SensitiveCategory; words: string[] }[];
  /**
   * What a VALUE of each category looks like, as regular-expression SOURCE.
   *
   * The other three routes answer "is this FIELD sensitive" from what the page said
   * about it. This one answers "does this TEXT look sensitive" — needed where there is
   * no field at all, because the application rendered someone's address back onto the
   * screen and a proposal was about to quote it.
   *
   * Source strings, not RegExp objects: this table is JSON-stringified into the page,
   * and a RegExp does not survive that. Tried in order, longest-evidence first.
   */
  valuePatterns: { category: SensitiveCategory; pattern: string }[];
}

export const SENSITIVE_TABLE: SensitiveTable = {
  byInputType: {
    password: "password",
    email: "email",
    tel: "phone",
  },
  byAutocomplete: {
    "current-password": "password",
    "new-password": "password",
    "one-time-code": "password",
    username: "username",
    email: "email",
    "street-address": "address",
    "address-line1": "address",
    "address-line2": "address",
    "address-line3": "address",
    "address-level1": "address",
    "address-level2": "address",
    "postal-code": "address",
    country: "address",
    "country-name": "address",
  },
  autocompletePrefixes: [
    { prefix: "cc-", category: "payment" },
    { prefix: "tel", category: "phone" },
  ],
  keywords: [
    {
      category: "password",
      words: ["password", "passwd", "pwd", "passphrase", "pin"],
    },
    {
      category: "payment",
      words: [
        "card",
        "cardnumber",
        "creditcard",
        "cvv",
        "cvc",
        "csc",
        "iban",
        "bic",
        "swift",
        "sort code",
        "routing",
        "account number",
        "expiry",
      ],
    },
    {
      category: "identification",
      words: [
        "ssn",
        "social security",
        "passport",
        "licence",
        "license",
        "national id",
        "nationalid",
        "tax id",
        "taxid",
        "nino",
      ],
    },
    { category: "email", words: ["email", "e mail", "mail"] },
    {
      category: "phone",
      words: ["phone", "telephone", "tel", "mobile", "cell"],
    },
    {
      category: "address",
      words: [
        "address",
        "street",
        "city",
        "town",
        "postal",
        "postcode",
        "zip",
        "zipcode",
        "county",
        "country",
        "province",
      ],
    },
    { category: "username", words: ["username", "userid", "login", "user id"] },
  ],
  // Deliberately conservative: a false positive costs a proposal the human can offer
  // again interactively, a false negative commits someone's address to a git history.
  // Digit thresholds are set above what ordinary page furniture carries — an order id
  // (SO-4471, four digits) and a money total ($12,480.00, seven) reach none of them.
  valuePatterns: [
    { category: "email", pattern: "[^\\s@]+@[^\\s@]+\\.[A-Za-z]{2,}" },
    { category: "payment", pattern: "\\d(?:[ -]?\\d){12,18}" },
    { category: "identification", pattern: "\\b[A-Za-z]{1,3}[ -]?\\d{6,9}\\b" },
    { category: "phone", pattern: "\\+?\\d(?:[ ().-]?\\d){9,}" },
  ],
};

/** Which of the three routes decided a classification — or `reference`, which is not a
 *  route at all but the spec having settled the question in advance. */
export type SensitiveSource =
  "type" | "autocomplete" | "keyword" | "value" | "reference";

/**
 * ONE classification record, computed once per element and consumed unchanged by every
 * projection downstream — the binding payload, the coalescer, the warning list, the
 * assertion proposal, the step log and the rrweb mask.
 *
 * The provenance is part of the record because a redaction a reviewer cannot account for
 * is one they will turn off. `source` names the route, `evidence` the token or word that
 * route matched, so the warning can say WHY a value was withheld and not merely that it
 * was.
 */
export interface SensitiveClassification {
  category: SensitiveCategory;
  source: SensitiveSource;
  evidence: string;
}

/**
 * How this field classifies, or null when it holds nothing sensitive.
 *
 * SELF-CONTAINED BY CONTRACT. Its source is stringified and injected into the page
 * (`capture/events.mts`), so it may reference nothing but its own parameters and locals
 * — no imports, no module-level constants, no helpers. That constraint is what buys one
 * matcher instead of two.
 */
export function classifySensitiveField(
  field: SensitiveFieldDescriptor,
  table: SensitiveTable,
): SensitiveClassification | null {
  const inputType = (field.type || "").toLowerCase();
  if (table.byInputType[inputType]) {
    return {
      category: table.byInputType[inputType],
      source: "type",
      evidence: inputType,
    };
  }

  const tokens = (field.autocomplete || "").toLowerCase().split(/[\s,]+/);
  for (const token of tokens) {
    if (token === "") continue;
    if (table.byAutocomplete[token]) {
      return {
        category: table.byAutocomplete[token],
        source: "autocomplete",
        evidence: token,
      };
    }
    for (const rule of table.autocompletePrefixes) {
      if (token.indexOf(rule.prefix) === 0) {
        return {
          category: rule.category,
          source: "autocomplete",
          evidence: token,
        };
      }
    }
  }

  // camelCase and every separator become word gaps, so `cardNumber`, `card_number` and
  // `Card Number` are the same three words and a whole-word match can be exact.
  const words = [field.name, field.id, field.label, field.placeholder]
    .map((part) =>
      (part || "")
        .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
        .replace(/[^A-Za-z0-9]+/g, " ")
        .toLowerCase()
        .trim(),
    )
    .filter((part) => part !== "")
    .join(" ");
  if (words === "") return null;
  const padded = " " + words + " ";
  for (const entry of table.keywords) {
    for (const keyword of entry.words) {
      if (padded.indexOf(" " + keyword + " ") >= 0) {
        return {
          category: entry.category,
          source: "keyword",
          evidence: keyword,
        };
      }
    }
  }
  return null;
}

/**
 * How this TEXT classifies, or null when it looks like nothing sensitive.
 *
 * The same table, a different question. `classifySensitiveField` asks whether a field
 * will HOLD something sensitive, from what the page declared about it; this asks whether
 * a string already IS something sensitive, because an application that renders the
 * signed-in user's own address puts personal data on screen where no field decision can
 * reach it — and an assertion built from that screen would quote it into a committed
 * spec. One table of patterns, read here and nowhere else.
 */
export function classifySensitiveValue(
  value: string,
  table: SensitiveTable,
): SensitiveClassification | null {
  const text = value || "";
  if (text === "") return null;
  for (const rule of table.valuePatterns) {
    const found = new RegExp(rule.pattern).exec(text);
    if (found !== null) {
      return { category: rule.category, source: "value", evidence: found[0] };
    }
  }
  return null;
}

/**
 * What the page can say about a field, read from the FULL set of relationships a browser
 * uses to name a control — not `label[for]` alone.
 *
 * `label[for]` was the whole descriptor once, and every other labelling form a real form
 * uses fell straight through it: a field named by `aria-label`, by `aria-labelledby`, by
 * the `<label>` that wraps it, or by two `label[for]` elements carrying one phrase
 * between them classified as if the page had said nothing about it. Each of those is an
 * ordinary way to label a card number.
 *
 * SELF-CONTAINED BY CONTRACT, for the same reason the matcher is: this runs page-side,
 * where the element and its label relationships are. It also declares NO inner function
 * — the source is stringified, and a transpiler that renames functions wraps each one in
 * a helper (`__name`) that does not exist in the page, so an inner arrow turns the whole
 * matcher into a ReferenceError the moment it is injected.
 */
export function sensitiveDescriptorOf(
  element: Element,
): SensitiveFieldDescriptor {
  const sources: (Element | null)[] = [];
  const labelledBy = (element.getAttribute("aria-labelledby") || "").split(
    /\s+/,
  );
  for (const id of labelledBy) {
    if (id !== "") sources.push(document.getElementById(id));
  }
  sources.push(element.closest("label"));
  const bound = document.querySelectorAll("label[for]");
  for (let index = 0; index < bound.length; index++) {
    if (element.id !== "" && bound[index].getAttribute("for") === element.id) {
      sources.push(bound[index]);
    }
  }
  const names: string[] = [element.getAttribute("aria-label") || ""];
  for (const source of sources) {
    if (source)
      names.push((source.textContent || "").replace(/\s+/g, " ").trim());
  }
  const labels: string[] = [];
  for (const name of names) {
    if (name !== "") labels.push(name);
  }
  return {
    type:
      element.tagName === "INPUT"
        ? (element.getAttribute("type") || "text").toLowerCase()
        : "",
    autocomplete: element.getAttribute("autocomplete") || "",
    name: element.getAttribute("name") || "",
    id: element.id || "",
    label: labels.join(" "),
    placeholder: element.getAttribute("placeholder") || "",
  };
}

/** What a redacted value is recorded as. The category is in the placeholder so a
 *  reviewer reading the spec knows what has to be supplied, not merely that something
 *  was withheld. */
export function secretPlaceholder(category: SensitiveCategory): string {
  return `<secret:${category}>`;
}

/** How long a derived variable name may get before it stops being readable. Long
 *  enough for a three-word spec and a two-word field; short enough to type. */
const VARIABLE_NAME_LIMIT = 64;

/** SHOUT_CASE, the way an environment variable is spelled: camelCase becomes two
 *  words, every other separator becomes one gap, and nothing else survives. */
function shoutCase(text: string): string {
  return (text || "")
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/[^A-Za-z0-9]+/g, " ")
    .trim()
    .toUpperCase()
    .replace(/ /g, "_");
}

/**
 * The environment variable a withheld value will be read back from.
 *
 * DETERMINISTIC, because the name goes in the spec and the human puts it in their
 * `.env`: recording the same flow twice must ask for the same variable, or every
 * re-record silently orphans the value that was already supplied.
 *
 * Three parts, in the order a person reads them: which flow, which field, what kind of
 * value. A part that repeats what another already said is dropped — a field labelled
 * "Password" holding a `password` would otherwise become `..._PASSWORD_PASSWORD`.
 *
 * `taken` collision-suffixes: two password fields in one flow (a change-password form)
 * are two different values, and one name for both would replay the same string into
 * both boxes.
 */
export function secretVariableName(
  specName: string,
  category: SensitiveCategory,
  label: string | undefined,
  taken: ReadonlySet<string>,
): string {
  const parts: string[] = [];
  for (const part of [
    shoutCase(specName),
    shoutCase(label ?? ""),
    shoutCase(category),
  ]) {
    if (part !== "" && !parts.includes(part)) parts.push(part);
  }
  // A name must start with a letter (the spec grammar's own rule for `env.<NAME>`), so
  // a spec named "2026 audit" cannot lend it a leading digit.
  const base = parts
    .join("_")
    .replace(/^[^A-Z]+/, "")
    .slice(0, VARIABLE_NAME_LIMIT);
  const stem = base === "" ? shoutCase(category) : base;
  if (!taken.has(stem)) return stem;
  for (let suffix = 2; ; suffix++) {
    const candidate = `${stem}_${suffix}`;
    if (!taken.has(candidate)) return candidate;
  }
}

/** The matcher, the descriptor reader and their table, as page-injectable source. */
export function sensitiveMatcherSource(): string {
  return [
    `var SENSITIVE_TABLE = ${JSON.stringify(SENSITIVE_TABLE)};`,
    `var classifySensitiveField = ${classifySensitiveField.toString()};`,
    `var sensitiveDescriptorOf = ${sensitiveDescriptorOf.toString()};`,
  ].join("\n");
}
