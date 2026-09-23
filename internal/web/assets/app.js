/* The wizard. Plain JavaScript on purpose: the whole point of this tool is that
   `go build` is the entire build, so there is no bundler and no framework. */

(async () => {
"use strict";

/* ── translation ──────────────────────────────────────────────────────
   The catalogue is assets/i18n.json — plain data, so a test can check that
   every language answers the same set of keys. English is the default and
   also what the markup carries, which makes it the fallback for anything a
   translation misses.

   What is deliberately NOT translated: the per-item notes that come from the
   Go side. They name sing-box and Xray fields — `ip_is_private`, `hijack-dns`,
   `rule_set` — and a half-translated technical note is harder to act on than
   an English one. The chrome around them says what to do; the note says which
   field. */

async function loadCatalogue() {
  try {
    const res = await fetch("/assets/i18n.json");
    if (res.ok) return await res.json();
  } catch { /* fall through */ }
  // An unreadable catalogue costs the other four languages and nothing else:
  // every string in the markup is already English.
  return { langs: [{ id: "en", name: "English", dir: "ltr" }], en: {} };
}

const I18N = await loadCatalogue();
const LANGS = I18N.langs;

let lang = localStorage.getItem("nx-lang");
if (!LANGS.some((l) => l.id === lang)) lang = pickBrowserLang();

function pickBrowserLang() {
  for (const want of navigator.languages || [navigator.language || ""]) {
    const base = String(want).toLowerCase().split("-")[0];
    if (LANGS.some((l) => l.id === base)) return base;
  }
  return "en";
}

/* t looks a key up in the current language, falls back to English, and finally
   to the key itself — a missing string should read oddly, never blank. */
function t(key, vars) {
  let s = (I18N[lang] && I18N[lang][key]) ?? (I18N.en && I18N.en[key]) ?? key;
  if (vars) for (const [k, v] of Object.entries(vars)) s = s.replaceAll(`{${k}}`, v);
  return s;
}

function applyLang() {
  const meta = LANGS.find((l) => l.id === lang) || LANGS[0];
  document.documentElement.lang = meta.id;
  document.documentElement.dir = meta.dir;
  for (const el of document.querySelectorAll("[data-i18n]")) el.textContent = t(el.dataset.i18n);
  for (const el of document.querySelectorAll("[data-i18n-ph]")) el.placeholder = t(el.dataset.i18nPh);
  localStorage.setItem("nx-lang", lang);
  document.getElementById("lang").value = lang;

  // The parts drawn from data rather than markup have to be redrawn.
  renderSources();
  if (picked) describeSource();
  if (tree.length) renderTree();
  if (lastPreflight) renderPreflight(lastPreflight);
  if (lastResult) renderResult(lastResult);
}

{
  const sel = document.getElementById("lang");
  for (const l of LANGS) {
    const o = document.createElement("option");
    o.value = l.id;
    o.textContent = l.name;
    sel.append(o);
  }
  sel.value = lang;
  sel.onchange = () => { lang = sel.value; applyLang(); };
}

/* ── plumbing ──────────────────────────────────────────────────────── */

async function api(path, body) {
  const opt = { headers: { "X-Nexora-Migrate": "1" } };
  if (body !== undefined) {
    opt.method = "POST";
    opt.headers["Content-Type"] = "application/json";
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(path, opt);
  const text = await res.text();
  let data = {};
  try { data = text ? JSON.parse(text) : {}; } catch { data = { error: text }; }
  if (!res.ok) throw new Error(data.error || `${res.status} ${res.statusText}`);
  return data;
}

const $ = (sel) => document.querySelector(sel);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
};

function mb(n) {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  if (n >= 1 << 10) return Math.round(n / (1 << 10)) + " KB";
  return n + " B";
}

let maxStep = 1;
function goto(n) {
  maxStep = Math.max(maxStep, n);
  for (const p of document.querySelectorAll("[data-panel]")) p.hidden = +p.dataset.panel !== n;
  for (const b of document.querySelectorAll(".steps button")) {
    const s = +b.dataset.step;
    b.classList.toggle("on", s === n);
    b.classList.toggle("past", s < n);
  }
  window.scrollTo({ top: 0, behavior: "smooth" });
  if (n === 4) refreshPlan();
}
for (const b of document.querySelectorAll("[data-goto]")) {
  b.onclick = () => goto(+b.dataset.goto);
}
for (const b of document.querySelectorAll(".steps button")) {
  b.onclick = () => { if (+b.dataset.step <= maxStep) goto(+b.dataset.step); };
}

/* ── step 1: pick a source ─────────────────────────────────────────── */

let sources = [];
let picked = null;     // the chosen descriptor
let channel = "file";  // "file" or "api"
let auth = "login";    // "login" or "token", for the panels that offer both
let uploaded = null;   // { path, name, size } once a file has been handed over

api("/api/sources").then((list) => { sources = list; renderSources(); });

function renderSources() {
  const box = $("#sources");
  if (!box || !sources.length) return;
  box.textContent = "";
  for (const s of sources) {
    const card = el("button");
    card.type = "button";
    card.append(el("strong", null, s.label));
    const both = s.channels.includes("file") && s.channels.includes("api");
    card.append(el("em", null, t(both ? "cap.both" : s.channels.includes("file") ? "cap.file" : "cap.api")));
    if (picked && picked.id === s.id) card.classList.add("on");
    card.onclick = () => pick(s);
    box.append(card);
  }
}

function pick(s) {
  const changed = !picked || picked.id !== s.id;
  picked = s;
  if (changed) {
    // A file uploaded for one panel is not an answer for another.
    uploaded = null;
    channel = s.channels[0];
    auth = (s.auth || ["login"])[0];
    $("#read-error").hidden = true;
  }
  renderSources();
  $("#source-form").hidden = false;
  describeSource();
  $("#source-form").scrollIntoView({ behavior: "smooth", block: "nearest" });
}

/* describeSource redraws everything on the form that depends on which panel
   and which channel are chosen — the two hints, which fields exist, and the
   state of the drop zone. */
function describeSource() {
  const s = picked;
  const form = $("#source-form");
  $("#source-title").textContent = s.label;
  // The Go descriptor's hint is the English fallback; the catalogue has the
  // sentence in the operator's own language.
  $("#source-hint").textContent = (I18N[lang] && I18N[lang]["src." + s.id]) || (I18N.en && I18N.en["src." + s.id]) || s.hint;

  const both = s.channels.length > 1;
  $("#channels").hidden = !both;
  for (const b of $("#channels").querySelectorAll("button")) {
    b.classList.toggle("on", b.dataset.ch === channel);
    b.onclick = () => { channel = b.dataset.ch; describeSource(); };
  }
  $("#channel-hint").textContent = channel === "file"
    ? t("ch.file.hint")
    : t(s.apiFetchesBackup ? "ch.api.hint.backup" : "ch.api.hint.rest");

  // Which doors this panel has is a fact about the panel: the classic x-ui
  // line never had API tokens, Hiddify has nothing else. Offering a choice
  // where there is none is how an operator ends up typing a password into a
  // panel that was never going to check it.
  const modes = s.auth && s.auth.length ? s.auth : ["login"];
  if (!modes.includes(auth)) auth = modes[0];
  for (const b of $("#auths").querySelectorAll("button")) {
    b.classList.toggle("on", b.dataset.authMode === auth);
    b.onclick = () => { auth = b.dataset.authMode; describeSource(); };
  }

  const file = channel === "file";
  for (const f of form.querySelectorAll("[data-channel=file]")) f.hidden = !file;
  for (const f of form.querySelectorAll("[data-channel=api]")) f.hidden = file;
  for (const f of form.querySelectorAll("[data-auth=login]")) f.hidden = file || auth !== "login";
  for (const f of form.querySelectorAll("[data-auth=token]")) f.hidden = file || auth !== "token";
  for (const f of form.querySelectorAll("[data-proxy]")) f.hidden = file || !s.needsProxyPath;
  for (const f of form.querySelectorAll("[data-twofactor]")) f.hidden = file || auth !== "login" || !s.needsTwoFactor;
  // Last word on the picker itself: the loop above hid it along with the rest
  // of the API fields, and with only one mode there is nothing to pick.
  $("#auths").hidden = file || modes.length < 2;

  $("#path").placeholder = s.defaultPath || "";
  showUpload();
}

function showUpload() {
  $("#drop-idle").hidden = !!uploaded;
  $("#drop-done").hidden = !uploaded;
  $("#drop-busy").hidden = true;
  $("#drop").classList.toggle("has", !!uploaded);
  if (uploaded) {
    $("#file-name").textContent = uploaded.name;
    $("#file-size").textContent = mb(uploaded.size);
  }
}

/* The file picker is the normal way in: the wizard runs on a laptop and the
   database was just downloaded to it, so asking for an absolute path would be
   asking the operator what their browser calls the Downloads folder. The path
   field underneath is for the other case — the wizard running on the server
   over an SSH tunnel, where the file never left. */
$("#choose").onclick = () => $("#file").click();
$("#rechoose").onclick = () => $("#file").click();
$("#file").onchange = () => { if ($("#file").files[0]) upload($("#file").files[0]); };

{
  const drop = $("#drop");
  const stop = (e) => { e.preventDefault(); e.stopPropagation(); };
  for (const ev of ["dragenter", "dragover"]) {
    drop.addEventListener(ev, (e) => { stop(e); drop.classList.add("over"); });
  }
  for (const ev of ["dragleave", "drop"]) {
    drop.addEventListener(ev, (e) => { stop(e); drop.classList.remove("over"); });
  }
  drop.addEventListener("drop", (e) => {
    const f = e.dataTransfer && e.dataTransfer.files[0];
    if (f) upload(f);
  });
  // A file dropped anywhere else must not navigate the page away from a
  // half-finished migration.
  for (const ev of ["dragover", "drop"]) {
    window.addEventListener(ev, (e) => { if (e.target !== drop && !drop.contains(e.target)) e.preventDefault(); });
  }
}

/* upload streams the file to the wizard's own process, which parks it in a
   temporary file and deletes it when the program closes. XHR rather than fetch
   for one reason: it reports upload progress, and a 300 MB panel database with
   no progress bar looks like a hang. */
function upload(file) {
  $("#read-error").hidden = true;
  $("#drop-idle").hidden = true;
  $("#drop-done").hidden = true;
  $("#drop-busy").hidden = false;
  $("#up-bar").style.width = "0";

  const xhr = new XMLHttpRequest();
  xhr.open("POST", "/api/upload");
  xhr.setRequestHeader("X-Nexora-Migrate", "1");
  xhr.setRequestHeader("Content-Type", "application/octet-stream");
  xhr.setRequestHeader("X-File-Name", encodeURIComponent(file.name).replace(/%20/g, " "));
  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) $("#up-bar").style.width = `${Math.round((e.loaded / e.total) * 100)}%`;
  };
  xhr.onload = () => {
    let data = {};
    try { data = JSON.parse(xhr.responseText || "{}"); } catch { /* handled below */ }
    if (xhr.status === 200 && data.path) {
      uploaded = data;
      $("#path").value = "";
    } else {
      uploaded = null;
      $("#read-error").textContent = data.error || `${xhr.status} ${xhr.statusText}`;
      $("#read-error").hidden = false;
    }
    $("#file").value = "";
    showUpload();
  };
  xhr.onerror = () => {
    uploaded = null;
    $("#read-error").textContent = "the upload did not reach the wizard";
    $("#read-error").hidden = false;
    showUpload();
  };
  xhr.send(file);
}

