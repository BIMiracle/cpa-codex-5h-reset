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
  parentTheme,
  liveSession,
  noSession = false,
  quotaAuths = [],
  quotas = {},
  configOverrides = {},
  now = "2026-10-02T01:00:00Z",
  failImport = false,
  beforeHolidayPreview = async () => {},
} = {}) {
  if (!noSession && !storage.has("cli-proxy-auth")) {
    storage.set("cli-proxy-auth", JSON.stringify({ state: {
      managementKey: "test-management-key", apiBase: "http://localhost:8317",
    } }));
  }
  const elements = new Map();
  const $ = (id) => {
    if (!elements.has(id)) elements.set(id, new Element());
    return elements.get(id);
  };
  const sent = [],
    notifications = [],
    timers = [], events = {}, observers = [];
  const config = {
    enabled: true,
    priority: 7,
    store: { version: "0.2.0" },
    custom: "preserve",
    quota_routing: {
      enabled: true,
      session_ttl: "4h",
      bindings: [{ key: "dedicated-key", allow: ["a.json"] }],
      unbound: "deny",
    },
    model: "gpt-6-luna",
    reasoning_effort: "low",
    timezone: "Asia/Singapore",
    max_retries: 3,
    max_log_entries: 200,
    prompt: "OK",
    schedules: [],
    auth_ids: [],
    ...configOverrides,
  };
  const holidayCalendar = {
    timezone: config.timezone, now, refreshing: false,
    years: [{ year: 2026, source: "https://timor.tech/api/holiday/year/2026/", known: true, manual: false, off_days: 1, workdays: 1, updated_at: now }],
  };
  function calendarFor(timezone = config.timezone) {
    const parts = new Intl.DateTimeFormat("en", { timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(new Date(now));
    const part = (type) => parts.find((p) => p.type === type).value;
    const today = new Date(part("year") + "-" + part("month") + "-" + part("day") + "T12:00:00Z");
    const gridStart = (offset) => {
      const first = new Date(today);
      first.setUTCDate(1);
      first.setUTCMonth(first.getUTCMonth() + offset);
      first.setUTCDate(1 - ((first.getUTCDay() + 6) % 7));
      return first;
    };
    const day = (date) => {
      const stamp = date.toISOString().slice(0, 10), weekend = [0, 6].includes(date.getUTCDay());
      const holiday = stamp === "2026-09-25" || stamp === "2026-10-01";
      return { date: stamp, name: holiday ? "节假日" : stamp === "2026-10-10" ? "国庆节" : weekend ? "普通周末" : "工作日",
        kind: holiday ? "holiday" : stamp === "2026-10-10" ? "workday" : weekend ? "weekend" : "weekday", data_known: true };
    };
    const days = Array.from({ length: 30 }, (_, i) => {
      const date = new Date(today);
      date.setUTCDate(date.getUTCDate() + i);
      return day(date);
    });
    const display_days = [], end = gridStart(1);
    end.setUTCDate(end.getUTCDate() + 42);
    for (const date = gridStart(-1); date < end; date.setUTCDate(date.getUTCDate() + 1)) display_days.push(day(date));
    return { ...holidayCalendar, timezone, now, days, display_days };
  }
  function Notification(title, options) {
    notifications.push({ title, ...options });
  }
  Notification.permission = permission;
  Notification.requestPermission = async () => permission;
  const context = {
    document: {
      documentElement: { dataset: {} },
      addEventListener: (_name, callback) => callback(),
      getElementById: $,
      createElement: () => new Element(),
      createTextNode: (t) => t,
    },
    localStorage: {
      getItem: (k) => storage.get(k),
      setItem: (k, v) => storage.set(k, v),
    },
    TextEncoder, TextDecoder, Uint8Array, atob, URL,
    navigator: { userAgent: "test-browser" },
    MutationObserver: class {
      constructor(fn) { observers.push(fn); }
      observe() {}
    },
    Notification,
    window: {
      Notification,
      location: { host: "localhost:8317", origin: "http://localhost:8317", href: "http://localhost:8317/v0/resource/plugins/cpa-codex-5h-reset/ui" },
      addEventListener(name, fn) { (events[name] ||= []).push(fn); },
      matchMedia: () => ({ matches: false, addEventListener() {} }),
    },
    setInterval: (fn) => timers.push(fn),
    setTimeout: (fn) => timers.push(fn),
    fetch: async (url, options) => {
      sent.push({ url, options });
      const isConfig = url.endsWith("/config");
      const isImport = url.endsWith("/holidays/import");
      if (isImport && JSON.parse(options.body).preview) await beforeHolidayPreview();
      if (isImport && !failImport && !JSON.parse(options.body).preview) holidayCalendar.years[0].manual = true;
      const data = isConfig
        ? config
        : url.endsWith("/account-quotas")
          ? { auths: quotaAuths, quotas }
        : url.endsWith("/logs")
          ? { logs: [] }
          : isImport ? failImport ? { error: "invalid holiday JSON" } : { year: 2026, off_days: 1, workdays: 1 }
          : url.includes("/holidays") ? calendarFor(new URL(url, "http://localhost:8317").searchParams.get("timezone") || config.timezone)
          : { enabled: true, config, auths: [], schedules: [], slots, now, holiday_status: calendarFor() };
      return {
        ok: !(isConfig && failConfig) && !(isImport && failImport),
        status: failConfig ? 401 : isImport && failImport ? 400 : 200,
        json: async () => data,
      };
    },
    console,
  };
  context.window.parent = context.window;
  if (parentTheme || liveSession) {
    context.window.parent = { document: { documentElement: { getAttribute: () => parentTheme } } };
  }
  if (liveSession) {
    function checkAuth() { return liveSession.managementKey && liveSession.apiBase; }
    const values = [liveSession.authenticated, liveSession.managementKey, liveSession.apiBase, checkAuth];
    let hook = null;
    for (const value of values.reverse()) hook = { queue: { getSnapshot: () => value }, next: hook };
    context.window.frameElement = { "__reactFiber$test": { return: { memoizedState: hook } } };
  }
  vm.runInNewContext(script, context);
  return { $, sent, notifications, timers, storage, elements, events, observers,
    setParentTheme: (theme) => { parentTheme = theme; observers.forEach((fn) => fn()); },
    root: context.document.documentElement, setNow: (value) => { now = value; } };
}
test("theme always follows CPA and ignores the removed plugin preference", () => {
  const storage = new Map([
    ["cli-proxy-theme", JSON.stringify({ state: { theme: "white" } })],
    ["cpa-5h-reset-theme-v1", "dark"],
  ]);
  const x = setup({ storage });
  assert.equal(x.root.dataset.theme, "white");
  assert.equal(x.elements.has("theme"), false);
  const embedded = setup({ storage, parentTheme: "dark" });
  for (const theme of ["white", "light", "dark"]) {
    embedded.setParentTheme(theme);
    assert.equal(embedded.root.dataset.theme, theme);
  }
  storage.set("cli-proxy-theme", JSON.stringify({ state: { theme: "dark" } }));
  x.events.storage.forEach((fn) => fn({ key: "cli-proxy-theme" }));
  assert.equal(x.root.dataset.theme, "dark");
  storage.set("cli-proxy-theme", JSON.stringify({ state: { theme: "auto" } }));
  x.events.storage.forEach((fn) => fn({ key: "cli-proxy-theme" }));
  assert.equal(x.root.dataset.theme, "light");
});
async function settle() {
  for (let i = 0; i < 8; i++)
    await new Promise((resolve) => setImmediate(resolve));
}
test("automatically reads CPA obfuscated credentials without a key field", async () => {
  const state = JSON.stringify({ state: { managementKey: "记住的-key", apiBase: "http://localhost:8317" } });
  const mask = new TextEncoder().encode("cli-proxy-api-webui::secure-storage|localhost:8317|test-browser");
  const encoded = Buffer.from(new TextEncoder().encode(state).map((b, i) => b ^ mask[i % mask.length])).toString("base64");
  const storage = new Map([["cli-proxy-auth", "enc::v1::" + encoded]]);
  const x = setup({ storage });
  await settle();
  assert.equal(x.$("fields").disabled, false);
  assert.equal(x.elements.has("key"), false);
  assert.ok(x.sent.some((s) => s.url.endsWith("/account-quotas")));
  assert.ok(x.sent.every((s) => s.options.headers.Authorization === "Bearer 记住的-key"));
  assert.equal(storage.size, 1);
});
test("uses the live memory-only session instead of a stale saved key", async () => {
  const x = setup({ liveSession: { authenticated: true, managementKey: "memory-key", apiBase: "http://localhost:8317" } });
  await settle();
  assert.ok(x.sent.length > 0);
  assert.ok(x.sent.every((s) => s.options.headers.Authorization === "Bearer memory-key"));
});
test("missing or logged-out sessions cannot send authenticated requests", async () => {
  for (const options of [
    { noSession: true },
    { liveSession: { authenticated: false, managementKey: "", apiBase: "" } },
  ]) {
    const x = setup(options);
    await settle();
    assert.equal(x.sent.length, 0);
    assert.match(x.$("notice").textContent, /重新登录/);
  }
});
test("does not forward the management key to a stored remote URL", async () => {
  const x = setup({ storage: new Map([["cli-proxy-auth", JSON.stringify({ state: {
    managementKey: "secret", apiBase: "https://other.example",
  } })]]) });
  await settle();
  assert.equal(x.sent.length, 0);
});
test("shows queried five-hour quota before any wake task exists", async () => {
  const x = setup({ quotaAuths: [{ id: "a", provider: "codex", plan_type: "plus", label: "Account A" }],
    quotas: { a: { known: true, used_percent: 42, observed_at: "2026-10-01T00:00:00Z", reset_at: "2026-10-01T05:00:00Z" } } });
  await settle();
  const row = x.$("accounts").children[0];
  assert.equal(row.children[0].textContent, "Account A");
  assert.equal(row.children[1].textContent, "Plus");
  assert.equal(row.children[2].textContent, "58%");
  assert.equal(row.children[6].textContent, "尚未执行");
  assert.ok(x.sent.every((s) => !s.options.method));
});
test("shows weekly remaining for Business without inventing a five-hour window", async () => {
  const x = setup({ quotaAuths: [
    { id: "business", provider: "codex", plan_type: "self_serve_business_prolite" },
    { id: "plus", provider: "codex", plan_type: "plus" },
  ], quotas: {
    business: { known: false, observed_at: "2026-10-27T00:00:00Z", weekly: { used_percent: 32, reset_at: "2026-11-01T00:00:00Z" } },
    plus: { known: true, used_percent: 6, observed_at: "2026-10-27T00:00:00Z", reset_at: "2026-10-27T05:00:00Z",
      weekly: { used_percent: 2, reset_at: "2026-11-01T00:00:00Z" } },
  } });
  await settle();
  const rows = x.$("accounts").children;
  assert.equal(rows[0].children[2].textContent, "未提供");
  assert.equal(rows[0].children[3].textContent, "—");
  assert.equal(rows[0].children[4].textContent, "68%");
  assert.equal(rows[1].children[2].textContent, "94%");
  assert.equal(rows[1].children[4].textContent, "98%");
});
test("only Plus accounts can be selected or woken", async () => {
  const x = setup({ quotaAuths: [
    { id: "plus", provider: "codex", plan_type: " PLUS " },
    { id: "premium", provider: "codex", plan_type: "business_premium" },
    { id: "unknown", provider: "codex" },
    { id: "pro", provider: "codex", plan_type: "pro" },
    { id: "disabled", provider: "codex", plan_type: "plus", disabled: true },
  ] });
  await settle();
  const rows = x.$("accounts").children;
  assert.equal(rows.length, 5);
  assert.equal(rows[1].children[1].textContent, "Business Premium");
  assert.match(rows[1].children[6].textContent, /不参与唤醒/);
  assert.match(rows[2].children[6].textContent, /套餐未知/);
  assert.equal(rows[0].children[9].children[0].disabled, false);
  for (const row of rows.slice(1)) assert.equal(row.children[9].children[0].disabled, true);
  assert.equal(x.$("run-all").disabled, false);
  assert.equal(x.$("auth-select").children.length, 1);
  assert.equal(x.$("auth-select").children[0].children[0].value, "plus");
  await rows[0].children[9].children[0].onclick();
  const runs = x.sent.filter((s) => s.url.endsWith("/run"));
  assert.equal(runs.length, 1);
  assert.equal(JSON.parse(runs[0].options.body).auth_id, "plus");
});

test("wake-all is disabled without an eligible selected Plus account", async () => {
  for (const options of [
    { quotaAuths: [{ id: "premium", provider: "codex", plan_type: "Business Premium" }] },
    { quotaAuths: [{ id: "unknown", provider: "codex" }] },
    { quotaAuths: [{ id: "plus", provider: "codex", plan_type: "plus" }], configOverrides: { auth_ids: ["premium"] } },
  ]) {
    const x = setup(options);
    await settle();
    assert.equal(x.$("run-all").disabled, true);
    assert.equal(x.$("accounts").children[0].children[9].children[0].disabled, true);
  }
});

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
  assert.match(x.$("notice").textContent, /登录已失效/);
});

