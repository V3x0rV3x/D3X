"use strict";

// Vue « Médiathèque » : les films et séries rangés par ta médiathèque
// (renommés, sans leur .torrent) retrouvés sur V3X, vérifiés sur ton disque,
// puis ajoutés à ton client. Utilise les outils de app.js.

const libView = { data: null, tab: "add", selected: new Set(), source: "local", shown: false, timer: null, version: -1 };

const LIB_TABS = [
  { id: "add", label: "À ajouter", statuses: ["add", "queued", "checking", "failed"], empty: "Rien à ajouter pour l'instant : lance une analyse." },
  { id: "done", label: "En seed", statuses: ["seeding", "in_client"], empty: "" },
  { id: "partial", label: "Incomplètes", statuses: ["completable", "partial"], empty: "", note: "Il manque des fichiers de la release V3X dans ta médiathèque. « À compléter » : il ne manque que des petits fichiers annexes (.nfo, image, texte, sous-titre) ; coche-les et ajoute-les, ton client les télécharge dans ton dossier de liens, puis seede. Il faut le dossier de liens, avec des liens physiques (Réglages)." },
  { id: "other", label: "Autres", statuses: ["different", "error", "removed"], empty: "", note: "Mêmes tailles que des fichiers de ta médiathèque, mais pas les mêmes données (ou illisibles) : D3X ne les propose pas." },
];

const LIB_STATUS = {
  add: ["À ajouter", "add"],
  queued: ["En file", ""],
  checking: ["Vérification…", "warn"],
  failed: ["Vérification incomplète", "bad"],
  seeding: ["Ajouté, en seed", "ok"],
  in_client: ["Déjà dans le client", "ok"],
  partial: ["Incomplète", ""],
  completable: ["À compléter", "warn"],
  different: ["Autres données", "bad"],
  error: ["Illisible", "bad"],
  removed: ["Retiré du client", ""],
};

function libTabOf(status) {
  return LIB_TABS.find((t) => t.statuses.includes(status)) || LIB_TABS[LIB_TABS.length - 1];
}