$("#source-form").onsubmit = async (e) => {
  e.preventDefault();
  if (!picked) return;
  // Only the chosen mode's fields are sent. Otherwise a token left behind in
  // a field the operator has since switched away from would silently win over
  // the login they just typed.
  const login = auth === "login";
  const options = channel === "file"
    ? { channel: "file", path: uploaded ? uploaded.path : $("#path").value.trim() }
    : {
        channel: "api",
        baseUrl: $("#baseUrl").value.trim(),
        username: login ? $("#username").value.trim() : "",
        password: login ? $("#password").value : "",
        token: login ? "" : $("#token").value.trim(),
        twoFactor: login ? $("#twoFactor").value.trim() : "",
        proxyPath: $("#proxyPath").value.trim(),
        insecure: $("#insecure").checked,
      };
  $("#read-error").hidden = true;
  $("#read-spin").hidden = false;
  $("#read-btn").disabled = true;
  try {
    await api("/api/read", { source: picked.id, options });
    await loadTree();
    goto(2);
  } catch (err) {
    $("#read-error").textContent = err.message;
    $("#read-error").hidden = false;
  } finally {
    $("#read-spin").hidden = true;
    $("#read-btn").disabled = false;
  }
};

/* ── step 2: the selection tree ────────────────────────────────────── */