test("timezone select defaults to Singapore and preserves an existing alias", async () => {
  const normal = setup();
  await settle();
  assert.equal(normal.$("timezone").value, "Asia/Singapore");
  assert.ok(normal.$("timezone").children.some((option) => option.value === "Asia/Shanghai"));
  const alias = setup({ configOverrides: { timezone: "US/Eastern" } });
  await settle();
  assert.equal(alias.$("timezone").value, "US/Eastern");
  assert.ok(alias.$("timezone").children.some((option) => option.value === "US/Eastern"));
});

test("calendar selects an inclusive range and tomorrow wake is mutually exclusive", async () => {
  const x = setup();
  await settle();
  assert.equal(x.$("calendar").children.length, 42);
  x.$("rule-exclude").onchange();
  x.$("calendar").children.find((day) => day.dataset.date === "2026-10-04").onclick();
  x.$("calendar").children.find((day) => day.dataset.date === "2026-10-08").onclick();
  assert.equal(x.$("exclude-start").value, "2026-10-04");
  assert.equal(x.$("exclude-end").value, "2026-10-08");
  assert.equal(x.$("exclude-end").max, "2026-10-31");
  assert.equal(x.$("calendar").children.filter((day) => day.className.includes("selected")).length, 5);
  x.$("skip-holidays").checked = true;
  await x.$("settings").onsubmit({ preventDefault() {} });
  const range = JSON.parse(x.sent.find((s) => s.options.method === "PUT").options.body);
  assert.deepEqual(range.one_time_rule, { mode: "exclude", start_date: "2026-10-04", end_date: "2026-10-08" });
  assert.equal(range.skip_cn_holidays, true);
  x.$("rule-tomorrow").onchange();
  assert.equal(x.$("rule-exclude").checked, false);
  assert.equal(x.$("rule-range").hidden, true);
  assert.equal(x.$("exclude-start").disabled, true);
  await x.$("settings").onsubmit({ preventDefault() {} });
  const wake = JSON.parse(x.sent.filter((s) => s.options.method === "PUT").at(-1).options.body);
  assert.deepEqual(wake.one_time_rule, { mode: "wake_tomorrow", start_date: "2026-10-03", end_date: "2026-10-03" });
});

