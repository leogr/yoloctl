// Run inside a disposable OpenShell box, with Pi installed and yolo.space.operator.
// A local deterministic provider exercises the real CLI and shell tool without
// paid model traffic. It establishes no real-provider authentication claim.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:http";

const state = "/home/sandbox/.pi/agent";
const session = `${state}/sessions/yoloctl-smoke.jsonl`;
const marker = "/workdir/pi-shell-smoke";
await mkdir(`${state}/sessions`, { recursive: true });
let previousHistory = "";
try {
  previousHistory = await readFile(session, "utf8");
} catch (error) {
  if (error.code !== "ENOENT") throw error;
}
let requests = 0;
let sawSkill = false;
let serverError;
const server = createServer(async (req, res) => {
  try {
    assert.equal(req.url, "/v1/chat/completions");
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = JSON.parse(Buffer.concat(chunks));
    sawSkill ||= JSON.stringify(body.messages).includes("yolo-box-operator");
    requests++;
    assert.ok(requests <= 2, "unexpected extra model request");
    if (requests === 1 && previousHistory) {
      assert.ok(JSON.stringify(body.messages).includes("pi-response-ok"), "Pi did not resume the previous conversation");
    }
    if (requests === 2) {
      assert.ok(body.messages.some(m => m.role === "tool" && JSON.stringify(m.content).includes("pi-shell-ok")));
    }
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    const delta = requests === 1 ? {
      role: "assistant",
      tool_calls: [{ index: 0, id: "call_smoke", type: "function", function: {
        name: "bash", arguments: JSON.stringify({ command: `printf pi-shell-ok | tee ${marker}` }),
      } }],
    } : { role: "assistant", content: "pi-response-ok" };
    const send = (value, finish) => res.write(`data: ${JSON.stringify({
      id: "smoke", object: "chat.completion.chunk", created: 1, model: "smoke-model",
      choices: [{ index: 0, delta: value, finish_reason: finish }],
    })}\n\n`);
    send(delta, null);
    send({}, requests === 1 ? "tool_calls" : "stop");
    res.end("data: [DONE]\n\n");
  } catch (error) {
    serverError = error;
    res.writeHead(500);
    res.end();
  }
});
await new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(0, "127.0.0.1", resolve);
});
try {
  // This file belongs only to the smoke account. The provider key is synthetic.
  await writeFile(`${state}/models.json`, JSON.stringify({ providers: {
    "yolo-smoke": {
      baseUrl: `http://127.0.0.1:${server.address().port}/v1`,
      api: "openai-completions", apiKey: "synthetic-local-fixture",
      models: [{ id: "smoke-model", contextWindow: 32000, maxTokens: 4096 }],
    },
  } }));
  const child = spawn("pi", [
    "--provider", "yolo-smoke", "--model", "smoke-model", "--no-extensions",
    "--tools", "bash", "--mode", "json", "--session", session,
    "-p", "Execute the requested shell tool, then stop.",
  ], {
    env: { ...process.env, PI_CODING_AGENT_DIR: state, PI_OFFLINE: "1", PI_TELEMETRY: "0", NO_PROXY: "127.0.0.1,localhost" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const stdout = [];
  const stderr = [];
  child.stdout.on("data", data => stdout.push(data));
  child.stderr.on("data", data => stderr.push(data));
  const timeout = setTimeout(() => child.kill("SIGKILL"), 45000);
  try {
    const status = await new Promise((resolve, reject) => {
      child.once("error", reject);
      child.once("exit", resolve);
    });
    assert.ifError(serverError);
    assert.equal(status, 0, `Pi exited unsuccessfully: ${JSON.stringify(Buffer.concat(stderr).toString())}`);
    assert.equal(requests, 2);
    assert.ok(sawSkill, "operator skill was not included in Pi's model context");
    assert.equal(await readFile(marker, "utf8"), "pi-shell-ok");
    assert.ok(Buffer.concat(stdout).toString().includes("pi-response-ok"));
    const history = await readFile(session, "utf8");
    assert.ok(history.includes("pi-response-ok"), "Pi did not save its actual session");
    assert.ok(history.length > previousHistory.length, "Pi did not append to the saved session");
    console.log("PASS: real Pi CLI, shell tool without PTYs, operator skill discovery and saved session (local model fixture)");
  } finally {
    clearTimeout(timeout);
    child.kill("SIGKILL");
  }
} finally {
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
}