let tree = [], selected = new Set(), leaves = [], sourceInfo = null, bundleNotes = [];

async function loadTree() {
  const data = await api("/api/tree");
  adoptBundle(data);
  // Everything that can move is ticked to begin with: the common case is
  // "move it all", and unticking is easier than hunting.
  selected = new Set(leaves.filter((l) => l.item.severity !== "blocked").map((l) => l.key));
  renderTree();
}

function adoptBundle(data) {
  tree = data.tree || [];
  sourceInfo = data.source;
  bundleNotes = data.notes || [];
  leaves = [];
  collectLeaves(tree);
  renderOrigin();
}

function renderOrigin() {
  if (!sourceInfo) return;
  $("#origin").textContent =
    `${sourceInfo.panel}${sourceInfo.version ? " " + sourceInfo.version : ""} · ${sourceInfo.origin}`;
  const notes = $("#bundle-notes");
  notes.textContent = "";
  for (const n of bundleNotes) notes.append(el("div", null, n));
}

function collectLeaves(nodes) {
  for (const n of nodes) {
    if (n.item) leaves.push(n);
    else collectLeaves(n.children || []);
  }
}

function renderTree() {
  const box = $("#tree");
  box.textContent = "";
  renderOrigin();
  const needle = $("#filter").value.trim().toLowerCase();
  const onlyWarn = $("#only-warn").checked;
  for (const n of tree) {
    const node = renderNode(n, needle, onlyWarn, 0);
    if (node) box.append(node);
  }
  updateCount();
}