async function libLoad() {
  try {
    libView.data = await api("/ui/library");
    libRender();
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

function libBusy() {
  const s = libView.data && libView.data.state;
  if (!s) return false;
  const p = s.scan.phase;
  return p === "list" || p === "read" || p === "match" || s.applying || (s.counts.checking || 0) > 0 || (s.counts.queued || 0) > 0;
}

function libPoll() {
  clearTimeout(libView.timer);
  if (!libView.shown) return;
  libView.timer = setTimeout(async () => {
    await libLoad();
    libPoll();
  }, libBusy() ? 1200 : 6000);
}

// ---------- dossiers ----------

function libFolderList() {
  const ul = $("#lib-folders");
  ul.replaceChildren();
  const folders = (libView.data && libView.data.folders) || [];
  for (const f of folders) {
    const li = el("li");
    const where = el("span", "lib-src", f.source === "ssh" ? "Seedbox" : localLabel());
    li.append(where, el("span", "lib-path", f.path));
    const del = el("button", "btn small", "Retirer");
    del.type = "button";
    del.addEventListener("click", () => libSaveFolders(folders.filter((x) => x !== f)));
    li.append(del);
    ul.append(li);
  }
  ul.classList.toggle("hidden", folders.length === 0);
  // Client en erreur : l'analyse le retente d'abord (Encrypted_Shards, 04/11/26).
  $("#lib-scan").disabled = folders.length === 0 || !(ui.state && ui.state.v3x.ok && clientsSet());
}

// Plusieurs clients : celui qui reçoit les ajouts de la médiathèque.
function libClient() {
  const list = (ui.state && ui.state.clients) || [];
  const row = $("#lib-client-row");
  row.classList.toggle("hidden", list.length < 2);
  if (list.length < 2) return;
  const sel = $("#lib-client");
  if (document.activeElement === sel) return;
  sel.replaceChildren(...list.map((c) => {
    const o = el("option", null, c.label + (c.ok ? "" : " (injoignable)"));
    o.value = c.id;
    return o;
  }));
  sel.value = (libView.data && libView.data.client) || list[0].id;
}

async function libSaveFolders(folders) {
  try {
    libView.data = await api("/ui/library/folders", { folders });
    libRender();
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

function libAddFolder() {
  const p = $("#lib-path").value.trim();
  if (!p) return toast("Indique le dossier de ta médiathèque (celui qui contient tes films ou tes séries).", "bad");
  const folders = ((libView.data && libView.data.folders) || []).concat([{ path: p, source: libView.source }]);
  $("#lib-path").value = "";
  $("#lib-fs").classList.add("hidden");
  libSaveFolders(folders);
}

async function libBrowse(path) {
  const ulist = $("#lib-fs");
  try {
    const r = await api("/ui/fs?source=" + libView.source + "&path=" + encodeURIComponent(path || ""));
    ulist.replaceChildren();
    if (r.parent !== null) {
      const up = el("li");
      const b = el("button", "fs-name", "Dossier parent");
      b.type = "button";
      b.addEventListener("click", () => libBrowse(r.parent));
      up.append(el("span", "fs-ico dir"), b);
      ulist.append(up);
    }
    for (const e of r.entries.filter((x) => x.dir && !x.skip)) {
      const li = el("li");
      const b = el("button", "fs-name", e.name);
      b.type = "button";
      b.addEventListener("click", () => libBrowse(e.path));
      const pick = el("button", "btn small", "Choisir ce dossier");
      pick.type = "button";
      pick.addEventListener("click", () => { $("#lib-path").value = e.path; libAddFolder(); });
      li.append(el("span", "fs-ico dir"), b, pick);
      ulist.append(li);
    }
    if (r.path) $("#lib-path").value = r.path;
    ulist.classList.remove("hidden");
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

window.libSources = () => {
  if (libView.source === "ssh" && !(ui.state && ui.state.ssh && ui.state.ssh.ok)) libView.source = "local";
  const shown = renderSourceSwitch($("#lib-src"), libView.source, (id) => {
    libView.source = id;
    $("#lib-path").value = "";
    $("#lib-fs").classList.add("hidden");
    window.libSources();
    if (id === "ssh") libBrowse("");
  });
  $("#lib-src-row").classList.toggle("hidden", !shown);
};

// ---------- affichage ----------

function libRender() {
  const d = libView.data;
  if (!d) return;
  libFolderList();
  libClient();
  const s = d.state;
  const p = s.scan.phase;
  const running = p === "list" || p === "read" || p === "match";
  $("#lib-scan").classList.toggle("hidden", running);
  $("#lib-stop").classList.toggle("hidden", !running && !s.applying);
  $("#lib-scan").textContent = p === "idle" ? "Analyser ma médiathèque" : "Analyser à nouveau";
  const prog = $("#lib-progress");
  prog.classList.toggle("hidden", !running);
  if (running) {
    const label = p === "list" ? "Liste des fichiers de ta médiathèque…" : p === "match" ? "Recherche sur V3X par taille…" : "Vérification sur ton disque : " + (s.scan.done + 1) + " / " + s.scan.total;
    const pct = s.scan.total ? (s.scan.done / s.scan.total) * 100 : 5;
    $("#lib-progress-label").textContent = label;
    $("#lib-progress-detail").textContent = s.scan.detail || "";
    $("#lib-progress-fill").style.width = Math.max(3, Math.min(100, pct)) + "%";
  }
  const err = $("#lib-error");
  err.textContent = p === "error" ? s.scan.error : "";
  err.classList.toggle("hidden", p !== "error");
  const warn = $("#lib-warn");
  warn.textContent = s.scan.warn && p !== "error" ? s.scan.warn + ". Ses torrents n'ont pas pu être écartés de la recherche." : "";
  warn.classList.toggle("hidden", !warn.textContent);

  // Comment D3X ajoutera.
  const how = $("#lib-how");
  how.classList.toggle("warn", !d.links);
  how.textContent = d.links
    ? "Ajout par liens " + (d.symbolic ? "symboliques" : "physiques") + ", dans ton dossier de liens : les fichiers de ta médiathèque ne sont jamais touchés."
    : "Sans liens, le torrent seede directement les fichiers de ta médiathèque : rien n'est copié ni déplacé. Si tu le retires un jour de ton client, garde les fichiers : « avec ses fichiers » effacerait ceux de ta médiathèque. Pour ne jamais courir ce risque, active les liens dans Réglages.";

  const rows = d.rows || [];
  // Les releases « À compléter » se cochent aussi : sans elles ici, chaque
  // rafraîchissement les décochait (MiTor, 23/10/26).
  const keys = new Set(rows.filter((r) => r.status === "add" || r.status === "completable").map((r) => r.key));
  for (const k of libView.selected) if (!keys.has(k)) libView.selected.delete(k);
  const counts = {};
  for (const r of rows) counts[libTabOf(r.status).id] = (counts[libTabOf(r.status).id] || 0) + 1;
  const tabs = $("#lib-tabs");
  tabs.replaceChildren();
  tabs.classList.toggle("hidden", rows.length === 0);
  for (const t of LIB_TABS) {
    if (t.id !== "add" && !counts[t.id]) continue;
    const b = el("button", "tab");
    b.setAttribute("role", "tab");
    b.setAttribute("aria-selected", String(t.id === libView.tab));
    b.append(el("span", null, t.label), el("span", "n", String(counts[t.id] || 0)));
    b.addEventListener("click", () => { libView.tab = t.id; libRender(); });
    tabs.append(b);
  }
  const tab = LIB_TABS.find((t) => t.id === libView.tab) || LIB_TABS[0];
  const list = rows.filter((r) => libTabOf(r.status).id === tab.id);
  const tbody = $("#lib-tbody");
  tbody.replaceChildren(...list.map(libRow));
  $("#lib-wrap").classList.toggle("hidden", rows.length === 0);
  const empty = $("#lib-empty");
  empty.textContent = list.length ? tab.note || "" : p === "done" && tab.id === "add" ? "Aucune release de V3X trouvée dans ces dossiers (hors celles que ton client seede déjà)." : tab.empty;
  empty.classList.toggle("hidden", !empty.textContent);
  const addable = list.filter((r) => r.status === "add" || r.status === "completable");
  const all = $("#lib-all");
  all.disabled = addable.length === 0;
  all.checked = addable.length > 0 && addable.every((r) => libView.selected.has(r.key));
  libBar();
}

function libRow(r) {
  const tr = el("tr");
  const td = el("td", "c-check");
  if (r.status === "add" || r.status === "completable") {
    const cb = el("input");
    cb.type = "checkbox";
    cb.checked = libView.selected.has(r.key);
    cb.setAttribute("aria-label", "Cocher " + r.name);
    cb.addEventListener("change", () => {
      if (cb.checked) libView.selected.add(r.key);
      else libView.selected.delete(r.key);
      libBar();
    });
    td.append(cb);
  }
  tr.append(td);
  const tdN = el("td");
  const name = el("div", "name");
  if (r.match && UUID.test(r.match.id)) {
    const a = el("a", null, r.name);
    a.href = "https://v3x.club/torrents/" + r.match.id;
    a.target = "_blank";
    a.rel = "noopener";
    name.append(a);
  } else name.textContent = r.name;
  tdN.append(name);
  if (r.folder) tdN.append(el("div", "sub", "Dans ta médiathèque : " + r.folder + (r.files > 1 ? " · " + r.files + " fichiers" : "")));
  if (r.detail) tdN.append(el("div", "sub", r.detail));
  if (r.missing && r.missing.length) {
    const m = el("div", "sub", "Manque : " + r.missing.slice(0, 3).map((x) => x.split("/").pop()).join(", ") + (r.missing.length > 3 ? "…" : ""));
    tdN.append(m);
  }
  tr.append(tdN, el("td", "num", fmtSize(r.size)));
  const tdS = el("td", "num seeds");
  if (r.match) {
    const b = el("b", r.match.seeders <= 1 ? "rare" : null, String(r.match.seeders));
    tdS.append(b, document.createTextNode(" / " + r.match.leechers));
  }
  tr.append(tdS);
  const tdE = el("td");
  const box = el("div", "state");
  const [label, kind] = LIB_STATUS[r.status] || [r.status, ""];
  box.append(el("span", "pill " + kind, label));
  if (r.status === "checking" && r.progress > 0) {
    const bar = el("div", "mini");
    const i = el("i");
    i.style.width = Math.round(r.progress * 100) + "%";
    bar.append(i);
    box.append(bar);
  }
  // Vérification incomplète : les mêmes boutons que dans le Cross-seed, sinon
  // la release restait bloquée, même vérifiée à 100 % ensuite dans le client
  // (SVz, ticket n°6, 04/11/26).
  if (r.status === "failed") {
    const rb = el("button", "btn small", "Revérifier");
    rb.type = "button";
    rb.title = "Relance la vérification dans ton client : la release démarre si elle atteint 100 %.";
    rb.addEventListener("click", () => libAction("/ui/recheck", r, rb, "Vérification relancée dans ton client."));
    box.append(rb);
    const b = el("button", "btn small", "Retirer du client");
    b.type = "button";
    b.title = "Retire ce torrent de ton client. Tes fichiers ne sont pas touchés.";
    b.addEventListener("click", () => libAction("/ui/remove", r, b, "Retiré de ton client, fichiers intacts."));
    box.append(b);
  }
  tdE.append(box);
  tr.append(tdE);
  return tr;
}

// Revérifier ou retirer un ajout (mêmes routes que le Cross-seed), puis la
// Médiathèque est relue ; libPoll remplace le suivi en cours, sans le doubler.
async function libAction(path, r, btn, ok) {
  btn.disabled = true;
  try {
    await api(path, { key: r.key });
    toast(ok, "ok");
    await libLoad();
    libPoll();
  } catch (e) {
    btn.disabled = false;
    if (e.message !== "session") toast(e.message, "bad");
  }
}

function libBar() {
  const rows = (libView.data && libView.data.rows) || [];
  const sel = rows.filter((r) => libView.selected.has(r.key));
  $("#lib-sel").textContent = sel.length + " " + plural(sel.length, "release cochée", "releases cochées") + " · " + fmtSize(sel.reduce((a, r) => a + r.size, 0));
  $("#lib-bar").classList.toggle("hidden", !libView.shown || sel.length === 0);
}

async function libApply() {
  const keys = [...libView.selected];
  if (!keys.length) return;
  $("#lib-apply").disabled = true;
  try {
    const r = await api("/ui/library/apply", { keys });
    libView.selected.clear();
    toast(r.queued + " " + plural(r.queued, "release mise", "releases mises") + " en file. Ton client les vérifie une par une" + (libView.tab === "partial" ? ", puis télécharge les petits fichiers manquants." : "."), "ok");
    await libLoad();
    libPoll();
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  } finally {
    $("#lib-apply").disabled = false;
  }
}

window.libShow = async (on) => {
  libView.shown = on;
  libBar();
  if (!on) return clearTimeout(libView.timer);
  window.libSources();
  await libLoad();
  libPoll();
};

function bindLibrary() {
  $("#lib-add").addEventListener("click", libAddFolder);
  $("#lib-path").addEventListener("keydown", (e) => { if (e.key === "Enter") libAddFolder(); });
  $("#lib-browse").addEventListener("click", () => libBrowse($("#lib-path").value));
  $("#lib-scan").addEventListener("click", async () => {
    try {
      await api("/ui/library/scan", {});
      libView.tab = "add";
      await libLoad();
      libPoll();
    } catch (e) {
      if (e.message !== "session") toast(e.message, "bad");
    }
  });
  $("#lib-stop").addEventListener("click", async () => {
    try { await api("/ui/library/stop", {}); await libLoad(); } catch (e) { /* rien */ }
  });
  $("#lib-apply").addEventListener("click", libApply);
  $("#lib-client").addEventListener("change", async (ev) => {
    try {
      libView.data = await api("/ui/library/client", { client: ev.target.value });
      libRender();
    } catch (e) {
      if (e.message !== "session") toast(e.message, "bad");
    }
  });
  $("#lib-all").addEventListener("change", (ev) => {
    for (const r of (libView.data && libView.data.rows) || []) {
      if ((r.status !== "add" && r.status !== "completable") || libTabOf(r.status).id !== libView.tab) continue;
      if (ev.target.checked) libView.selected.add(r.key);
      else libView.selected.delete(r.key);
    }
    libRender();
  });
}

bindLibrary();
