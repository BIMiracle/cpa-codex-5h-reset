"use strict";
(() => {
  const $ = (id) => document.getElementById(id),
    base = "/v0/management/plugins/cpa-codex-5h-reset";
  let configuration = {},
    connected = false,
    polling = false,
    auths = [],
    modelConfig = {};
  const states = {
    pending: "排队中",
    requesting: "正在请求",
    retry_pending: "等待重试",
    wait_for_reset: "等待五小时重置",
    fresh_window: "请求成功 · 新窗口信号",
    request_ok_reset_unknown: "请求成功 · 重置未知",
    failed: "重试结束 · 失败",
    cancelled: "已取消",
  };
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
  async function request(path, options = {}) {
    const key = $("key").value.trim();
    if (!key) throw Error("请输入 CPA 管理密钥");
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
          ? "管理密钥无效"
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
  function render(status) {
    modelConfig = status.config;
    auths = status.auths || [];
    $("health").textContent = status.enabled ? "插件运行中" : "插件已停用";
    $("run-all").disabled = !status.enabled;
    $("next").textContent =
      "时区 " +
      modelConfig.timezone +
      " · " +
      (status.schedules || [])
        .map((s) => s.at + " → " + format(s.time))
        .join(" / ");
    const slots = status.slots || [];
    $("accounts").replaceChildren();
    for (const a of auths.filter((a) => a.provider.toLowerCase() === "codex")) {
      const slot = slots.find((s) => s.auth_id === a.id),
        row = document.createElement("tr");
      cell(row, a.label || a.id);
      cell(row, slot?.quota?.known ? slot.quota.used_percent + "%" : "未知");
      cell(row, format(slot?.quota?.reset_at));
      cell(row, a.disabled ? "凭据已禁用" : states[slot?.status] || "尚未执行");
      cell(
        row,
        slot ? Math.max(0, slot.attempts - 1) + " / " + slot.max_retries : "—",
      );
      cell(row, format(slot?.next_attempt));
      const action = cell(row, "");
      const button = document.createElement("button");
      button.textContent = "立即唤醒";
      button.disabled =
        a.disabled ||
        !status.enabled ||
        (slot && slot.completed_at.startsWith("0001-"));
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
    $("timezone").value = c.timezone;
    $("retries").value = c.max_retries;
    $("prompt").value = c.prompt;
    $("max-logs").value = c.max_log_entries;
    $("desktop").checked = !!c.desktop_notifications;
    $("schedules").replaceChildren();
    (status.config.schedules || []).forEach(scheduleRow);
    $("all-auths").checked = !(c.auth_ids || []).length;
    $("auth-select").replaceChildren();
    for (const a of auths.filter(
      (a) => a.provider.toLowerCase() === "codex" && !a.disabled,
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
    try {
      const [status, logs] = await Promise.all([
        request("/status"),
        request("/logs"),
      ]);
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
      if (withConfig) fill(await request("/config"), status);
      connected = true;
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
    refresh(true).then(() => {
      if (connected) notice("已连接。");
    });
  };
  $("run-all").onclick = () => run("", $("run-all"));
  $("all-auths").onchange = toggleAuths;
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
      const c = {
        ...latest,
        model: $("model").value.trim(),
        reasoning_effort: $("effort").value,
        timezone: $("timezone").value.trim(),
        max_retries: Number($("retries").value),
        prompt: $("prompt").value,
        max_log_entries: Number($("max-logs").value),
        desktop_notifications: $("desktop").checked,
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
})();
