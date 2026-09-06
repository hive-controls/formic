/**
 * Interactive prompts for Launchie — three primitives over node:readline, always
 * built with `terminal: true` regardless of `PromptIo.isTty` (readline's own
 * documented mask workaround only fires in that mode, and it works fine reading a
 * plain, non-tty stream too — verified live on node 22, 2026-09-01: the output
 * stream carried the prompt text, never the typed characters).
 *
 * `askSecret` mutes echo by overwriting `readline.Interface`'s undocumented
 * `_writeToOutput` hook, muted from the moment the interface is created — never
 * "after `question()` prints the prompt", which races: `question()` writes the
 * prompt SYNCHRONOUSLY, and a caller that reacts to that write synchronously (a
 * scripted/piped answer, not a human's reaction time) can have its first keystroke
 * echo before a later mute line ever runs (reproduced live: a reactive test driver
 * that types the instant the prompt appears leaked the secret's first character).
 * So the prompt itself is written directly to `output`, bypassing readline's own
 * prompt-drawing path entirely — the mask is never in a race with anything.
 */
import { createInterface, type Interface } from "node:readline";

export interface PromptIo {
  input: NodeJS.ReadableStream;
  output: NodeJS.WritableStream;
  isTty: boolean;
}

export interface ChooseOption<T> {
  label: string;
  value: T;
  hint?: string;
}

interface MaskableInterface extends Interface {
  _writeToOutput(stringToWrite: string): void;
}

function askRaw(io: PromptIo, question: string): Promise<string> {
  const rl = createInterface({
    input: io.input,
    output: io.output,
    terminal: true,
  });
  return new Promise<string>((resolveAsk) => {
    rl.question(question, (answer) => resolveAsk(answer));
  }).finally(() => rl.close());
}

export async function ask(
  io: PromptIo,
  question: string,
  opts: { default?: string } = {},
): Promise<string> {
  const answer = (await askRaw(io, question)).trim();
  return answer === "" && opts.default !== undefined ? opts.default : answer;
}

export function askSecret(io: PromptIo, question: string): Promise<string> {
  const rl = createInterface({
    input: io.input,
    output: io.output,
    terminal: true,
  }) as MaskableInterface;
  // Muted from creation — readline never gets a chance to echo anything through its
  // own writer. question("") arms the line listener FIRST, so a reply written the
  // instant the prompt appears is never lost; the visible prompt is then printed by
  // hand, bypassing readline's own prompt-drawing path (and its mute-timing race)
  // entirely.
  rl._writeToOutput = () => {};
  return new Promise<string>((resolveAsk) => {
    rl.question("", (answer) => resolveAsk(answer));
    io.output.write(question);
  }).finally(() => rl.close());
}

function renderMenu<T>(question: string, options: ChooseOption<T>[]): string {
  const lines = options.map(
    (option, index) =>
      `  ${index + 1}. ${option.label}${option.hint ? ` (${option.hint})` : ""}`,
  );
  return [question, ...lines, "> "].join("\n");
}

export async function choose<T>(
  io: PromptIo,
  question: string,
  options: ChooseOption<T>[],
  opts: { default?: number } = {},
): Promise<T> {
  let prompt = renderMenu(question, options);
  for (;;) {
    const answer = await ask(io, prompt);
    if (answer === "" && opts.default !== undefined) {
      return options[opts.default].value;
    }
    const picked = Number(answer);
    if (Number.isInteger(picked) && picked >= 1 && picked <= options.length) {
      return options[picked - 1].value;
    }
    prompt = `not a number from 1 to ${options.length} — try again\n${renderMenu(question, options)}`;
  }
}