test("month calendar starts on Monday with muted adjacent dates and marks today", async () => {
  const x = setup();
  await settle();
  const days = x.$("calendar").children;
  assert.equal(days.length, 42);
  assert.equal(days[0].dataset.date, "2026-09-28");
  assert.equal(days[6].dataset.date, "2026-10-04");
  assert.equal(days.at(-1).dataset.date, "2026-11-08");
  assert.ok(days[0].className.includes("adjacent"));
  assert.ok(days.at(-1).className.includes("adjacent"));
  assert.ok(days.find((day) => day.dataset.date === "2026-10-02").className.includes("today"));
  assert.equal(days[0].disabled, true);
  assert.equal(days.find((day) => day.dataset.date === "2026-10-31").disabled, false);
  assert.equal(days.find((day) => day.dataset.date === "2026-11-01").disabled, true);
  assert.match(x.$("calendar-title").textContent, /2026 年 10 月/);
});

test("previous month retains holiday labels and navigation preserves an unsaved range", async () => {
  const x = setup({ now: "2026-10-20T01:00:00Z" });
  await settle();
  x.$("calendar-prev").onclick();
  assert.match(x.$("calendar-title").textContent, /2026 年 9 月/);
  assert.equal(x.$("calendar-prev").disabled, true);
  assert.ok(x.$("calendar").children.find((day) => day.dataset.date === "2026-09-25").className.includes("holiday"));
  x.$("calendar-current").onclick();
  x.$("rule-exclude").onchange();
  x.$("calendar").children.find((day) => day.dataset.date === "2026-10-31").onclick();
  x.$("calendar-next").onclick();
  assert.match(x.$("calendar-title").textContent, /2026 年 11 月/);
  assert.equal(x.$("calendar-next").disabled, true);
  x.$("calendar").children.find((day) => day.dataset.date === "2026-11-03").onclick();
  assert.equal(x.$("exclude-start").value, "2026-10-31");
  assert.equal(x.$("exclude-end").value, "2026-11-03");
  assert.equal(x.$("calendar").children.filter((day) => day.className.includes("selected")).length, 4);
  x.$("calendar-current").onclick();
  assert.equal(x.$("exclude-end").value, "2026-11-03");
  assert.ok(!x.sent.some((s) => s.options.method === "PUT"));
});

