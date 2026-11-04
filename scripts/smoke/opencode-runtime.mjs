// Run inside a disposable OpenShell box with OpenCode and yolo.space.operator.
// A deterministic loopback provider exercises the real CLI without real keys.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, readFile, stat, writeFile } from "node:fs/promises";
import { createServer } from "node:http";

const state = "/home/sandbox/.local/share/opencode";
const marker = "/workdir/opencode-shell-smoke";
const binary = process.env.YOLO_SMOKE_OPENCODE || "opencode";
await mkdir(state, { recursive: true });
// Loading this controlled plugin requires OpenCode to download its SDK from
// npm, including the scoped-package metadata URL. The hook proves it executed.
const config = `${state}/config/opencode`;
await mkdir(`${config}/plugins`, { recursive: true });
await writeFile(`${config}/plugins/yoloctl-smoke.js`, `
import { tool } from "@opencode-ai/plugin";
export default async () => {
  if (typeof tool !== "function") throw new Error("plugin SDK was not loaded");
  return {
    "experimental.chat.system.transform": async (_input, output) => {
      output.system.push("yoloctl-plugin-ok");
    },
  };
};
`);
let previousSession;
try {
  previousSession = await readFile(`${state}/yoloctl-smoke-session`, "utf8");
} catch (error) {
  if (error.code !== "ENOENT") throw error;
}
let requests = 0;
let sawSkill = false;
let sawPlugin = false;
let serverError;
const server = createServer(async (req, res) => {
  try {
    assert.equal(req.url, "/v1/chat/completions");
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = JSON.parse(Buffer.concat(chunks));
    sawSkill ||= JSON.stringify(body).includes("yolo-box-operator");
    sawPlugin ||= body.messages.some(m => m.role === "system" && JSON.stringify(m.content).includes("yoloctl-plugin-ok"));
    requests++;
    assert.ok(requests <= 2, "unexpected extra model request");
    if (requests === 1 && previousSession) {
      assert.ok(JSON.stringify(body.messages).includes("opencode-response-ok"), "previous session was not resumed");
    }
    if (requests === 2) {
      assert.ok(body.messages.some(m => m.role === "tool" && JSON.stringify(m.content).includes("opencode-shell-ok")));
    }
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    const delta = requests === 1 ? {
      role: "assistant",
      tool_calls: [{ index: 0, id: `call_smoke_${Date.now()}`, type: "function", function: {
        name: "bash", arguments: JSON.stringify({ command: `printf opencode-shell-ok | tee ${marker}`, description: "Write the smoke marker" }),
      } }],
    } : { role: "assistant", content: "opencode-response-ok" };
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
const env = {
  ...process.env,
  XDG_DATA_HOME: "/home/sandbox/.local/share",
  XDG_CONFIG_HOME: `${state}/config`, XDG_CACHE_HOME: `${state}/cache`, XDG_STATE_HOME: `${state}/state`,
  NPM_CONFIG_CACHE: `${state}/cache/npm`,
  OPENCODE_DISABLE_AUTOUPDATE: "true", OPENCODE_DISABLE_MODELS_FETCH: "true", OPENCODE_DISABLE_LSP_DOWNLOAD: "true",
  NO_PROXY: "localhost,127.0.0.1,::1",
  OPENCODE_CONFIG_CONTENT: JSON.stringify({ share: "disabled", compaction: { auto: false }, provider: {
    "yolo-smoke": {
      npm: "@ai-sdk/openai-compatible", name: "Local smoke provider",
      options: { baseURL: `http://127.0.0.1:${server.address().port}/v1`, apiKey: "synthetic-local-fixture" },
      models: { "smoke-model": { name: "Smoke model", limit: { context: 32000, output: 4096 } } },
    },
  } }),
};
async function run(args) {
  const child = spawn(binary, args, { env, stdio: ["ignore", "pipe", "pipe"] });
  const stdout = [], stderr = [];
  child.stdout.on("data", data => stdout.push(data));
  child.stderr.on("data", data => stderr.push(data));
  const timeout = setTimeout(() => child.kill("SIGKILL"), 120000);
  try {
    const status = await new Promise((resolve, reject) => {
      child.once("error", reject);
      child.once("exit", resolve);
    });
    assert.ifError(serverError);
    assert.equal(status, 0, `OpenCode exited unsuccessfully: ${JSON.stringify(Buffer.concat(stderr).toString())}`);
    return Buffer.concat(stdout).toString();
  } finally {
    clearTimeout(timeout);
    child.kill("SIGKILL");
  }
}
try {
  const output = await run(["run", "--auto", "--format", "json", "--model", "yolo-smoke/smoke-model", "--title", "yoloctl smoke",
    ...(previousSession ? ["--session", previousSession] : []), "Execute the requested shell tool, then stop."]);
  assert.equal(requests, 2, output);
  assert.ok(sawSkill, "operator skill was not advertised to the model");
  assert.ok(sawPlugin, "plugin SDK installation or plugin execution failed");
  const sdk = JSON.parse(await readFile(`${config}/node_modules/@opencode-ai/plugin/package.json`, "utf8"));
  assert.equal(sdk.name, "@opencode-ai/plugin");
  assert.ok((await stat(`${state}/cache/npm/_cacache`)).isDirectory(), "npm cache escaped the account leaf");
  assert.equal(await readFile(marker, "utf8"), "opencode-shell-ok");
  const events = output.trim().split("\n").map(line => JSON.parse(line));
  assert.ok(events.some(event => event.type === "text" && event.part.text.includes("opencode-response-ok")), output);
  const session = events.find(event => event.sessionID)?.sessionID;
  assert.ok(session, "missing session ID");
  if (previousSession) assert.equal(session, previousSession);
  const history = JSON.parse(await run(["export", session]));
  assert.ok(JSON.stringify(history).includes("opencode-response-ok"), "session export lost the response");
  await writeFile(`${state}/yoloctl-smoke-session`, session);
  console.log(`PASS: real OpenCode CLI, installed plugin SDK ${sdk.version}, plugin hook, shell tool without PTYs, operator skill discovery and persistent session (local model fixture)`);
} finally {
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
}