function matches(node, needle, onlyWarn) {
  if (node.item) {
    if (onlyWarn && node.item.severity === "ok") return false;
    if (!needle) return true;
    return (node.label + " " + (node.item.sourceName || "")).toLowerCase().includes(needle);
  }
  return (node.children || []).some((c) => matches(c, needle, onlyWarn));
}

function renderNode(node, needle, onlyWarn, depth) {
  if (!matches(node, needle, onlyWarn)) return null;

  const wrap = el("div", node.item ? "node leaf" : "node");
  const head = el("div", "head");

  const twist = el("span", "twist", node.item ? "›" : "▸");
  head.append(twist);

  const box = document.createElement("input");
  box.type = "checkbox";
  head.append(box);

  const label = el("span", "label", node.label);
  head.append(label);

  if (node.item) {
    const d = node.item.detail || {};
    const bits = [];
    for (const k of ["type", "protocol", "port", "quota", "expires", "role", "status"]) {
      if (d[k]) bits.push(`${k} ${d[k]}`);
    }
    if (bits.length) head.append(el("span", "sub", bits.slice(0, 3).join(" · ")));
    const sev = node.item.severity;
    const n = (node.item.notes || []).length;
    if (sev === "blocked") head.append(el("span", "badge blocked", t("t.blocked")));
    else if (sev === "warn") head.append(el("span", "badge warn", n === 1 ? t("t.note") : t("t.notes", { n })));
  } else {
    head.append(el("span", "badge", node.blocked ? `${node.count} · ${node.blocked} blocked` : String(node.count)));
  }
  wrap.append(head);

  if (node.item) {
    box.checked = selected.has(node.key);
    box.disabled = node.item.severity === "blocked";
    box.onclick = (e) => {
      e.stopPropagation();
      if (box.checked) selected.add(node.key); else selected.delete(node.key);
      refreshBoxes();
      updateCount();
    };
    head.onclick = (e) => { if (e.target !== box) wrap.classList.toggle("open"); };
    wrap.append(renderDetail(node.item));
    wrap.dataset.key = node.key;
  } else {
    const kids = el("div", "kids");
    for (const c of node.children || []) {
      const cn = renderNode(c, needle, onlyWarn, depth + 1);
      if (cn) kids.append(cn);
    }
    wrap.append(kids);
    if (depth === 0 || needle) wrap.classList.add("open");
    head.onclick = (e) => { if (e.target !== box) wrap.classList.toggle("open"); };
    box.onclick = (e) => {
      e.stopPropagation();
      setSubtree(node, box.checked);
      refreshBoxes();
      updateCount();
    };
    wrap._node = node;
    wrap._box = box;
  }
  wrap._box = box;
  wrap._node = node;
  return wrap;
}

