"use strict";
(() => {
  const themes = ["white", "light", "dark"];
  const root = document.documentElement;
  const system = window.matchMedia("(prefers-color-scheme: dark)");
  function managementTheme() {
    try {
      if (window.parent !== window) {
        const theme = window.parent.document.documentElement.getAttribute("data-theme");
        if (themes.includes(theme)) return theme;
      }
    } catch {}
    try {
      const saved = JSON.parse(localStorage.getItem("cli-proxy-theme") || "null");
      const theme = saved?.state?.theme;
      if (themes.includes(theme)) return theme;
    } catch {}
    return system.matches ? "dark" : "light";
  }
  function applyTheme() {
    root.dataset.theme = managementTheme();
  }
  applyTheme();
  window.addEventListener("storage", (event) => {
    if (event.key === "cli-proxy-theme" || event.key === null) applyTheme();
  });
  window.addEventListener("focus", applyTheme);
  system.addEventListener("change", applyTheme);
  try {
    if (window.parent !== window) {
      new MutationObserver(applyTheme).observe(window.parent.document.documentElement, {
        attributes: true,
        attributeFilter: ["data-theme"],
      });
    }
  } catch {}
})();
// CPA currently provides no public iframe session bridge. Read its same-origin
// React auth guard's live external-store snapshots so memory-only logins work.
// Keep this compatibility adapter isolated; never persist or display credentials.
function managementSession() {
  try {
    if (window.parent !== window) {
      const frame = window.frameElement;
      const fiberKey = frame && Object.keys(frame).find((k) => k.startsWith("__reactFiber$"));
      for (let fiber = fiberKey && frame[fiberKey]; fiber; fiber = fiber.return) {
        const snapshots = [];
        for (let hook = fiber.memoizedState; hook; hook = hook.next) {
          if (typeof hook.queue?.getSnapshot === "function") {
            snapshots.push(hook.queue.getSnapshot());
          }
        }
        for (let i = 0; i + 3 < snapshots.length; i++) {
          const [authenticated, managementKey, apiBase, checkAuth] = snapshots.slice(i, i + 4);
          if (typeof authenticated === "boolean" && typeof managementKey === "string" &&
              typeof apiBase === "string" && typeof checkAuth === "function" &&
              /managementKey/.test(Function.prototype.toString.call(checkAuth)) &&
              /apiBase/.test(Function.prototype.toString.call(checkAuth))) {
            return { managementKey: authenticated ? managementKey : "", apiBase };
          }
        }
      }
    }
  } catch {}
  // Standalone pages and older hosts can reuse CPA's remembered login.
  function stored(name) {
    try {
      let value = localStorage.getItem(name);
      if (value?.startsWith("enc::v1::")) {
        const key = new TextEncoder().encode(
          "cli-proxy-api-webui::secure-storage|" + window.location.host + "|" + navigator.userAgent,
        );
        const bytes = Uint8Array.from(atob(value.slice(9)), (c, i) => c.charCodeAt(0) ^ key[i % key.length]);
        value = new TextDecoder().decode(bytes);
      }
      try { return JSON.parse(value); } catch { return value; }
    } catch { return null; }
  }
  const state = stored("cli-proxy-auth")?.state;
  return {
    managementKey: state?.managementKey || stored("managementKey") || "",
    apiBase: state?.apiBase || "",
  };
}
(() => {
  const $ = (id) => document.getElementById(id),
    base = "/v0/management/plugins/cpa-codex-5h-reset";
  let configuration = {},
    connected = false,
    polling = false,
    auths = [],
    modelConfig = {},
    quotas = {},
    quotaAuths = null,
    quotaUpdatedAt = 0,
    serverNow = new Date().toISOString(),
    serverReceivedAt = Date.now(),
    calendar = null,
    calendarMonth = "",
    calendarAnchor = "",
    calendarTimezone = "",
    loadedRule = { mode: "none" },
    ruleTouched = false,
    selectingEnd = false,
    pendingImport = null,
    importRevision = 0;
  const states = {
    pending: "排队中",
    requesting: "正在请求",
    retry_pending: "等待重试",
    wait_for_reset: "等待五小时重置",
    fresh_window: "请求成功 · 新窗口信号",
    request_ok_reset_unknown: "请求成功 · 重置未知",
    failed: "重试结束 · 失败",
    cancelled: "已取消",
    skipped: "自动任务已跳过",
  };
  const reasons = { holiday: "法定节假日", weekend: "普通周末", excluded_date: "一次性排除日期" };
  const seenKey = "cpa-5h-reset-notified-v1";
  let seen = new Set();
  try {
    seen = new Set(JSON.parse(localStorage.getItem(seenKey) || "[]"));
  } catch {}
  function notice(text, error = false) {
    $("notice").textContent = text;
    $("notice").className = error ? "error" : "";
  }
  const format = (value) =>
    !value || value.startsWith("0001-")
      ? "—"
      : new Date(value).toLocaleString("zh-CN", {
          timeZone: modelConfig.timezone || "Asia/Singapore",
          hour12: false,
        });
  function currentTime() {
    return new Date(new Date(serverNow).getTime() + Date.now() - serverReceivedAt);
  }
  function dateInZone(at, timezone = $("timezone").value || "Asia/Singapore") {
    const parts = new Intl.DateTimeFormat("en", {
      timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit",
    }).formatToParts(at);
    const part = (type) => parts.find((p) => p.type === type).value;
    return part("year") + "-" + part("month") + "-" + part("day");
  }
  function addDays(date, count) {
    const at = new Date(date + "T12:00:00Z");
    at.setUTCDate(at.getUTCDate() + count);
    return at.toISOString().slice(0, 10);
  }
  function addMonths(month, count) {
    const at = new Date(month + "-01T12:00:00Z");
    at.setUTCMonth(at.getUTCMonth() + count);
    return at.toISOString().slice(0, 7);
  }
  function monthDates(month) {
    const first = month + "-01";
    const weekday = new Date(first + "T12:00:00Z").getUTCDay();
    const start = addDays(first, -((weekday + 6) % 7));
    return Array.from({ length: 42 }, (_, index) => addDays(start, index));
  }
  function effectiveRule(rule, today) {
    return rule?.end_date >= today ? rule : { mode: "none" };
  }
  function ruleMode() {
    return $("rule-exclude").checked ? "exclude" : $("rule-tomorrow").checked ? "wake_tomorrow" : "none";
  }
  function populateTimezones(value = "Asia/Singapore") {
    const common = {
      "Asia/Singapore": "新加坡", "Asia/Shanghai": "中国上海", "Asia/Hong_Kong": "香港",
      "Asia/Tokyo": "东京", "UTC": "协调世界时", "Europe/London": "伦敦",
      "America/New_York": "纽约", "America/Los_Angeles": "洛杉矶",
    };
    let supported = [];
    try { supported = Intl.supportedValuesOf("timeZone"); } catch {}
    const values = [...new Set([...Object.keys(common), value, ...supported])];
    $("timezone").replaceChildren();
    for (const zone of values) {
      const option = document.createElement("option");
      option.value = zone;
      option.textContent = common[zone] ? common[zone] + " · " + zone : zone;
      $("timezone").append(option);
    }
    $("timezone").value = value;
  }
  function draftRule(at = currentTime()) {
    const today = dateInZone(at);
    if (!ruleTouched) return effectiveRule(loadedRule, today);
    const mode = ruleMode();
    if (mode === "exclude") return { mode, start_date: $("exclude-start").value, end_date: $("exclude-end").value };
    if (mode === "wake_tomorrow") {
      const tomorrow = addDays(today, 1);
      return { mode, start_date: tomorrow, end_date: tomorrow };
    }
    return { mode: "none" };
  }
  function updateDateControls() {
    const today = dateInZone(currentTime()), mode = ruleMode();
    const preserved = !ruleTouched && loadedRule.mode === "exclude" && loadedRule.end_date >= today;
    for (const id of ["exclude-start", "exclude-end"]) {
      $(id).min = preserved && loadedRule.start_date < today ? loadedRule.start_date : today;
      $(id).max = addDays(today, 29);
      $(id).required = mode === "exclude";
      $(id).disabled = mode !== "exclude";
    }
    $("rule-range").hidden = mode !== "exclude";
    const rule = draftRule();
    $("rule-draft").textContent = rule.mode === "exclude"
      ? "保存后：排除 " + (rule.start_date || "开始日期") + " 至 " + (rule.end_date || "结束日期") + "（包含首尾两天）"
      : rule.mode === "wake_tomorrow"
        ? "保存后：" + rule.start_date + " 按已启用的每日时点唤醒，覆盖节假日和周末限制。"
        : "保存后：按默认每日时点和节假日设置运行。";
    renderCalendar();
  }
  function changeRule(mode) {
    ruleTouched = true;
    selectingEnd = false;
    for (const [id, value] of [["rule-none", "none"], ["rule-exclude", "exclude"], ["rule-tomorrow", "wake_tomorrow"]]) $(id).checked = mode === value;
    if (mode === "exclude" && !$("exclude-start").value) {
      $("exclude-start").value = $("exclude-end").value = dateInZone(currentTime());
    }
    updateDateControls();
  }
  function renderCalendar(value) {
    if (value) calendar = value;
    if (!calendar) return;
    const today = dateInZone(currentTime(), calendar.timezone), currentMonth = today.slice(0, 7);
    const firstMonth = addMonths(currentMonth, -1), lastMonth = addMonths(currentMonth, 1);
    if (calendarTimezone !== calendar.timezone || calendarAnchor !== currentMonth || calendarMonth < firstMonth || calendarMonth > lastMonth) calendarMonth = currentMonth;
    calendarAnchor = currentMonth;
    calendarTimezone = calendar.timezone;
    $("calendar-title").textContent = calendarMonth.slice(0, 4) + " 年 " + Number(calendarMonth.slice(5)) + " 月 · " + calendar.timezone;
    $("calendar").setAttribute("aria-label", calendarMonth + " 月日期安排，星期一至星期日");
    $("calendar-prev").disabled = calendarMonth === firstMonth;
    $("calendar-next").disabled = calendarMonth === lastMonth;
    $("calendar-current").disabled = calendarMonth === currentMonth;
    $("holiday-error").textContent = calendar.error || "";
    $("holiday-refresh").disabled = !!calendar.refreshing;
    $("holiday-refresh").textContent = calendar.refreshing ? "正在获取节假日数据…" : "刷新节假日数据";
    $("holiday-meta").replaceChildren();
    for (const year of calendar.years || []) {
      const row = document.createElement("div");
      row.className = "holiday-year";
      let source = "尚未获取数据";
      if (year.manual) source = "手动导入（优先使用）";
      else if (year.source?.includes("timor.tech")) source = "提莫接口";
      else if (year.source?.includes("raw.githubusercontent.com")) source = "holiday-cn · GitHub";
      else if (year.source?.includes("jsdelivr.net")) source = "holiday-cn · jsDelivr";
      row.append(document.createTextNode(year.year + " 年 · " + source + (year.known ? " · 放假 " + year.off_days + " 天 / 补班 " + year.workdays + " 天 · 更新 " + format(year.updated_at) : "")));
      if (year.manual) {
        const clear = document.createElement("button");
        clear.type = "button";
        clear.textContent = "清除 " + year.year + " 年手动导入";
        clear.onclick = async () => {
          clear.disabled = true;
          try {
            renderCalendar(await request("/holidays/clear-import", { method: "POST", body: JSON.stringify({ year: year.year }) }));
            notice("已清除手动导入，恢复使用在线数据或缓存。");
          } catch (e) { notice("清除失败：" + e.message, true); clear.disabled = false; }
        };
        row.append(clear);
      }
      $("holiday-meta").append(row);
    }
    $("calendar").replaceChildren();
    const rule = draftRule();
    const days = new Map([...(calendar.display_days || []), ...(calendar.days || [])].map((day) => [day.date, day]));
    for (const date of monthDates(calendarMonth)) {
      const weekday = new Date(date + "T12:00:00Z").getUTCDay();
      const weekend = weekday === 0 || weekday === 6;
      const day = days.get(date) || { date, kind: weekend ? "weekend" : "weekday", name: weekend ? "普通周末" : "工作日" };
      const button = document.createElement("button"), title = document.createElement("strong"), name = document.createElement("span"), action = document.createElement("span");
      const selected = rule.mode === "exclude" && day.date >= rule.start_date && day.date <= rule.end_date;
      const wake = rule.mode === "wake_tomorrow" && day.date === rule.start_date;
      const skip = selected || (!wake && $("skip-holidays").checked && ["holiday", "weekend"].includes(day.kind));
      button.type = "button";
      button.dataset.date = day.date;
      button.disabled = day.date < today || day.date > addDays(today, 29);
      button.className = "calendar-day " + day.kind + (day.date.slice(0, 7) !== calendarMonth ? " adjacent" : "") + (day.date === today ? " today" : "") + (selected ? " selected" : wake ? " wake" : skip ? " skipped" : "");
      button.setAttribute("aria-label", day.date + " 星期" + ["日", "一", "二", "三", "四", "五", "六"][weekday] + " " + day.name + (selected ? "，一次性排除" : wake ? "，安排唤醒" : skip ? "，自动跳过" : "，按每日时点运行") + (button.disabled ? "，超出可选日期范围" : ""));
      if (day.date === today) button.setAttribute("aria-current", "date");
      title.textContent = String(Number(day.date.slice(8)));
      name.textContent = day.kind === "workday" && !/补班|调休/.test(day.name) ? day.name + " · 补班" : day.name;
      action.className = "calendar-action";
      action.textContent = selected ? "排除日期" : wake ? "安排唤醒" : skip ? "自动跳过" : "按每日时点";
      button.append(title, name, action);
      button.onclick = () => {
        if (ruleMode() !== "exclude") return;
        const today = dateInZone(currentTime());
        if (day.date < today || day.date > addDays(today, 29)) return;
        ruleTouched = true;
        if (!selectingEnd) {
          $("exclude-start").value = $("exclude-end").value = day.date;
          selectingEnd = true;
        } else {
          const start = $("exclude-start").value;
          $("exclude-start").value = day.date < start ? day.date : start;
          $("exclude-end").value = day.date < start ? start : day.date;
          selectingEnd = false;
        }
        updateDateControls();
      };
      $("calendar").append(button);
    }
  }
  async function request(path, options = {}) {
    const session = managementSession();
    const key = typeof session.managementKey === "string" ? session.managementKey.trim() : "";
    if (!key) throw Error("未获取到管理页面登录会话，请在 CLI Proxy API 管理页面重新登录后刷新。");
    // The resource and API must share an origin; never send a key to a stored URL.
    if (session.apiBase && new URL(session.apiBase, window.location.href).origin !== window.location.origin)
      throw Error("请从当前 CLI Proxy API 管理页面打开插件。");
    const res = await fetch(base + path, {
      ...options,
      headers: {
        Authorization: "Bearer " + key,
        "Content-Type": "application/json",
      },
    });
    let data = {};
    try {
      data = await res.json();
    } catch {}
    if (!res.ok)
      throw Error(
        res.status === 401
          ? "管理页面登录已失效，请重新登录后刷新。"
          : res.status === 409
            ? "账号已有未完成任务或插件未启用"
            : data.error || "请求失败：" + res.status,
      );
    return data;
  }
  function cell(row, value) {
    const td = document.createElement("td");
    td.textContent = String(value ?? "—");
    row.append(td);
    return td;
  }
  function isPlus(a) {
    return (a.plan_type || "").trim().toLowerCase() === "plus";
  }
  function selectedForWake(a) {
    return a.provider.toLowerCase() === "codex" && !a.disabled && isPlus(a) &&
      (!(modelConfig.auth_ids || []).length || modelConfig.auth_ids.includes(a.id));
  }
  function planLabel(a) {
    const plan = (a.plan_type || "").trim().toLowerCase();
    return { plus: "Plus", business_premium: "Business Premium", "business premium": "Business Premium",
      "business-premium": "Business Premium", self_serve_business_prolite: "Business Premium", business: "Business", team: "Team", pro: "Pro", free: "Free", enterprise: "Enterprise" }[plan] || plan || "未知";
  }
  function render(status) {
    modelConfig = status.config;
    if (status.now) {
      serverNow = status.now;
      serverReceivedAt = Date.now();
    }
    auths = quotaAuths || status.auths || [];
    $("health").textContent = status.enabled ? "插件运行中" : "插件已停用";
    $("run-all").disabled = !status.enabled || !auths.some(selectedForWake);
    $("next").textContent =
      "时区 " +
      modelConfig.timezone +
      " · " +
      (status.schedules || [])
        .map((s) => s.at + " → " + format(s.time))
        .join(" / ");
    const rule = status.effective_rule || effectiveRule(modelConfig.one_time_rule, dateInZone(currentTime(), modelConfig.timezone || "Asia/Singapore"));
    $("rule-active").textContent = rule.mode === "exclude" ? "当前日期安排：排除 " + rule.start_date + " 至 " + rule.end_date + "，到期自动恢复。"
      : rule.mode === "wake_tomorrow" ? "当前日期安排：" + rule.start_date + " 按每日时点唤醒，随后恢复默认规则。" : "当前日期安排：按默认规则。";
    if (status.holiday_status && (!$("timezone").value || $("timezone").value === status.holiday_status.timezone)) renderCalendar(status.holiday_status);
    const slots = status.slots || [];
    $("accounts").replaceChildren();
    for (const a of auths.filter((a) => a.provider.toLowerCase() === "codex")) {
      const slot = slots.find((s) => s.auth_id === a.id),
        row = document.createElement("tr"),
        quota = quotas[a.id] && (!slot?.quota?.observed_at ||
          new Date(quotas[a.id].observed_at) >= new Date(slot.quota.observed_at))
          ? quotas[a.id] : slot?.quota;
      cell(row, a.label || a.id);
      cell(row, planLabel(a));
      const remaining = (used) => Math.round((100 - used) * 100) / 100 + "%";
      cell(row, quota?.known ? remaining(quota.used_percent) : quota?.weekly ? "未提供" : "未知");
      cell(row, quota?.known ? format(quota.reset_at) : "—");
      cell(row, quota?.weekly ? remaining(quota.weekly.used_percent) : "未知");
      cell(row, format(quota?.weekly?.reset_at));
      const authError = {
        credential_reauth_required: "OAuth 凭据已失效，请在 CPA 重新登录此账号",
        credential_refresh_failed: "OAuth 自动刷新失败，请检查网络或重新登录",
        credential_refresh_unavailable: "CPA 不支持自动刷新，请升级 CPA 或重新登录",
        quota_unauthorized: "刷新后仍认证失败，请在 CPA 重新登录此账号",
      }[quota?.error];
      cell(row, authError || (a.disabled ? "凭据已禁用" : !isPlus(a) ? (a.plan_type ? "不参与唤醒 · 仅限 Plus" : "套餐未知 · 不参与唤醒") :
        !selectedForWake(a) ? "未选择唤醒" : (states[slot?.status] || "尚未执行") + (slot?.status === "skipped" && reasons[slot.last_error] ? " · " + reasons[slot.last_error] : "")));
      cell(
        row,
        slot ? Math.max(0, slot.attempts - 1) + " / " + slot.max_retries : "—",
      );
      cell(row, format(slot?.next_attempt));
      const action = cell(row, "");
      const button = document.createElement("button");
      button.textContent = "立即唤醒";
      button.disabled =
        !selectedForWake(a) ||
        !status.enabled ||
        (!!slot && slot.completed_at.startsWith("0001-"));
      button.addEventListener("click", () => run(a.id, button));
      action.append(button);
      $("accounts").append(row);
    }
    const failures = slots.filter(
      (s) => s.status === "failed" && !seen.has(s.key),
    );
    if (failures.length) {
      for (const s of failures) seen.add(s.key);
      try {
        localStorage.setItem(seenKey, JSON.stringify([...seen].slice(-2000)));
      } catch {}
      const text = failures
        .map(
          (s) =>
            (auths.find((a) => a.id === s.auth_id)?.label || s.auth_id) +
            "：" +
            (s.last_error || "重试次数已用完"),
        )
        .join("\n");
      $("failure-text").textContent = text;
      if (!$("failure").open) $("failure").showModal();
      if ("Notification" in window && Notification.permission === "granted") {
        try {
          new Notification("Codex 五小时唤醒失败", {
            body: text,
            tag: "cpa-5h-reset-" + failures.map((s) => s.key).join("-"),
          });
        } catch {
          notice("系统通知不可用，失败详情已显示在页面。", true);
        }
      }
    }
    if (status.state_error || status.auth_error)
      notice("后台状态：" + (status.state_error || status.auth_error), true);
    if (status.quota_routing?.state_error)
      notice("账号分配状态：" + status.quota_routing.state_error, true);
  }
  function scheduleRow(s) {
    const row = document.createElement("div");
    row.className = "row schedule";
    row.dataset.id = s.id;
    const enabled = document.createElement("input");
    enabled.type = "checkbox";
    enabled.checked = s.enabled;
    enabled.setAttribute("aria-label", "启用时点");
    const at = document.createElement("input");
    at.type = "time";
    at.required = true;
    at.value = s.at;
    at.setAttribute("aria-label", "每日时间");
    const remove = document.createElement("button");
    remove.type = "button";
    remove.textContent = "删除";
    remove.onclick = () => row.remove();
    row.append(enabled, at, remove);
    $("schedules").append(row);
  }
  function fill(config, status) {
    configuration = config;
    const c = { ...status.config, ...config };
    modelConfig = c;
    $("model").value = c.model;
    $("effort").value = c.reasoning_effort;
    populateTimezones(c.timezone || "Asia/Singapore");
    $("skip-holidays").checked = !!c.skip_cn_holidays;
    loadedRule = effectiveRule(c.one_time_rule, dateInZone(currentTime()));
    ruleTouched = false;
    selectingEnd = false;
    $("rule-none").checked = loadedRule.mode === "none";
    $("rule-exclude").checked = loadedRule.mode === "exclude";
    $("rule-tomorrow").checked = loadedRule.mode === "wake_tomorrow";
    $("exclude-start").value = loadedRule.mode === "exclude" ? loadedRule.start_date : "";
    $("exclude-end").value = loadedRule.mode === "exclude" ? loadedRule.end_date : "";
    updateDateControls();
    $("retries").value = c.max_retries;
    $("prompt").value = c.prompt;
    $("max-logs").value = c.max_log_entries;
    $("desktop").checked = !!c.desktop_notifications;
    $("quota-routing").checked = !!c.quota_routing?.enabled;
    $("schedules").replaceChildren();
    (status.config.schedules || []).forEach(scheduleRow);
    $("all-auths").checked = !(c.auth_ids || []).length;
    $("auth-select").replaceChildren();
    for (const a of auths.filter(
      (a) => a.provider.toLowerCase() === "codex" && !a.disabled && isPlus(a),
    )) {
      const label = document.createElement("label"),
        box = document.createElement("input");
      box.type = "checkbox";
      box.value = a.id;
      box.checked = (c.auth_ids || []).includes(a.id);
      label.append(box, document.createTextNode(" " + (a.label || a.id)));
      $("auth-select").append(label);
    }
    toggleAuths();
    $("fields").disabled = false;
  }
  function toggleAuths() {
    for (const box of $("auth-select").querySelectorAll("input"))
      box.disabled = $("all-auths").checked;
  }
  async function refresh(withConfig = false) {
    if (polling) return;
    polling = true;
    let quotaError = "";
    try {
      const [status, logs] = await Promise.all([
        request("/status"),
        request("/logs"),
      ]);
      if (withConfig || Date.now() - quotaUpdatedAt >= 60000) {
        try {
          const data = await request("/account-quotas");
          quotas = data.quotas || {};
          quotaAuths = data.auths || null;
        } catch (e) {
          quotaError = "额度查询失败：" + e.message;
        }
        quotaUpdatedAt = Date.now();
      }
      render(status);
      $("logs").textContent =
        (logs.logs || [])
          .slice()
          .reverse()
          .map(
            (l) =>
              format(l.time) +
              "  " +
              l.auth_id +
              "  " +
              (states[l.status] || l.event),
          )
          .join("\n") || "暂无记录";
      if (withConfig) {
        fill(await request("/config"), status);
        try { renderCalendar(await request("/holidays")); }
        catch (e) { $("holiday-error").textContent = "节假日数据获取异常：" + e.message; }
      }
      connected = true;
      if (quotaError) notice(quotaError, true);
      else if (withConfig && !status.state_error && !status.auth_error) notice("已连接。");
    } catch (e) {
      connected = false;
      notice(e.message, true);
    } finally {
      polling = false;
    }
  }
  async function run(id, button) {
    button.disabled = true;
    try {
      await request("/run", {
        method: "POST",
        body: JSON.stringify(id ? { auth_id: id } : {}),
      });
      notice("已排队；可在账号列表查看执行与重试状态。");
      await refresh();
    } catch (e) {
      notice(e.message, true);
    } finally {
      button.disabled = false;
    }
  }
  $("connect").onclick = () => {
    notice("正在连接…");
    refresh(true);
  };
  $("run-all").onclick = () => run("", $("run-all"));
  $("all-auths").onchange = toggleAuths;
  for (const [id, mode] of [["rule-none", "none"], ["rule-exclude", "exclude"], ["rule-tomorrow", "wake_tomorrow"]]) {
    $(id).onchange = () => changeRule(mode);
    $(id).onclick = () => changeRule(mode);
  }
  $("timezone").onchange = async () => {
    updateDateControls();
    const timezone = $("timezone").value;
    try {
      const data = await request("/holidays?timezone=" + encodeURIComponent(timezone));
      if ($("timezone").value === timezone) renderCalendar(data);
    } catch (e) { $("holiday-error").textContent = "节假日数据获取异常：" + e.message; }
  };
  $("skip-holidays").onchange = renderCalendar.bind(null, null);
  for (const [id, offset] of [["calendar-prev", -1], ["calendar-next", 1], ["calendar-current", 0]]) $(id).onclick = () => {
    if ($(id).disabled || !calendar) return;
    calendarMonth = offset ? addMonths(calendarMonth, offset) : dateInZone(currentTime(), calendar.timezone).slice(0, 7);
    renderCalendar();
  };
  for (const id of ["exclude-start", "exclude-end"]) $(id).onchange = () => {
    ruleTouched = true;
    selectingEnd = false;
    updateDateControls();
  };
  $("holiday-refresh").onclick = async () => {
    $("holiday-refresh").disabled = true;
    try {
      renderCalendar(await request("/holidays/refresh", { method: "POST" }));
      notice("已发起后台刷新，可继续编辑设置。");
    } catch (e) { $("holiday-error").textContent = "节假日数据获取异常：" + e.message; $("holiday-refresh").disabled = false; }
  };
  function resetImport() {
    importRevision++;
    pendingImport = null;
    $("holiday-import").disabled = true;
    $("import-preview").textContent = "";
  }
  $("holiday-file").onchange = resetImport;
  resetImport();
  $("holiday-preview").onclick = async () => {
    resetImport();
    const revision = importRevision;
    $("holiday-preview").disabled = true;
    try {
      const file = $("holiday-file").files?.[0];
      if (!file) throw Error("请选择年度节假日 JSON 文件。");
      if (file.size > 2 * 1024 * 1024) throw Error("文件不能超过 2 MiB。");
      const data = JSON.parse(await file.text());
      if (revision !== importRevision || file !== $("holiday-file").files?.[0]) return;
      const result = await request("/holidays/import", { method: "POST", body: JSON.stringify({ data, preview: true }) });
      if (revision !== importRevision || file !== $("holiday-file").files?.[0]) return;
      pendingImport = { data, file };
      $("import-preview").textContent = result.year + " 年：放假 " + result.off_days + " 天，补班 " + result.workdays + " 天。确认后将优先使用该文件，在线刷新不会覆盖。";
      $("holiday-import").disabled = false;
    } catch (e) { if (revision === importRevision) $("import-preview").textContent = "导入预览失败：" + e.message; }
    finally { $("holiday-preview").disabled = false; }
  };
  $("holiday-import").onclick = async () => {
    if (!pendingImport || pendingImport.file !== $("holiday-file").files?.[0] || $("holiday-import").disabled) return;
    $("holiday-import").disabled = true;
    $("holiday-file").disabled = $("holiday-preview").disabled = true;
    try {
      await request("/holidays/import", { method: "POST", body: JSON.stringify({ data: pendingImport.data, preview: false }) });
      resetImport();
      $("import-preview").textContent = "节假日数据已导入并生效。";
      renderCalendar(await request("/holidays"));
      await refresh();
    } catch (e) { $("import-preview").textContent = "导入失败：" + e.message; $("holiday-import").disabled = !pendingImport; }
    finally { $("holiday-file").disabled = $("holiday-preview").disabled = false; }
  };
  $("add-schedule").onclick = () =>
    scheduleRow({
      id: "daily-" + Date.now() + "-" + Math.random().toString(16).slice(2),
      at: "05:00",
      enabled: true,
    });
  $("settings").onsubmit = async (event) => {
    event.preventDefault();
    const selected = [
      ...$("auth-select").querySelectorAll("input:checked"),
    ].map((x) => x.value);
    if (!$("all-auths").checked && !selected.length) {
      notice("请选择至少一个账号，或勾选全部账号。", true);
      return;
    }
    const schedules = [...$("schedules").children].map((row) => ({
      id: row.dataset.id,
      enabled: row.querySelector("[type=checkbox]").checked,
      at: row.querySelector("[type=time]").value,
    }));
    if (
      new Set(schedules.filter((s) => s.enabled).map((s) => s.at)).size !==
      schedules.filter((s) => s.enabled).length
    ) {
      notice("启用的时点不能重复。", true);
      return;
    }
    try {
      const latest = await request("/config");
      const latestStatus = await request("/status");
      const at = latestStatus.now ? new Date(latestStatus.now) : currentTime();
      const rule = draftRule(at), today = dateInZone(at);
      if (ruleTouched && rule.mode === "exclude" && (!rule.start_date || !rule.end_date || rule.start_date < today || rule.end_date < rule.start_date || rule.end_date > addDays(today, 29))) {
        notice("请选择今天起未来 30 天内的起止日期，结束日期不能早于开始日期。", true);
        return;
      }
      const c = {
        ...latest,
        model: $("model").value.trim(),
        reasoning_effort: $("effort").value,
        timezone: $("timezone").value.trim(),
        skip_cn_holidays: $("skip-holidays").checked,
        one_time_rule: rule,
        max_retries: Number($("retries").value),
        prompt: $("prompt").value,
        max_log_entries: Number($("max-logs").value),
        desktop_notifications: $("desktop").checked,
        quota_routing: { ...(latest.quota_routing || {}), enabled: $("quota-routing").checked },
        auth_ids: $("all-auths").checked ? [] : selected,
        schedules,
      };
      delete c.times;
      await request("/config", { method: "PUT", body: JSON.stringify(c) });
      notice("设置已保存。");
      setTimeout(() => refresh(true), 1000);
    } catch (e) {
      notice(e.message, true);
    }
  };
  $("notify").onclick = async () => {
    if (!("Notification" in window)) {
      notice("此浏览器不支持系统通知。", true);
      return;
    }
    try {
      const permission = await Notification.requestPermission();
      notice(
        permission === "granted"
          ? "已开启系统通知，请保持页面打开。"
          : "未获通知权限；仍会显示页面失败弹窗。",
      );
    } catch {
      notice("通知请求失败；仍会显示页面失败弹窗。", true);
    }
  };
  window.addEventListener("storage", (e) => {
    if (e.key === seenKey) {
      try {
        for (const id of JSON.parse(e.newValue || "[]")) seen.add(id);
      } catch {}
    }
  });
  setInterval(() => {
    if (connected && !polling) refresh();
  }, 3000);
  $("connect").onclick();
})();