test("month grid uses timezone boundaries and handles a Monday start and leap February", async () => {
  const x = setup({ now: "2026-10-01T01:00:00Z" });
  await settle();
  x.$("timezone").value = "America/Los_Angeles";
  await x.$("timezone").onchange();
  assert.match(x.$("calendar-title").textContent, /2026 年 9 月/);
  assert.ok(x.$("calendar").children.find((day) => day.dataset.date === "2026-09-30").className.includes("today"));
  const monday = setup({ now: "2027-02-01T01:00:00Z" });
  await settle();
  assert.equal(monday.$("calendar").children[0].dataset.date, "2027-02-01");
  assert.equal(monday.$("calendar").children[6].dataset.date, "2027-02-07");
  const leap = setup({ now: "2028-02-02T01:00:00Z" });
  await settle();
  assert.equal(leap.$("calendar").children[0].dataset.date, "2028-01-31");
  assert.ok(leap.$("calendar").children.some((day) => day.dataset.date === "2028-02-29" && !day.className.includes("adjacent")));
  const year = setup({ now: "2027-01-02T01:00:00Z" });
  await settle();
  assert.equal(year.$("calendar").children[0].dataset.date, "2026-12-28");
  year.$("calendar-prev").onclick();
  assert.match(year.$("calendar-title").textContent, /2026 年 12 月/);
});