function renderDetail(item) {
  const d = el("div", "detail");
  const dl = el("dl");
  if (item.sourceName && item.sourceName !== item.name) {
    dl.append(el("dt", null, t("t.srcname")), el("dd", null, item.sourceName));
  }
  for (const [k, v] of Object.entries(item.detail || {})) {
    dl.append(el("dt", null, k), el("dd", null, v));
  }
  if (dl.children.length) d.append(dl);
  if (item.notes && item.notes.length) {
    const ul = el("ul");
    for (const n of item.notes) {
      ul.append(el("li", item.severity === "blocked" ? "blocked" : null, n));
    }
    d.append(ul);
  }
  return d;
}

/* Ticking a group takes exactly what that group is showing. With a filter
   typed, a group row that reads "3" must not quietly select the 400 rows the
   filter is hiding. */
function setSubtree(node, on) {
  const needle = $("#filter").value.trim().toLowerCase();
  const onlyWarn = $("#only-warn").checked;
  const walk = (n) => {
    if (!matches(n, needle, onlyWarn)) return;
    if (n.item) {
      if (n.item.severity === "blocked") return;
      if (on) selected.add(n.key); else selected.delete(n.key);
      return;
    }
    for (const c of n.children || []) walk(c);
  };
  walk(node);
}

/* visibleLeaves is what "select all" and the counter mean: the rows currently
   on screen, not everything that was read. */
function visibleLeaves() {
  const needle = $("#filter").value.trim().toLowerCase();
  const onlyWarn = $("#only-warn").checked;
  return leaves.filter((l) => l.item.severity !== "blocked" && matches(l, needle, onlyWarn));
}

/* refreshBoxes walks the rendered tree and puts every group checkbox into the
   right one of its three states — on, off, or indeterminate — which is what
   makes "all inbounds / this group / these two rows" legible at a glance. */
function refreshBoxes() {
  const walk = (elm) => {
    const node = elm._node;
    if (!node) return { total: 0, on: 0 };
    if (node.item) {
      const on = selected.has(node.key) ? 1 : 0;
      elm._box.checked = !!on;
      return { total: node.item.severity === "blocked" ? 0 : 1, on };
    }
    let total = 0, on = 0;
    for (const kid of elm.querySelectorAll(":scope > .kids > .node")) {
      const r = walk(kid);
      total += r.total; on += r.on;
    }
    elm._box.checked = total > 0 && on === total;
    elm._box.indeterminate = on > 0 && on < total;
    return { total, on };
  };
  for (const root of document.querySelectorAll("#tree > .node")) walk(root);
}

function updateCount() {
  const shown = visibleLeaves().length;
  const all = leaves.filter((l) => l.item.severity !== "blocked").length;
  $("#sel-count").textContent = shown === all
    ? `${selected.size} / ${all}`
    : `${selected.size} / ${all}  (${shown} shown)`;
  refreshBoxes();
}

$("#filter").oninput = renderTree;
$("#only-warn").onchange = renderTree;
$("#select-all").onclick = () => {
  for (const l of visibleLeaves()) selected.add(l.key);
  renderTree();
};
$("#select-none").onclick = () => {
  for (const l of visibleLeaves()) selected.delete(l.key);
  renderTree();
};

/* ── step 3: connect to Nexora ─────────────────────────────────────── */

let lastPreflight = null;
let targetAuth = "login";

function renderTargetAuth() {
  for (const b of $("#t-auths").querySelectorAll("button")) {
    b.classList.toggle("on", b.dataset.authMode === targetAuth);
    b.onclick = () => { targetAuth = b.dataset.authMode; renderTargetAuth(); };
  }
  for (const f of document.querySelectorAll("[data-tauth=login]")) f.hidden = targetAuth !== "login";
  for (const f of document.querySelectorAll("[data-tauth=token]")) f.hidden = targetAuth !== "token";
}
renderTargetAuth();

