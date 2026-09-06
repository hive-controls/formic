/**
 * A scripted healer: hands back predetermined proposals. The test double for the
 * seam — it makes the loop's behaviour provable without a model, and it is how the
 * loop's own rules (verification, attempt limits, needs-human) are exercised.
 */
import type { HealContext, Healer, RepairProposal } from "./types.mts";

export function scriptedHealer(
  script: RepairProposal[] | ((context: HealContext) => RepairProposal),
  name = "scripted",
): Healer & { calls: HealContext[] } {
  const calls: HealContext[] = [];
  let cursor = 0;
  return {
    name,
    modelVersion: "scripted",
    calls,
    async propose(context) {
      calls.push(context);
      if (typeof script === "function") return script(context);
      const proposal = script[cursor];
      if (proposal === undefined) {
        throw new Error(
          `scripted healer asked for proposal ${cursor + 1} of ${script.length}`,
        );
      }
      cursor++;
      return proposal;
    },
  };
}