test("invalid exclusion dates cannot be saved", async () => {
  const x = setup();
  await settle();
  x.$("rule-exclude").onchange();
  x.$("exclude-start").value = "2026-10-04";
  x.$("exclude-end").value = "2026-10-03";
  await x.$("settings").onsubmit({ preventDefault() {} });
  assert.ok(!x.sent.some((s) => s.options.method === "PUT"));
  assert.match(x.$("notice").textContent, /结束日期不能早于/);
});

test("calendar refresh follows a new month without losing the exclusion draft", async () => {
  const x = setup({ now: "2026-10-31T15:55:00Z" });
  await settle();
  x.$("rule-exclude").onchange();
  x.$("exclude-start").value = "2026-11-02";
  x.$("exclude-end").value = "2026-11-04";
  x.$("exclude-end").onchange();
  x.setNow("2026-10-31T16:05:00Z");
  x.timers.at(-1)();
  await settle();
  assert.match(x.$("calendar-title").textContent, /2026 年 11 月/);
  assert.ok(x.$("calendar").children.find((day) => day.dataset.date === "2026-11-01").className.includes("today"));
  assert.equal(x.$("exclude-start").value, "2026-11-02");
  assert.equal(x.$("exclude-end").value, "2026-11-04");
  assert.equal(x.$("calendar").children.filter((day) => day.className.includes("selected")).length, 3);
});

