"use strict";
const { test } = require("node:test");
const assert = require("node:assert/strict");
const vm = require("node:vm");
const fs = require("node:fs");
const script = fs.readFileSync(__dirname + "/app.js", "utf8");
class Element {
  constructor() {
    this.value = "";
    this.checked = false;
    this.disabled = false;
    this.children = [];
    this.dataset = {};
    this.textContent = "";
    this.open = false;
  }
  append(...items) {
    this.children.push(...items);
  }
  replaceChildren(...items) {
    this.children = [...items];
  }
  setAttribute() {}
  addEventListener(name, fn) {
    this["on" + name] = fn;
  }
  querySelectorAll() {
    return [];
  }
  showModal() {
    this.open = true;
    this.modalCount = (this.modalCount || 0) + 1;
  }
}
function setup({
  permission = "granted",
  slots = [],
  storage = new Map(),
  failConfig = false,
} = {}) {
  const elements = new Map();
  const $ = (id) => {
    if (!elements.has(id)) elements.set(id, new Element());
    return elements.get(id);
  };
  const sent = [],
    notifications = [],
    timers = [];
  const config = {
    enabled: true,
    priority: 7,
    store: { version: "0.2.0" },
    custom: "preserve",
    model: "gpt-6-luna",
    reasoning_effort: "low",
    timezone: "Asia/Singapore",
    max_retries: 3,
    max_log_entries: 200,
    prompt: "OK",
    schedules: [],
    auth_ids: [],
  };
  function Notification(title, options) {
    notifications.push({ title, ...options });
  }
  Notification.permission = permission;
  Notification.requestPermission = async () => permission;
  const context = {
    document: {
      getElementById: $,
      createElement: () => new Element(),
      createTextNode: (t) => t,
    },
    localStorage: {
      getItem: (k) => storage.get(k),
      setItem: (k, v) => storage.set(k, v),
    },
    Notification,
    window: { Notification, addEventListener() {} },
    setInterval: (fn) => timers.push(fn),
    setTimeout: (fn) => timers.push(fn),
    fetch: async (url, options) => {
      sent.push({ url, options });
      const isConfig = url.endsWith("/config");
      const data = isConfig
        ? config
        : url.endsWith("/logs")
          ? { logs: [] }
          : { enabled: true, config, auths: [], schedules: [], slots };
      return {
        ok: !(isConfig && failConfig),
        status: failConfig ? 401 : 200,
        json: async () => data,
      };
    },
    console,
  };
  vm.runInNewContext(script, context);
  $("key").value = "test-management-key";
  return { $, sent, notifications, timers, storage };
}
async function settle() {
  for (let i = 0; i < 8; i++)
    await new Promise((resolve) => setImmediate(resolve));
}
test("config save preserves host fields and omits legacy times", async () => {
  const x = setup();
  x.$("connect").onclick();
  await settle();
  assert.equal(x.$("fields").disabled, false);
  x.$("retries").value = "0";
  await x.$("settings").onsubmit({ preventDefault() {} });
  const save = x.sent.find((s) => s.options.method === "PUT");
  assert.ok(save);
  const body = JSON.parse(save.options.body);
  assert.equal(body.priority, 7);
  assert.equal(body.store.version, "0.2.0");
  assert.equal(body.custom, "preserve");
  assert.equal(body.max_retries, 0);
  assert.ok(!("times" in body));
  assert.ok(!JSON.stringify(body).includes("test-management-key"));
});
test("terminal failure dialog and notification are deduplicated after reload", async () => {
  const slots = [
    { key: "task-1", auth_id: "a", status: "failed", last_error: "http_500" },
  ];
  const first = setup({ slots });
  first.$("connect").onclick();
  await settle();
  assert.equal(first.$("failure").modalCount, 1);
  assert.equal(first.notifications.length, 1);
  first.timers[0]();
  await settle();
  assert.equal(first.notifications.length, 1);
  const again = setup({ slots, storage: first.storage });
  again.$("connect").onclick();
  await settle();
  assert.equal(again.notifications.length, 0);
  assert.ok(!again.$("failure").open);
});
test("denied notifications still show dialog; intermediate failure never notifies", async () => {
  const failed = setup({
    permission: "denied",
    slots: [{ key: "failed", auth_id: "a", status: "failed" }],
  });
  failed.$("connect").onclick();
  await settle();
  assert.ok(failed.$("failure").open);
  assert.equal(failed.notifications.length, 0);
  await failed.$("notify").onclick();
  assert.match(failed.$("notice").textContent, /未获通知权限/);
  const pending = setup({
    slots: [{ key: "pending", status: "retry_pending" }],
  });
  pending.$("connect").onclick();
  await settle();
  assert.equal(pending.notifications.length, 0);
  assert.ok(!pending.$("failure").open);
});
test("config authentication failure leaves editing disabled", async () => {
  const x = setup({ failConfig: true });
  x.$("fields").disabled = true;
  x.$("connect").onclick();
  await settle();
  assert.equal(x.$("fields").disabled, true);
  assert.match(x.$("notice").textContent, /管理密钥无效/);
});
