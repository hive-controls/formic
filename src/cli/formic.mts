#!/usr/bin/env node
/**
 * `formic` — the platform's one command.
 *
 *   formic run <recipe> [args…]   resolve configuration, then run the recipe
 *   formic setup                  Launchie: detect agents, smoke-test, write a Cocoon
 *
 * Everything before the recipe's own arguments belongs to the launcher; everything
 * after is the recipe's, passed through untouched.
 */
import { runRecipe } from "../run/run.mts";
import { runSetup } from "./setup/setup.mts";
import { defaultDetectSeams } from "./setup/detect.mts";

const USAGE = `usage:
  formic run <recipe> [--manifest <path>] [--profile <name>] [-- ] [recipe args…]
  formic setup [--non-interactive …]`;

function optionValue(argv: string[], name: string): string | undefined {
  const index = argv.indexOf(name);
  return index >= 0 ? argv[index + 1] : undefined;
}

/** Launcher options are recognised only BEFORE the recipe's own arguments start, so a
 *  recipe with a `--profile` flag of its own is never intercepted. An explicit `--`
 *  ends the launcher's arguments outright. */
function splitLauncherArgs(argv: string[]): {
  launcher: string[];
  recipeArgs: string[];
} {
  const separator = argv.indexOf("--");
  if (separator >= 0) {
    return {
      launcher: argv.slice(0, separator),
      recipeArgs: argv.slice(separator + 1),
    };
  }
  const known = new Set(["--manifest", "--profile"]);
  let index = 0;
  while (index < argv.length && known.has(argv[index])) index += 2;
  return { launcher: argv.slice(0, index), recipeArgs: argv.slice(index) };
}

async function main(): Promise<number> {
  const [subcommand, ...rest] = process.argv.slice(2);
  if (!subcommand || subcommand === "--help" || subcommand === "-h") {
    console.log(USAGE);
    return subcommand ? 0 : 2;
  }
  if (subcommand === "setup") {
    return runSetup(rest, {
      cwd: process.cwd(),
      env: process.env,
      io: {
        input: process.stdin,
        output: process.stdout,
        isTty: Boolean(process.stdin.isTTY),
      },
      log: (line: string) => console.log(line),
      error: (line: string) => console.error(line),
      detect: defaultDetectSeams,
    });
  }
  if (subcommand !== "run") {
    console.error(`unknown command "${subcommand}"\n${USAGE}`);
    return 2;
  }
  const [recipe, ...tail] = rest;
  if (!recipe) {
    console.error(`run needs a recipe name\n${USAGE}`);
    return 2;
  }
  const { launcher, recipeArgs } = splitLauncherArgs(tail);
  return runRecipe({
    recipe,
    args: recipeArgs,
    cwd: process.cwd(),
    env: process.env,
    manifestPath: optionValue(launcher, "--manifest"),
    profile: optionValue(launcher, "--profile"),
    log: (line: string) => console.log(line),
  });
}

main().then(
  (code) => {
    process.exitCode = code;
  },
  (error: Error) => {
    console.error(error.message);
    process.exitCode = 2;
  },
);