$("#target-form").onsubmit = async (e) => {
  e.preventDefault();
  $("#conn-error").hidden = true;
  $("#conn-spin").hidden = false;
  const login = targetAuth === "login";
  try {
    const data = await api("/api/connect", {
      baseUrl: $("#t-url").value.trim(),
      username: login ? $("#t-user").value.trim() : "",
      password: login ? $("#t-pass").value : "",
      token: login ? "" : $("#t-token").value.trim(),
      code: login ? $("#t-code").value.trim() : "",
      insecure: $("#t-insecure").checked,
    });
    lastPreflight = data;
    renderPreflight(data);
    goto(4);
  } catch (err) {
    $("#conn-error").textContent = err.message;
    $("#conn-error").hidden = false;
  } finally {
    $("#conn-spin").hidden = true;
  }
};

function renderPreflight(data) {
  const box = $("#preflight");
  box.hidden = false;
  box.textContent = "";
  const p = data.preflight;
  box.append(el("h2", null,
    t("c.connected", { base: data.base, user: p.identity.username || "an API token" }) + ` (${p.identity.role})`));
  if (p.licence && p.licence.length) {
    const table = el("table", "sum");
    for (const u of p.licence) {
      const tr = el("tr");
      tr.append(el("td", null, u.resource));
      tr.append(el("td", null, u.max > 0 ? `${u.used} / ${u.max}` : `${u.used} / unlimited`));
      table.append(tr);
    }
    box.append(table);
  }
  if (p.nodes && p.nodes.length) {
    box.append(el("p", "hint", t("c.nodes", { n: p.nodes.length }) + p.nodes.map((n) => n.name).join(", ")));
  }
}

/* ── step 4: the plan ──────────────────────────────────────────────── */

async function refreshPlan() {
  const box = $("#plan");
  box.textContent = "";
  let data;
  try {
    data = await api("/api/plan", { selected: [...selected] });
  } catch (err) {
    box.append(el("div", "error", err.message));
    return;
  }
  const table = el("table", "sum");
  for (const [kind, n] of Object.entries(data.counts || {})) {
    if (!n) continue;
    const tr = el("tr");
    tr.append(el("td", null, kind));
    tr.append(el("td", null, String(n)));
    table.append(tr);
  }
  const tr = el("tr");
  tr.append(el("td", null, t("p.total")));
  tr.append(el("td", null, String(data.total)));
  table.append(tr);
  box.append(table);

  if (data.added && data.added.length) {
    box.append(el("div", "warn-box", t("p.added") + data.added.join(", ")));
  }
  for (const warn of data.warnings || []) box.append(el("div", "warn-box", warn));
  // A two-factor session is asked for a fresh code before the panel creates an
  // admin; asking here beats a 428 halfway through the run.
  const mfa = !!(lastPreflight && lastPreflight.preflight.identity.mfa);
  $("#confirm-box").hidden = !(mfa && (data.counts || {}).admin > 0);
  $("#go").disabled = data.total === 0;
}

/* ── step 5: run it ────────────────────────────────────────────────── */

let lastResult = null;

$("#go").onclick = async () => {
  goto(5);
  $("#log").textContent = "";
  $("#run-done").hidden = true;
  $("#run-actions").hidden = false;
  $("#bar").style.width = "0";
  try {
    await api("/api/apply", {
      selected: [...selected],
      pauseNodes: $("#pause-nodes").checked,
      code: $("#confirm-box").hidden ? "" : $("#confirm-code").value.trim(),
    });
  } catch (err) {
    $("#log").append(logLine({ status: "failed", name: "", message: err.message }));
    return;
  }
  listen();
};

