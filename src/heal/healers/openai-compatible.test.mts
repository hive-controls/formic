/**
 * The OpenAI-compatible healer against a fake endpoint: the request shape a real
 * provider would receive, and the parsing of what one sends back.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import type { HealContext } from "../types.mts";
import { openAiCompatibleHealer } from "./openai-compatible.mts";

const CONTEXT: HealContext = {
  spec: {
    name: "t",
    startUrl: "http://app.test/",
    steps: [
      { id: "st_1", index: 1, action: "goto", target: "http://app.test/" },
      {
        id: "st_2",
        index: 2,
        action: "click",
        target: "#signin-button",
        assert: { testId: "current-user", hasText: "ops@forgedepot.test" },
      },
    ],
  },
  failure: {
    stepId: "st_2",
    index: 2,
    action: "click",
    target: "#signin-button",
    phase: "action",
    error: "page.click: Timeout 2000ms exceeded.",
  },
  failedStep: {
    id: "st_2",
    index: 2,
    action: "click",
    target: "#signin-button",
    assert: { testId: "current-user", hasText: "ops@forgedepot.test" },
  },
  url: "http://app.test/",
  ariaSnapshot: '- button "Sign in"',
  attempt: 1,
  priorAttempts: [],
};

async function fakeEndpoint(status: number, body: unknown) {
  let captured:
    | { path: string; headers: Record<string, unknown>; body: string }
    | undefined;
  const server = createServer((req, res) => {
    let data = "";
    req.on("data", (chunk) => (data += chunk));
    req.on("end", () => {
      captured = { path: req.url ?? "", headers: req.headers, body: data };
      res.writeHead(status, { "content-type": "application/json" });
      res.end(JSON.stringify(body));
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address() as AddressInfo;
  return {
    baseUrl: `http://127.0.0.1:${port}/v1`,
    captured: () => captured,
    close: () => new Promise<void>((r) => server.close(() => r())),
  };
}

test("sends the rules, the brief, the model and the bearer; parses a fenced JSON reply", async () => {
  const endpoint = await fakeEndpoint(200, {
    choices: [
      {
        message: {
          content:
            'Here you go:\n```json\n{"kind":"rewrite-target","stepId":"st_2","target":"#submit-login","reason":"renamed"}\n```',
        },
      },
    ],
  });
  try {
    const healer = openAiCompatibleHealer({
      baseUrl: endpoint.baseUrl,
      model: "claude-sonnet-5",
      apiKey: "k-test",
    });
    const proposal = await healer.propose(CONTEXT);
    assert.deepEqual(proposal, {
      kind: "rewrite-target",
      stepId: "st_2",
      target: "#submit-login",
      reason: "renamed",
    });
    const request = endpoint.captured()!;
    assert.equal(request.path, "/v1/chat/completions");
    assert.equal(request.headers.authorization, "Bearer k-test");
    const body = JSON.parse(request.body) as {
      model: string;
      messages: { role: string; content: string }[];
    };
    assert.equal(body.model, "claude-sonnet-5");
    assert.equal(body.messages[0].role, "system");
    assert.match(body.messages[0].content, /Never change a step's assert/);
    assert.match(body.messages[1].content, /st_2/);
    assert.match(body.messages[1].content, /Sign in/);
    assert.equal(healer.modelVersion, "claude-sonnet-5");
  } finally {
    await endpoint.close();
  }
});

test("a non-2xx reply is an error naming the model, endpoint and status — never a guessed proposal", async () => {
  const endpoint = await fakeEndpoint(429, {
    error: { message: "rate limited" },
  });
  try {
    const healer = openAiCompatibleHealer({
      baseUrl: endpoint.baseUrl,
      model: "qwen3",
    });
    await assert.rejects(
      healer.propose(CONTEXT),
      /qwen3 .*HTTP 429.*rate limited/,
    );
  } finally {
    await endpoint.close();
  }
});

test("a reply that is not a valid proposal is rejected by the strict parser", async () => {
  const endpoint = await fakeEndpoint(200, {
    choices: [
      {
        message: {
          content:
            '{"kind":"rewrite-target","stepId":"st_2","target":"#x","assert":{"selector":"#y"},"reason":"smuggle"}',
        },
      },
    ],
  });
  try {
    const healer = openAiCompatibleHealer({
      baseUrl: endpoint.baseUrl,
      model: "m",
    });
    await assert.rejects(healer.propose(CONTEXT), /assert is not allowed/);
  } finally {
    await endpoint.close();
  }
});

test("captures token usage and latency on the healer object, never on the proposal", async () => {
  const endpoint = await fakeEndpoint(200, {
    choices: [
      {
        message: { content: '{"kind":"no-repair","reason":"nothing to fix"}' },
      },
    ],
    usage: { prompt_tokens: 120, completion_tokens: 40 },
  });
  try {
    const healer = openAiCompatibleHealer({
      baseUrl: endpoint.baseUrl,
      model: "m",
    });
    assert.equal(healer.lastUsage, null);
    assert.equal(healer.lastLatencyMs, null);
    const proposal = await healer.propose(CONTEXT);
    assert.deepEqual(proposal, { kind: "no-repair", reason: "nothing to fix" });
    assert.deepEqual(healer.lastUsage, { inputTokens: 120, outputTokens: 40 });
    assert.ok(
      typeof healer.lastLatencyMs === "number" && healer.lastLatencyMs >= 0,
    );
  } finally {
    await endpoint.close();
  }
});
