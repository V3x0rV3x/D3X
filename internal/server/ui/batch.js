"use strict";

// Vue « Lot ». Le lot tourne dans D3X, release par release ; la page choisit
// les releases, le lance, l'arrête et suit son état. D3X ne publie seul une
// release que si tout est sûr ; le reste est « à vérifier ».

const lot = {
  dir: "",
  source: "local", // local ou ssh
  entries: [], // { name, path, size, files, video, exec, flat, error, last, state, detail, checked }
  tab: "pending",
  running: false,
  stop: false,
  current: "",
  published: 0,
  done: 0,
  total: 0,
  seen: "", // dernier état du lot de D3X vu par cette page
  poll: null,
};

// États du lot, dans l'ordre des onglets.
const LOT_TABS = [
  ["pending", "En attente", "Pas encore traitées par un lot."],
  ["ready", "Prête", "Corrigée dans Publier et gardée : le lot la publiera avec ta fiche, telle quelle. « Revoir » pour la modifier."],
  // « À vérifier » plutôt que « À revoir » (AntoineAngel, 22/10/26) ; le bouton
  // « Revoir » de chaque ligne garde son nom : il ouvre la release dans Publier.
  ["review", "À vérifier", "Le lot n'a pas osé : la raison est sur la ligne. « Revoir » l'ouvre dans Publier."],
  ["simulated", "Simulé", "Le lot l'aurait publiée. Décoche Simulation pour le faire pour de vrai."],
  ["published", "Publié", "Publiée sur V3X et mise en seed."],
  ["duplicate", "Déjà présent", "Déjà sur V3X : rien à publier. « Revoir » la met en seed avec tes fichiers (ou la Médiathèque, s'ils portent d'autres noms)."],
  ["ignored", "Ignoré", "Écartée à la main : les lots la sautent."],
  ["error", "Erreur", "Illisible ou refusée par V3X."],
];
const LOT_STATE_OF_KIND = { published: "published", batch_ready: "ready", batch_review: "review", batch_simulated: "simulated", batch_duplicate: "duplicate", batch_ignored: "ignored", batch_error: "error" };

// ---------- liste ----------

function lotEntries(list, hits) {
  return list.map((e) => {
    const st = e.error ? "error" : (e.last && LOT_STATE_OF_KIND[e.last.kind]) || "pending";
    return { ...e, state: st, detail: e.error || (e.last && e.last.detail) || "", checked: st === "pending" || st === "ready" || st === "review" || st === "simulated", requests: (hits && hits[e.path]) || null };
  });
}