function listen() {
  const src = new EventSource("/api/events");
  src.onmessage = (m) => {
    let e;
    try { e = JSON.parse(m.data); } catch { return; }
    if (e.phase === "finished") { src.close(); done(); return; }
    if (e.total) {
      $("#bar").style.width = `${Math.round((e.done / e.total) * 100)}%`;
      $("#run-counts").textContent = `${e.done} / ${e.total}`;
    }
    const log = $("#log");
    log.append(logLine(e));
    log.scrollTop = log.scrollHeight;
  };
  /* A dropped stream is not a finished run. Ask the server which it was, and
     reconnect if the run is still going — otherwise a lost connection would
     show the operator a "finished" screen for an import still in flight. */
  src.onerror = async () => {
    src.close();
    try {
      const data = await api("/api/result");
      if (data.running) { setTimeout(listen, 1000); return; }
    } catch { /* the server is gone; fall through and show what we have */ }
    done();
  };
}

function logLine(e) {
  const row = el("div", e.status || "");
  row.append(el("span", "st", e.phase ? "·" : (e.status || "")));
  row.append(el("span", "label", e.phase ? e.phase : `${e.kind || ""} ${e.name || ""}`));
  if (e.message) row.append(el("span", "msg", e.message));
  return row;
}

async function done() {
  let data;
  try { data = await api("/api/result"); } catch { return; }
  $("#run-actions").hidden = true;
  $("#run-done").hidden = false;
  if (!data.result) return;
  lastResult = data.result;
  renderResult(lastResult);
}

function renderResult(res) {
  $("#run-counts").textContent = [
    `${t("r.created")} ${res.created}`,
    `${t("r.existing")} ${res.existing}`,
    `${t("r.failed")} ${res.failed}`,
    `${t("r.skipped")} ${res.skipped}`,
  ].join(" · ");

  const pw = $("#passwords");
  pw.textContent = "";
  if (res.passwords && Object.keys(res.passwords).length) {
    pw.append(el("h2", null, t("r.pw")));
    const table = el("table");
    for (const [user, p] of Object.entries(res.passwords)) {
      const tr = el("tr");
      tr.append(el("td", null, user));
      tr.append(el("td", null, p));
      table.append(tr);
    }
    pw.append(table);
  }
  const notes = $("#run-notes");
  notes.textContent = "";
  for (const n of res.notes || []) notes.append(el("div", "warn-box", n));
}

$("#cancel").onclick = () => api("/api/cancel", {});

$("#save-report").onclick = () => {
  const res = lastResult;
  if (!res) return;
  const lines = [
    `Nexora migration report`,
    `started  ${res.started}`,
    `finished ${res.finished}`,
    ``,
    `created ${res.created}, already there ${res.existing}, failed ${res.failed}, skipped ${res.skipped}`,
    ``,
  ];
  if (res.passwords && Object.keys(res.passwords).length) {
    lines.push(`Admin passwords (shown once, stored nowhere):`);
    for (const [u, p] of Object.entries(res.passwords)) lines.push(`  ${u}  ${p}`);
    lines.push(``);
  }
  for (const n of res.notes || []) lines.push(`note: ${n}`);
  lines.push(``, `Every item:`);
  for (const e of res.events || []) {
    if (e.phase) { lines.push(`  [${e.phase}] ${e.message || ""}`); continue; }
    lines.push(`  ${(e.status || "").padEnd(8)} ${e.kind} ${e.name}${e.message ? "  — " + e.message : ""}`);
  }
  const blob = new Blob([lines.join("\n")], { type: "text/plain" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "nexora-migration-report.txt";
  a.click();
  URL.revokeObjectURL(a.href);
};

$("#finish").onclick = async () => {
  try { await api("/api/quit", {}); } catch { /* the server is going away; that is the point */ }
  document.querySelector("main").hidden = true;
  document.getElementById("steps").hidden = true;
  $("#closed").hidden = false;
};

/* Reloading the page must not throw away a read that took two minutes against
   somebody's production panel. The server holds the bundle, so on load we ask
   whether there is one and pick the wizard up where it was. */
async function resume() {
  try {
    const data = await api("/api/tree");
    if (!data.tree || !data.tree.length) return;
    adoptBundle(data);
    selected = new Set(leaves.filter((l) => l.item.severity !== "blocked").map((l) => l.key));
    renderTree();
    maxStep = 2;
    goto(2);
  } catch { /* nothing has been read yet, which is the normal first run */ }
}

applyLang();
goto(1);
resume();
})();
