/**
 * Agent detection with fake seams — never a real PATH lookup or a real agent binary.
 * `defaultDetectSeams` itself is not exercised here (it would depend on what's
 * actually installed on the machine running the suite).
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { describeAgent, detectAgents, type DetectSeams } from "./detect.mts";
import type { AgentPreset } from "../../heal/profiles/agent-presets.mts";

const FAKE_ADAPTER: AgentPreset = {
  name: "fake",
  command: "fake-agent",
  args: ["-p", "{prompt}"],
  verified: false,
};
const ADAPTERS = { fake: FAKE_ADAPTER };

test("on PATH: version is the first non-empty stdout line", async () => {
  const calls: { command: string; args: string[] }[] = [];
  const seams: DetectSeams = {
    which: async (command) => `/usr/local/bin/${command}`,
    run: async (command, args) => {
      calls.push({ command, args });
      return { code: 0, stdout: "\nfake-agent 1.2.3\nextra\n", stderr: "" };
    },
  };
  const [agent] = await detectAgents(seams, ADAPTERS);
  assert.deepEqual(agent, {
    name: "fake",
    onPath: true,
    path: "/usr/local/bin/fake-agent",
    version: "fake-agent 1.2.3",
    verified: false,
    measuredLatency: null,
    loginChecked: false,
  });
  // Only the version probe is ever spawned — no login/auth command exists to run.
  assert.deepEqual(calls, [{ command: "fake-agent", args: ["--version"] }]);
});

test("off PATH: no version probe is even attempted", async () => {
  const calls: string[] = [];
  const seams: DetectSeams = {
    which: async () => null,
    run: async (command) => {
      calls.push(command);
      return { code: 0, stdout: "should not run", stderr: "" };
    },
  };
  const [agent] = await detectAgents(seams, ADAPTERS);
  assert.equal(agent.onPath, false);
  assert.equal(agent.path, null);
  assert.equal(agent.version, null);
  assert.deepEqual(calls, []);
});

test("probe timeout or non-zero exit reads back as version: null, never throws", async () => {
  const seams: DetectSeams = {
    which: async (command) => `/bin/${command}`,
    run: async () => ({ code: null, stdout: "", stderr: "" }),
  };
  const [timedOut] = await detectAgents(seams, ADAPTERS);
  assert.equal(timedOut.version, null);

  const failing: DetectSeams = {
    which: async (command) => `/bin/${command}`,
    run: async () => ({ code: 1, stdout: "error text", stderr: "boom" }),
  };
  const [nonZero] = await detectAgents(failing, ADAPTERS);
  assert.equal(nonZero.version, null);
});

test("a rejecting seam never throws out of detectAgents", async () => {
  const seams: DetectSeams = {
    which: async () => {
      throw new Error("spawn failed");
    },
    run: async () => {
      throw new Error("spawn failed");
    },
  };
  const [agent] = await detectAgents(seams, ADAPTERS);
  assert.equal(agent.onPath, false);
});

test("describeAgent: one line, no login command mentioned, latency shown when known", async () => {
  const seams: DetectSeams = {
    which: async (command) => `/bin/${command}`,
    run: async () => ({ code: 0, stdout: "fake-agent 9.9.9\n", stderr: "" }),
  };
  const [agent] = await detectAgents(seams, ADAPTERS);
  const line = describeAgent(agent);
  assert.equal(line, "fake: on PATH (fake-agent 9.9.9)");
  assert.ok(!line.includes("\n"));

  const withLatency = {
    ...agent,
    measuredLatency: "53/68/42 s",
    verified: true,
  };
  assert.equal(
    describeAgent(withLatency),
    "fake: on PATH (fake-agent 9.9.9, verified) — measured 53/68/42 s per repair",
  );

  const offPath = { ...agent, onPath: false, path: null, version: null };
  assert.equal(describeAgent(offPath), "fake: not on PATH");
});