test("saving unrelated settings preserves a fixed wake date and clears expired rules", async () => {
  const x = setup({ configOverrides: { one_time_rule: { mode: "wake_tomorrow", start_date: "2026-10-02", end_date: "2026-10-02" } } });
  await settle();
  await x.$("settings").onsubmit({ preventDefault() {} });
  let saved = JSON.parse(x.sent.find((s) => s.options.method === "PUT").options.body);
  assert.equal(saved.one_time_rule.start_date, "2026-10-02");
  x.setNow("2026-10-02T16:00:00Z");
  x.$("connect").onclick();
  await settle();
  assert.equal(x.$("rule-none").checked, true);
  await x.$("settings").onsubmit({ preventDefault() {} });
  saved = JSON.parse(x.sent.filter((s) => s.options.method === "PUT").at(-1).options.body);
  assert.deepEqual(saved.one_time_rule, { mode: "none" });
});

test("tomorrow is computed from fresh server time and the selected timezone", async () => {
  const x = setup();
  await settle();
  x.$("rule-tomorrow").onchange();
  x.setNow("2026-10-02T16:00:00Z");
  await x.$("settings").onsubmit({ preventDefault() {} });
  const saved = JSON.parse(x.sent.find((s) => s.options.method === "PUT").options.body);
  assert.equal(saved.one_time_rule.start_date, "2026-10-04");
  const y = setup({ now: "2026-10-02T01:00:00Z" });
  await settle();
  y.$("timezone").value = "America/Los_Angeles";
  await y.$("timezone").onchange();
  assert.match(y.$("calendar-title").textContent, /America\/Los_Angeles/);
  assert.ok(y.sent.some((s) => s.url.includes("/holidays?timezone=America%2FLos_Angeles")));
  y.$("rule-tomorrow").onchange();
  await y.$("settings").onsubmit({ preventDefault() {} });
  assert.equal(JSON.parse(y.sent.find((s) => s.options.method === "PUT").options.body).one_time_rule.start_date, "2026-10-02");
});

test("active exclusion intervals can be preserved after their start date passes", async () => {
  const x = setup({ configOverrides: { one_time_rule: { mode: "exclude", start_date: "2026-10-01", end_date: "2026-10-04" } } });
  await settle();
  assert.equal(x.$("exclude-start").min, "2026-10-01");
  await x.$("settings").onsubmit({ preventDefault() {} });
  const saved = JSON.parse(x.sent.find((s) => s.options.method === "PUT").options.body);
  assert.equal(saved.one_time_rule.start_date, "2026-10-01");
});

test("holiday import previews before applying and rejects failed previews", async () => {
  const json = { year: 2026, days: [{ date: "2026-10-01", name: "国庆", isOffDay: true }] };
  const x = setup();
  await settle();
  x.$("holiday-file").files = [{ size: 100, text: async () => JSON.stringify(json) }];
  await x.$("holiday-preview").onclick();
  let imports = x.sent.filter((s) => s.url.endsWith("/holidays/import"));
  assert.equal(imports.length, 1);
  assert.equal(JSON.parse(imports[0].options.body).preview, true);
  assert.match(x.$("import-preview").textContent, /2026 年：放假 1 天/);
  assert.equal(x.$("holiday-import").disabled, false);
  await x.$("holiday-import").onclick();
  imports = x.sent.filter((s) => s.url.endsWith("/holidays/import"));
  assert.equal(JSON.parse(imports[1].options.body).preview, false);
  assert.equal(x.$("holiday-import").disabled, true);
  assert.ok(x.$("holiday-meta").children[0].children.some((child) => typeof child === "string" && child.includes("手动导入")));
  assert.ok(x.sent.every((s) => s.url.startsWith("/v0/management/plugins/")));
  const bad = setup({ failImport: true });
  await settle();
  bad.$("holiday-file").files = [{ size: 100, text: async () => JSON.stringify(json) }];
  await bad.$("holiday-preview").onclick();
  assert.equal(bad.$("holiday-import").disabled, true);
  assert.match(bad.$("import-preview").textContent, /导入预览失败/);
});

