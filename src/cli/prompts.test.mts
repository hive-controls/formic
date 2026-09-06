/**
 * `ask`/`askSecret`/`choose` against PassThrough pairs standing in for a real
 * terminal — no actual TTY is ever opened in this suite.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { PassThrough } from "node:stream";
import { ask, askSecret, choose, type PromptIo } from "./prompts.mts";

interface TestIo {
  io: PromptIo;
  input: PassThrough;
  output: PassThrough;
}

function makeIo(isTty = true): TestIo {
  const input = new PassThrough();
  const output = new PassThrough();
  output.setEncoding("utf8");
  return { io: { input, output, isTty }, input, output };
}

/** A plain returned string would freeze at "" — the data listener mutates this
 *  object's field instead, so callers see output written after the call too. */
function readOutput(output: PassThrough): { text: string } {
  const captured = { text: "" };
  output.on("data", (chunk: string) => (captured.text += chunk));
  return captured;
}

/** Writes each answer only once its cue text has actually reached `output` — a new
 *  readline.Interface is created per question (askRaw/askSecret both close after one
 *  answer), and a SECOND line written before that new Interface exists is silently
 *  lost (verified live: two synchronous writes ahead of the second prompt hang
 *  forever). A real terminal never produces that ordering — each Enter is its own
 *  read event — so this reproduces realistic pacing instead of a race. */
function driveAnswers(
  output: PassThrough,
  input: PassThrough,
  steps: { when: RegExp; answer: string }[],
): void {
  let buffer = "";
  let next = 0;
  output.on("data", (chunk: string) => {
    buffer += chunk;
    if (next < steps.length && steps[next].when.test(buffer)) {
      input.write(`${steps[next].answer}\n`);
      buffer = "";
      next++;
    }
  });
}

test("ask: trims the answer and applies default on an empty line", async () => {
  const { io, input, output } = makeIo();
  const captured = readOutput(output);
  const pending = ask(io, "name? ", { default: "fallback" });
  input.write("  bob  \n");
  assert.equal(await pending, "bob");
  assert.match(captured.text, /name\? /);

  const empty = makeIo();
  const pendingEmpty = ask(empty.io, "name? ", { default: "fallback" });
  empty.input.write("\n");
  assert.equal(await pendingEmpty, "fallback");
});

test("askSecret: the prompt reaches output, the secret never does", async () => {
  const { io, input, output } = makeIo();
  const captured = readOutput(output);
  const pending = askSecret(io, "key: ");
  input.write("sk-super-secret\n");
  const answer = await pending;
  assert.equal(answer, "sk-super-secret");
  assert.match(captured.text, /key: /);
  assert.ok(!captured.text.includes("sk-super-secret"));
});

test("choose: re-asks once on garbage input, then returns the picked value", async () => {
  const { io, input, output } = makeIo();
  const captured = readOutput(output);
  driveAnswers(output, input, [
    { when: /healer\?/, answer: "nope" },
    { when: /try again/, answer: "2" },
  ]);
  const pending = choose(io, "healer?", [
    { label: "agent:claude", value: "agent:claude" },
    { label: "openai-compatible", value: "openai-compatible" },
  ]);
  assert.equal(await pending, "openai-compatible");
  assert.match(captured.text, /try again/);
});

test("choose: an empty line uses the default index", async () => {
  const { io, input } = makeIo();
  const pending = choose(
    io,
    "healer?",
    [
      { label: "a", value: "a" },
      { label: "b", value: "b" },
    ],
    { default: 1 },
  );
  input.write("\n");
  assert.equal(await pending, "b");
});

test("terminal:false input (a plain PassThrough) still answers", async () => {
  const { io, input } = makeIo(false);
  const pending = ask(io, "q? ");
  input.write("answered\n");
  assert.equal(await pending, "answered");
});