async function lotList() {
  const dir = $("#b-path").value.trim();
  if (!dir) return toast("Choisis le dossier qui contient les releases.", "bad");
  try {
    const r = await api("/ui/batch/list?source=" + lot.source + "&path=" + encodeURIComponent(dir));
    lot.dir = r.path;
    lot.picked = false;
    $("#b-from").classList.add("hidden");
    lot.entries = lotEntries(r.entries);
    $("#b-fs").classList.add("hidden");
    lot.tab = "pending";
    lotRender();
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

async function lotBrowse(path) {
  const ul = $("#b-fs");
  try {
    const r = await api("/ui/fs?source=" + lot.source + "&path=" + encodeURIComponent(path || ""));
    ul.replaceChildren();
    if (r.parent !== null) {
      const up = el("li");
      const b = el("button", "fs-name", "Dossier parent");
      b.type = "button";
      b.addEventListener("click", () => lotBrowse(r.parent));
      up.append(el("span", "fs-ico dir"), b);
      ul.append(up);
    }
    for (const e of r.entries.filter((x) => x.dir && !x.skip)) {
      const li = el("li");
      const b = el("button", "fs-name", e.name);
      b.type = "button";
      b.addEventListener("click", () => lotBrowse(e.path));
      const pick = el("button", "btn small", "Choisir ce dossier");
      pick.type = "button";
      pick.addEventListener("click", () => { $("#b-path").value = e.path; lotList(); });
      li.append(el("span", "fs-ico dir"), b, pick);
      ul.append(li);
    }
    if (r.path) $("#b-path").value = r.path;
    ul.classList.remove("hidden");
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

// Releases choisies dans « Pas sur V3X » : le lot les prend telles quelles.
window.lotFromPaths = async (paths, source, hits) => {
  if (lot.running) {
    setView("batch");
    return toast("Un lot tourne : attends sa fin ou arrête-le d'abord.", "bad");
  }
  try {
    const r = await api("/ui/batch/entries", { paths, source });
    lot.source = source;
    lot.dir = "";
    lot.picked = true;
    $("#b-path").value = "";
    $("#b-fs").classList.add("hidden");
    lot.entries = lotEntries(r.entries, hits);
    const from = $("#b-from");
    from.textContent = lot.entries.length + " " + plural(lot.entries.length, "release choisie", "releases choisies") + " dans Cross-seed ▸ Pas sur V3X" + (source === "ssh" ? ", sur ta seedbox." : ".");
    from.classList.remove("hidden");
    lot.tab = "pending";
    window.lotSources();
    setView("batch");
    lotRender();
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
};

// Ce PC ou la seedbox : changer de source vide la liste.
window.lotSources = () => {
  if (lot.source === "ssh" && !(ui.state && ui.state.ssh && ui.state.ssh.ok)) lot.source = "local";
  const shown = renderSourceSwitch($("#b-src"), lot.source, (id) => {
    if (lot.running) return toast("Un lot tourne : arrête-le avant de changer de source.", "bad");
    lot.source = id;
    lot.entries = [];
    lot.dir = "";
    lot.picked = false;
    $("#b-from").classList.add("hidden");
    $("#b-path").value = "";
    $("#b-fs").classList.add("hidden");
    window.lotSources();
    lotRender();
    if (id === "ssh") lotBrowse("");
  });
  $("#b-src-row").classList.toggle("hidden", !shown);
};

// ---------- affichage ----------

function lotRender() {
  const has = lot.entries.length > 0;
  $("#b-tabs").classList.toggle("hidden", !has);
  $("#b-wrap").classList.toggle("hidden", !has);
  const counts = {};
  for (const e of lot.entries) counts[e.state] = (counts[e.state] || 0) + 1;
  const tabs = $("#b-tabs");
  tabs.replaceChildren();
  for (const [id, label, help] of LOT_TABS) {
    const b = el("button", "tab");
    b.setAttribute("role", "tab");
    b.setAttribute("aria-selected", String(id === lot.tab));
    b.title = help;
    b.append(el("span", null, label), el("span", "n", String(counts[id] || 0)));
    b.addEventListener("click", () => { lot.tab = id; lotRender(); });
    tabs.append(b);
  }
  const list = lot.entries.filter((e) => e.state === lot.tab);
  const tb = $("#b-tbody");
  tb.replaceChildren();
  for (const e of list) tb.append(lotRow(e));
  const empty = $("#b-empty");
  const help = LOT_TABS.find((t) => t[0] === lot.tab);
  empty.textContent = list.length ? "" : "Rien ici. " + (help ? help[2] : "");
  empty.classList.toggle("hidden", list.length > 0);
  const checkable = list.filter((e) => e.state !== "published" && e.state !== "duplicate" && e.state !== "error");
  $("#b-all").checked = checkable.length > 0 && checkable.every((e) => e.checked);
  $("#b-all").disabled = lot.running || checkable.length === 0;
  const resettable = lotResettable();
  $("#b-reset-line").classList.toggle("hidden", lot.running || resettable.length === 0);
  $("#b-reset").textContent = "Tout remettre en attente (" + resettable.length + ")";
  lotBar();
}

// Onglet ouvert (À revoir, Simulé, Ignoré, Erreur) : ses releases repartent
// « En attente », pour relancer le lot avec d'autres options (Turias, 21/10/26 :
// « Vidéos et albums seulement » laissé coché, aucun moyen de recommencer).
// Une release illisible le reste : son erreur vient de la lecture du dossier.
// « Déjà présent » aussi (CoprinusComatus, 23/10/26 : 15 releases rangées là
// par erreur, sans moyen de les relancer).
function lotResettable() {
  if (!["ready", "review", "simulated", "ignored", "error", "duplicate"].includes(lot.tab)) return [];
  return lot.entries.filter((e) => e.state === lot.tab && !e.error);
}

async function lotReset() {
  const list = lotResettable();
  for (const e of list) {
    e.state = "pending";
    e.detail = "";
    e.checked = true;
    try { await api("/ui/batch/decision", { path: e.path, source: lot.source, name: e.name, kind: "batch_reset", detail: "" }); } catch (err) { /* rien */ }
  }
  lot.tab = "pending";
  lotRender();
  toast(list.length + " " + plural(list.length, "release remise", "releases remises") + " en attente.", "ok");
}

function lotRow(e) {
  const tr = el("tr", e.path === lot.current ? "current" : "");
  const td = el("td", "c-check");
  if (e.state !== "published" && e.state !== "duplicate" && e.state !== "error") {
    const cb = el("input");
    cb.type = "checkbox";
    cb.checked = e.checked;
    cb.disabled = lot.running;
    cb.setAttribute("aria-label", "Inclure " + e.name);
    cb.addEventListener("change", () => { e.checked = cb.checked; lotBar(); });
    td.append(cb);
  }
  tr.append(td);
  const name = el("td");
  name.append(el("div", "name", e.name));
  const tags = [];
  if (e.files > 1) tags.push(e.files + " fichiers");
  if (e.audio) tags.push("album");
  else if (!e.video) tags.push("pas de vidéo");
  if (e.exec) tags.push("contient un exécutable");
  if (tags.length) name.append(el("div", "sub", tags.join(" · ")));
  const asked = (e.requests || []).filter((h) => !h.mine);
  if (asked.length) {
    const reqs = el("div", "reqs");
    if (e.state === "published" && e.active && e.last && e.last.id) for (const h of asked.slice(0, 2)) reqs.append(proposeButton(h, e.last.id));
    else reqs.append(...requestBadges(asked));
    name.append(reqs);
  }
  if (e.last && e.last.id && /^[0-9a-f-]{36}$/i.test(e.last.id)) {
    const a = el("a", "sub", "Voir la fiche sur V3X");
    a.href = "https://v3x.club/torrents/" + e.last.id;
    a.target = "_blank";
    a.rel = "noopener";
    name.append(a);
  }
  tr.append(name, el("td", "num", e.size ? fmtSize(e.size) : ""));
  const st = el("td");
  const label = (LOT_TABS.find((t) => t[0] === e.state) || [e.state, e.state])[1];
  const kind = { ready: "add", review: "warn", simulated: "add", published: "ok", duplicate: "ok", error: "bad" }[e.state] || "";
  st.append(el("span", "pill " + kind, e.path === lot.current ? "En cours…" : label));
  if (e.detail) st.append(el("div", "sub", e.detail));
  tr.append(st);
  const act = el("td");
  const box = el("div", "b-actions");
  // Déjà sur V3X : « Revoir » ouvre Publier, qui la met en seed avec ces
  // fichiers (Turias, 22/10/26).
  if (e.state === "review" || e.state === "simulated" || e.state === "pending" || e.state === "ready" || e.state === "duplicate") {
    const r = el("button", "btn small", "Revoir");
    r.type = "button";
    r.title = e.state === "duplicate" ? "Ouvrir dans Publier pour la seeder avec ces fichiers" : "Ouvrir dans Publier pour finir la fiche à la main, puis la publier ou la garder pour le lot";
    r.disabled = lot.running;
    r.addEventListener("click", () => { setView("publish"); choose(e.path, lot.source, { fromLot: true }); });
    box.append(r);
  }
  if (e.state !== "published" && e.state !== "duplicate") {
    const ig = el("button", "btn small", e.state === "ignored" ? "Ne plus ignorer" : "Ignorer");
    ig.type = "button";
    ig.disabled = lot.running;
    ig.addEventListener("click", () => lotIgnore(e));
    box.append(ig);
  }
  act.append(box);
  tr.append(act);
  return tr;
}

function lotBar() {
  const bar = $("#b-bar");
  const show = $("#view-batch") && !$("#view-batch").classList.contains("hidden") && lot.entries.length > 0;
  bar.classList.toggle("hidden", !show);
  if (!show) return;
  const checked = lot.entries.filter((e) => e.checked && (e.state === "pending" || e.state === "ready" || e.state === "review" || e.state === "simulated"));
  const n = checked.length;
  // Les cases restent cochées d'un onglet à l'autre et le lot les prend
  // toutes : on dit combien sont dans l'onglet ouvert, et « Tout décocher »
  // évite de repasser par chaque onglet (sultar, 22/10/26).
  const here = checked.filter((e) => e.state === lot.tab).length;
  const elsewhere = n > 0 && here < n ? ", dont " + (here ? here : "aucune") + " dans cet onglet" : "";
  $("#b-bar-txt").textContent = lot.running ? "Lot en cours : " + lot.done + " / " + lot.total : n + (n > 1 ? " releases cochées" : " release cochée") + elsewhere;
  $("#b-uncheck").classList.toggle("hidden", lot.running || n === 0);
  $("#b-bar-how").textContent = $("#b-sim").checked ? "Simulation : rien ne sera publié, tu verras ce que le lot ferait." : "Pour de vrai : les releases sûres seront publiées et mises en seed.";
  $("#b-run").classList.toggle("hidden", lot.running);
  $("#b-run").disabled = n === 0;
  $("#b-run").textContent = $("#b-sim").checked ? "Simuler le lot" : "Lancer le lot";
  $("#b-stop").classList.toggle("hidden", !lot.running);
}

function lotProgress(label, detail) {
  const p = $("#b-progress");
  p.classList.toggle("hidden", !lot.running);
  $("#b-progress-label").textContent = label || "";
  $("#b-progress-detail").textContent = detail || "";
  $("#b-progress-fill").style.width = (lot.total ? Math.max(3, (lot.done / lot.total) * 100) : 0) + "%";
}

async function decide(e, state, detail, id) {
  e.state = state;
  e.detail = detail || "";
  e.checked = state === "review" || state === "simulated";
  const kind = { review: "batch_review", simulated: "batch_simulated", duplicate: "batch_duplicate", ignored: "batch_ignored", error: "batch_error" }[state];
  if (kind) {
    try { await api("/ui/batch/decision", { path: e.path, source: lot.source, name: e.name, kind, detail: e.detail, id: id || "" }); } catch (err) { /* le lot continue */ }
  }
  lotRender();
}

async function lotIgnore(e) {
  if (e.state === "ignored") {
    e.state = "pending";
    e.detail = "";
    e.checked = true;
    try { await api("/ui/batch/decision", { path: e.path, source: lot.source, name: e.name, kind: "batch_unignored", detail: "" }); } catch (err) { /* rien */ }
    lotRender();
    return;
  }
  await decide(e, "ignored", "écartée à la main");
}

function waitCheckSaved() {
  try { return localStorage.getItem("d3x-lot-waitcheck") === "1"; } catch (err) { return false; }
}

// Option « Démarrer le seed à la fin du lot » (karat, 22/10/26) : D3X garde les
// releases publiées à l'arrêt pendant qu'il hache les suivantes.
function seedLaterSaved() {
  try { return localStorage.getItem("d3x-lot-seedlater") === "1"; } catch (err) { return false; }
}

// ---------- le lot tourne dans D3X ----------

// La page ne mène plus le lot : elle l'envoie à D3X (releases cochées et
// options), puis suit son état. Fermée et rouverte, elle le retrouve. Sur un
// serveur (Docker), le lot continue sans elle ; sur ce PC sans binaire
// mediainfo, c'est elle qui calcule le MediaInfo quand D3X le lui demande.

function lotOptions() {
  return {
    sim: $("#b-sim").checked,
    video: $("#b-video").checked,
    fixName: $("#b-fixname").checked,
    waitCheck: $("#b-waitcheck").checked,
    seedLater: $("#b-seedlater").checked,
    keepNfo: keepNfoOn(),
    cap: Number($("#b-cap").value) || 0,
    max: Number($("#b-max").value) || 0,
    maxGib: Number($("#b-maxgib").value) || 0,
  };
}

async function lotRun() {
  if (lot.running) return;
  if (!pub.ctx) await loadCtx();
  if (!pub.ctx) return toast("Publication impossible : " + (pub.ctxError || "V3X injoignable"), "bad");
  // Seulement ce qui est coché ET affiché à l'écran : un dossier apparu entre-temps
  // n'est jamais publié sans avoir été vu.
  const todo = lot.entries.filter((e) => e.checked && (e.state === "pending" || e.state === "ready" || e.state === "review" || e.state === "simulated"));
  if (!todo.length) return;
  try {
    const run = await api("/ui/batch/start", { path: lot.dir, source: lot.source, paths: todo.map((e) => e.path), options: lotOptions() });
    lot.seen = "";
    lot.tab = "pending";
    applyRun(run);
    lotPollSoon(0);
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

async function lotStop() {
  try {
    await api("/ui/batch/stop", {});
    toast("Le lot s'arrête après la release en cours.");
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

// applyRun reporte l'état du lot de D3X sur la liste de la page. Une page
// rouverte sans liste reprend le dossier du lot ; une liste déjà préparée (ou
// choisie dans Cross-seed) est gardée, le lot n'y met que ses états.
function applyRun(run) {
  if (!run) return;
  const active = run.state === "running" || run.state === "paused" || run.state === "stopping";
  if (active && lot.dir !== run.dir && !lot.picked) {
    lot.dir = run.dir;
    lot.source = run.source;
    $("#b-path").value = run.dir || "";
    $("#b-fs").classList.add("hidden");
    lot.entries = lotEntries(run.entries);
    window.lotSources();
  }
  const byPath = new Map(lot.entries.map((e) => [e.path, e]));
  for (const re of run.entries) {
    let e = byPath.get(re.path);
    if (!e) {
      e = lotEntries([re])[0];
      lot.entries.push(e);
    }
    e.state = re.state;
    e.detail = re.detail || "";
    e.job = re.job || e.job;
    e.last = re.last || e.last;
    e.active = !!re.active;
    if (re.requests) e.requests = re.requests;
    if (active) e.checked = false;
    else e.checked = re.state === "review" || re.state === "simulated";
  }
  lot.running = active;
  lot.stop = run.state === "stopping";
  lot.current = run.current || "";
  lot.done = run.done;
  lot.total = run.total;
  lot.published = run.published;
  lot.publishedBytes = run.publishedBytes;
  lotRender();
  lotProgress(run.label, run.state === "paused" ? "MediaInfo en cours dans cette page…" : run.detail);
  if (run.mediainfoWanted) lotServeMediaInfo(run.mediainfoWanted);
  // Fin du lot vue par cette page : le bilan, une fois. Une page ouverte sur
  // un lot déjà fini (lot.seen vide) n'en refait pas.
  if (lot.seen && lot.seen !== "done" && run.state === "done") {
    const counts = {};
    for (const e of lot.entries) counts[e.state] = (counts[e.state] || 0) + 1;
    lot.tab = run.options.sim ? "simulated" : counts.published ? "published" : "review";
    lotRender();
    const started = run.options.seedLater && !run.options.sim && run.published ? " Leurs seeds démarrent maintenant." : "";
    toast((run.stopped ? "Lot arrêté." : "Lot terminé : " + (run.options.sim ? (counts.simulated || 0) + " seraient publiées" : run.published + " publiées") + ", " + (counts.review || 0) + " à vérifier.") + started, "ok");
  }
  lot.seen = run.state;
}

// Suivi du lot : chaque seconde tant qu'il tourne, toutes les 5 s quand le
// Lot est affiché, sinon rien (le statut d'app.js voit encore les demandes
// de MediaInfo).
async function lotPoll() {
  clearTimeout(lot.poll);
  lot.poll = null;
  try {
    applyRun(await api("/ui/batch/state"));
  } catch (e) {
    if (e.message === "session") return;
  }
  const shown = $("#view-batch") && !$("#view-batch").classList.contains("hidden");
  if (lot.running) lotPollSoon(1000);
  else if (shown) lotPollSoon(5000);
}

function lotPollSoon(ms) {
  clearTimeout(lot.poll);
  lot.poll = setTimeout(lotPoll, ms);
}

// MediaInfo demandé par D3X : une seule fois par publication, par cette page.
// Illisible, la réponse vide part quand même : D3X range la release « à
// vérifier » au lieu d'attendre.
const miServed = new Set();
async function lotServeMediaInfo(req) {
  if (!req || miServed.has(req.jobId) || !window.mediaInfoRead) return;
  miServed.add(req.jobId);
  let text = "";
  try { text = await window.mediaInfoRead(req.jobId, req.fileIndex, req.size); } catch (e) { text = ""; }
  try { await api("/ui/pub/" + req.jobId + "/mediainfo", { text }); } catch (e) { /* une autre page a répondu, ou le lot est arrêté */ }
}
window.lotServeMediaInfo = lotServeMediaInfo;

window.batchShow = async (on) => {
  lotBar();
  if (!on) return;
  if (!pub.ctx) loadCtx();
  if (!$("#b-path").value && !lot.picked) {
    try { const s = await api("/ui/state"); $("#b-path").value = s.lastDir || ""; } catch (e) { /* rien */ }
  }
  lotRefresh();
  lotPoll();
};

// Retour sur le Lot : états relus dans le journal. Une release ouverte par
// « Revoir » et publiée dans Publier restait « À revoir » (Ekhstaz, 21/10/26).
// Les cases cochées et l'onglet ouvert sont gardés.
async function lotRefresh() {
  if (lot.running || !lot.entries.length || (!lot.picked && !lot.dir)) return;
  const before = new Map(lot.entries.map((e) => [e.path, e]));
  const hits = {};
  for (const e of lot.entries) if (e.requests) hits[e.path] = e.requests;
  try {
    const r = lot.picked
      ? await api("/ui/batch/entries", { paths: lot.entries.map((e) => e.path), source: lot.source })
      : await api("/ui/batch/list?source=" + lot.source + "&path=" + encodeURIComponent(lot.dir));
    if (lot.running) return;
    lot.entries = lotEntries(r.entries, hits).map((e) => {
      const old = before.get(e.path);
      return old && old.state === e.state ? { ...e, checked: old.checked } : e;
    });
    lotRender();
  } catch (e) {
    /* liste gardée telle quelle */
  }
}

function bindBatch() {
  $("#b-list").addEventListener("click", lotList);
  $("#b-path").addEventListener("keydown", (e) => { if (e.key === "Enter") lotList(); });
  $("#b-browse").addEventListener("click", () => lotBrowse($("#b-path").value));
  $("#b-run").addEventListener("click", lotRun);
  $("#b-stop").addEventListener("click", lotStop);
  $("#b-uncheck").addEventListener("click", () => {
    for (const e of lot.entries) e.checked = false;
    lotRender();
  });
  $("#b-sim").addEventListener("change", lotBar);
  $("#b-waitcheck").checked = waitCheckSaved();
  $("#b-waitcheck").addEventListener("change", (ev) => {
    try { localStorage.setItem("d3x-lot-waitcheck", ev.target.checked ? "1" : "0"); } catch (err) { /* rien */ }
  });
  $("#b-seedlater").checked = seedLaterSaved();
  $("#b-seedlater").addEventListener("change", (ev) => {
    try { localStorage.setItem("d3x-lot-seedlater", ev.target.checked ? "1" : "0"); } catch (err) { /* rien */ }
  });
  $("#b-reset").addEventListener("click", lotReset);
  $("#b-all").addEventListener("change", (ev) => {
    for (const e of lot.entries) if (e.state === lot.tab && e.state !== "published" && e.state !== "duplicate" && e.state !== "error") e.checked = ev.target.checked;
    lotRender();
  });
}

bindBatch();