test("holiday confirmation stays disabled until this file has a successful preview", async () => {
  const x = setup();
  await settle();
  assert.equal(x.$("holiday-import").disabled, true);
  await x.$("holiday-import").onclick();
  assert.ok(!x.sent.some((s) => s.url.endsWith("/holidays/import")));
  await x.$("holiday-preview").onclick();
  assert.match(x.$("import-preview").textContent, /请选择/);
  assert.equal(x.$("holiday-import").disabled, true);
  const file = { size: 100, text: async () => JSON.stringify({ year: 2026, days: [{ date: "2026-10-01", name: "国庆", isOffDay: true }] }) };
  x.$("holiday-file").files = [file];
  x.$("holiday-file").onchange();
  await x.$("holiday-import").onclick();
  assert.ok(!x.sent.some((s) => s.url.endsWith("/holidays/import")));
  await x.$("holiday-preview").onclick();
  assert.equal(x.$("holiday-import").disabled, false);
  x.$("holiday-file").files = [{ size: 10, text: async () => "invalid json" }];
  x.$("holiday-file").onchange();
  assert.equal(x.$("holiday-import").disabled, true);
  assert.equal(x.$("import-preview").textContent, "");
  await x.$("holiday-import").onclick();
  await x.$("holiday-preview").onclick();
  assert.equal(x.$("holiday-import").disabled, true);
  assert.match(x.$("import-preview").textContent, /导入预览失败/);
  assert.ok(x.sent.filter((s) => s.url.endsWith("/holidays/import")).every((s) => JSON.parse(s.options.body).preview));
});

test("an old preview response cannot enable confirmation after a file change", async () => {
  let finish;
  const pending = new Promise((resolve) => { finish = resolve; });
  const x = setup({ beforeHolidayPreview: () => pending });
  await settle();
  x.$("holiday-file").files = [{ size: 100, text: async () => JSON.stringify({ year: 2026, days: [{ date: "2026-10-01", name: "国庆", isOffDay: true }] }) }];
  const preview = x.$("holiday-preview").onclick();
  await settle();
  assert.equal(x.$("holiday-import").disabled, true);
  assert.equal(x.sent.filter((s) => s.url.endsWith("/holidays/import")).length, 1);
  x.$("holiday-file").files = [{ size: 10, text: async () => "{}" }];
  x.$("holiday-file").onchange();
  finish();
  await preview;
  assert.equal(x.$("holiday-import").disabled, true);
  assert.equal(x.$("holiday-preview").disabled, false);
  assert.equal(x.$("import-preview").textContent, "");
  await x.$("holiday-import").onclick();
  assert.ok(x.sent.filter((s) => s.url.endsWith("/holidays/import")).every((s) => JSON.parse(s.options.body).preview));
});

test("skipped automatic tasks do not open failure notifications", async () => {
  const x = setup({ slots: [{ key: "skip", auth_id: "a", status: "skipped", last_error: "weekend" }] });
  await settle();
  assert.equal(x.notifications.length, 0);
  assert.equal(x.$("failure").open, false);
});

test("quota routing toggle preserves key isolation and TTL settings", async () => {
  const x = setup();
  x.$("connect").onclick();
  await settle();
  assert.equal(x.$("quota-routing").checked, true);
  x.$("quota-routing").checked = false;
  await x.$("settings").onsubmit({ preventDefault() {} });
  const save = x.sent.find((s) => s.options.method === "PUT");
  const routing = JSON.parse(save.options.body).quota_routing;
  assert.equal(routing.enabled, false);
  assert.equal(routing.session_ttl, "4h");
  assert.equal(routing.unbound, "deny");
  assert.deepEqual(routing.bindings, [{ key: "dedicated-key", allow: ["a.json"] }]);
});
