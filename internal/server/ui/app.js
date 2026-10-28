"use strict";

// Page locale de D3X. Tout le texte venu des torrents passe par textContent :
// un nom de release ne doit jamais pouvoir injecter du HTML.

const $ = (s) => document.querySelector(s);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

const TABS = [
  { id: "todo", label: "À ajouter", statuses: ["add", "queued", "checking", "failed"], selectable: true, empty: "Rien à ajouter : tout ce que V3X a en commun avec ton client y est déjà." },
  { id: "done", label: "Déjà en seed pour V3X", statuses: ["seeding", "in_client", "on_v3x"], empty: "Aucun torrent V3X dans ce client pour l'instant." },
  { id: "renamed", label: "Noms différents", statuses: ["renamed"], selectable: true, empty: "", note: "Mêmes fichiers que sur V3X, sous d'autres noms. D3X ajoute la release V3X à l'arrêt, donne à ses fichiers les noms de ta copie dans le client, puis vérifie : elle ne démarre qu'à 100 %. Rien n'est créé, déplacé ni renommé sur ton disque." },
  { id: "none", label: "Pas sur V3X", statuses: ["none"], selectable: true, empty: "", note: "Ces releases n'existent pas sur V3X : de bonnes candidates à un upload. « Publier » ouvre la fiche d'une release ; coches-en plusieurs pour les passer au crible du Lot." },
  { id: "other", label: "Autres", statuses: ["same_video", "gone_v3x", "partial", "error", "removed", "missing"], empty: "" },
];

const STATUS = {
  add: ["À ajouter", "add"],
  queued: ["En file", ""],
  checking: ["Vérification…", "warn"],
  failed: ["Vérification incomplète", "bad"],
  seeding: ["Ajouté, en seed", "ok"],
  in_client: ["Déjà dans le client", "ok"],
  on_v3x: ["Seedé pour V3X", "ok"],
  renamed: ["Noms différents", "warn"],
  none: ["Pas sur V3X", ""],
  same_video: ["Même vidéo sur V3X", ""],
  gone_v3x: ["Retirée de V3X", "warn"],
  partial: ["Téléchargé en partie", ""],
  error: ["Illisible", "bad"],
  missing: ["Absente du disque", "bad"],
  removed: ["Retiré du client", ""],
};

const PAGE = 400;
const UUID = /^[0-9a-f-]{36}$/i;

const ui = {
  // Filtre texte des listes (quebecman, 21/10/26) : mots à garder, « -mot » à écarter.
  filter: "",
  state: null,
  rows: [],
  version: -1,
  tab: "todo",
  shown: PAGE,
  selected: new Set(),
  picked: new Set(), // « Pas sur V3X » : releases choisies pour Publier ou Lot
  reqs: {}, // demandes ouvertes de V3X par ligne « Pas sur V3X »
  reqSig: "", // releases pour lesquelles ces données ont été lues
  pols: {}, // team au réupload interdit ou sur accord, par ligne « Pas sur V3X »
  polSig: "",
  relays: {}, // relais ouverts sur V3X, par info hash
  relayPrime: 0,
  relSig: "",
  view: "cross",
  showIgnored: false, // « Pas sur V3X » : réafficher les releases ignorées
};

// ---------- utilitaires ----------

function fmtSize(n) {
  if (!n || n < 1) return "0 octet";
  const u = ["octets", "Kio", "Mio", "Gio", "Tio", "Pio"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  const d = i === 0 ? 0 : v >= 100 ? 0 : v >= 10 ? 1 : 2;
  return v.toLocaleString("fr-FR", { maximumFractionDigits: d, minimumFractionDigits: 0 }) + " " + u[i];
}

function plural(n, one, many) { return n > 1 ? many : one; }

function toast(msg, kind) {
  const t = el("div", "toast" + (kind ? " " + kind : ""), msg);
  $("#toasts").append(t);
  setTimeout(() => t.remove(), kind === "bad" ? 9000 : 4500);
}

async function api(path, body) {
  const opt = body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  const r = await fetch(path, opt);
  const data = await r.json().catch(() => ({}));
  // Seul le refus de session de D3X lui-même ouvre ce bandeau ; un autre 401
  // est une erreur comme les autres.
  if (r.status === 401 && data.error === "session") {
    // Mode serveur (Docker, NAS) : pas de fenêtre à relancer, la page s'ouvre
    // avec le lien d'accès écrit dans le journal (sultar, 22/10/26).
    if (data.remote) $("#session").textContent = "Ouvre D3X avec son lien d'accès, qui finit par /auth?t=… : il est écrit dans le journal de D3X à son démarrage. Avec l'image Docker : docker compose logs d3x. Le lien reste le même d'un redémarrage à l'autre.";
    $("#session").classList.remove("hidden");
    throw new Error("session");
  }
  if (!r.ok) throw new Error(data.error || "erreur " + r.status);
  return data;
}

// ---------- apparence (comme sur le site) ----------

const ACCENTS = [
  ["bleu", "Bleu", "#4d8dff"], ["rose", "Rose", "#ff5d8f"], ["vert", "Vert", "#3ddc84"], ["violet", "Violet", "#a78bfa"],
  ["cyan", "Cyan", "#26d0e8"], ["ambre", "Ambre", "#ffb02e"], ["braise", "Braise", "#ff5a1f"], ["lime", "Lime", "#b8e62e"],
];
let previewing = false; // réglages ouverts : l'aperçu prime sur l'état reçu

function applyAppearance(theme, accent) {
  const d = document.documentElement;
  d.setAttribute("data-theme", theme || "dark");
  d.setAttribute("data-accent", accent || "bleu");
  try {
    localStorage.setItem("d3x-theme", theme || "dark");
    localStorage.setItem("d3x-accent", accent || "bleu");
  } catch (e) { /* stockage indisponible */ }
}

// Apparence choisie dans les réglages, appliquée tout de suite à titre d'aperçu.
function previewAppearance() {
  const s = ui.state;
  if (!s) return;
  previewing = true;
  const theme = $("#f-theme").value;
  const accent = ($("#f-accent").querySelector('[aria-checked="true"]') || {}).dataset;
  const a = accent && accent.accent;
  // « Comme sur V3X » : l'apparence du profil, reçue avec l'état.
  applyAppearance(theme || s.appearance.profileTheme || "dark", a || s.appearance.profileAccent || "bleu");
}

function buildSwatches(selected) {
  const box = $("#f-accent");
  box.replaceChildren();
  const add = (value, label, color) => {
    const b = el("button", "swatch" + (value ? "" : " auto"));
    b.type = "button";
    b.setAttribute("role", "radio");
    b.setAttribute("aria-checked", String(value === selected));
    b.setAttribute("aria-label", label);
    b.title = label;
    b.dataset.accent = value;
    if (color) b.style.setProperty("--c", color);
    else b.textContent = "V3X";
    b.addEventListener("click", () => {
      for (const x of box.children) x.setAttribute("aria-checked", String(x === b));
      previewAppearance();
    });
    box.append(b);
  };
  add("", "Comme sur V3X", null);
  for (const [v, l, c] of ACCENTS) add(v, l, c);
}

// ---------- connexions ----------

function renderState(s) {
  ui.state = s;
  if (s.appearance && !previewing) applyAppearance(s.appearance.theme, s.appearance.accent);
  $("#ver").textContent = s.version === "dev" ? "dev" : "v" + s.version;

  const cv = $("#chip-v3x");
  cv.classList.toggle("ok", s.v3x.ok);
  cv.classList.toggle("bad", s.v3x.configured && !s.v3x.ok);
  cv.querySelector(".txt").textContent = s.v3x.ok ? "V3X · " + s.v3x.username : s.v3x.configured ? "V3X · erreur" : "V3X · à régler";
  cv.title = s.v3x.error || "";

  renderClientChips(s.clients || []);

  const cs = $("#chip-ssh");
  const ssh = s.ssh || {};
  cs.classList.toggle("hidden", !ssh.configured);
  cs.classList.toggle("ok", !!ssh.ok);
  cs.classList.toggle("bad", !!ssh.configured && !ssh.ok);
  cs.querySelector(".txt").textContent = ssh.ok ? "Seedbox · " + ssh.label : "Seedbox · " + (ssh.error ? "erreur" : "à connecter");
  cs.title = ssh.ok ? ssh.system || "" : ssh.error || "Mot de passe ou phrase de passe à redonner";
  if (window.pubSources) window.pubSources();
  if (window.lotSources) window.lotSources();
  if (window.libSources) window.libSources();

  // Mode serveur : les fichiers sont ceux de la machine qui fait tourner D3X.
  document.documentElement.dataset.mode = s.mode || "";
  $("#detect").classList.toggle("hidden", s.mode === "docker");

  const ready = s.v3x.ok && clientsReady();
  $("#onboard").classList.toggle("hidden", s.v3x.configured || (s.clients || []).length > 0);
  $("#scan").disabled = !ready;
  $("#scan").title = ready ? "" : "Règle d'abord V3X et ton client torrent";

  const up = $("#update");
  if (s.update) {
    up.replaceChildren();
    up.classList.remove("bad");
    if (s.update.ready) {
      up.append(document.createTextNode("D3X " + s.update.version + " est prête, signature et empreinte vérifiées. "));
      const b = el("button", "btn small primary", "Redémarrer pour l'installer");
      b.type = "button";
      b.addEventListener("click", installUpdate);
      up.append(b);
    } else if (s.update.managed === "docker") {
      up.append(document.createTextNode("D3X " + s.update.version + " est sortie. Mets à jour l'image : docker compose pull, puis docker compose up -d. "));
    } else if (s.update.error) {
      up.classList.add("bad");
      up.append(document.createTextNode("D3X " + s.update.version + " est sortie, mais D3X ne l'installera pas : " + s.update.error + " "));
    } else {
      up.append(document.createTextNode("D3X " + s.update.version + " est sortie : téléchargement et vérification en cours… "));
    }
    if (!s.update.ready) {
      const a = el("a", null, "Voir la version");
      a.href = s.update.url;
      a.target = "_blank";
      a.rel = "noopener";
      up.append(a);
    }
    up.classList.remove("hidden");
  } else {
    up.classList.add("hidden");
  }
  renderWatch(s);
  if (lastVersion && lastVersion !== s.version) toast("D3X est passé en " + s.version + ".", "ok");
  lastVersion = s.version;
}

let lastVersion = "";

// ---------- clients torrent ----------

// Plusieurs clients réglés : les lignes portent alors le nom de leur client.
function multiClients() {
  return ((ui.state && ui.state.clients) || []).length > 1;
}

// Au moins un client répond.
function clientsReady() {
  return ((ui.state && ui.state.clients) || []).some((c) => c.ok);
}

function kindName(kind) {
  return (KINDS[kind] || KINDS.qbittorrent).name;
}

// Pastilles de la barre du haut : une par client (une seule, comme avant,
// quand il n'y en a qu'un).
function renderClientChips(list) {
  const box = $("#chips-clients");
  const chip = (cls, text, title) => {
    const b = el("button", "chip" + (cls ? " " + cls : ""));
    b.type = "button";
    const dot = el("span", "dot");
    b.append(dot, el("span", "txt", text));
    b.title = title || "";
    b.addEventListener("click", openSettings);
    return b;
  };
  const cls = (c) => (c.ok ? "ok" : c.pending ? "" : "bad");
  box.replaceChildren();
  if (!list.length) {
    box.append(chip("", "Client torrent · à régler", ""));
  } else if (list.length === 1) {
    const c = list[0];
    box.append(chip(cls(c), c.ok ? c.version : kindName(c.kind) + (c.pending ? " · connexion…" : " · erreur"), c.error || ""));
  } else {
    for (const c of list) box.append(chip(cls(c), c.label + (c.ok ? "" : c.pending ? " · connexion…" : " · injoignable"), c.label + " : " + (c.ok ? c.version : c.error || "connexion en cours")));
  }
}

function hourOf(iso) {
  return new Date(iso).toLocaleTimeString("fr-FR", { hour: "2-digit", minute: "2-digit" }).replace(":", " h ");
}

// Le jour d'une analyse quand ce n'est pas aujourd'hui (« demain », « hier »,
// « le 25 octobre »), suivi d'une espace ; vide sinon. Avec une surveillance
// toutes les 12 heures ou une fois par jour (Ekhstaz, 27/10/26), l'heure seule
// ne disait plus quel jour.
function dayOf(iso) {
  const d = new Date(iso);
  const now = new Date();
  const day = (x) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(d) - day(now)) / 86400000);
  if (diff === 0) return "";
  if (diff === 1) return "demain ";
  if (diff === -1) return "hier ";
  return "le " + d.toLocaleDateString("fr-FR", { day: "numeric", month: "long" }) + " ";
}

// Ligne « Surveillance » sous le titre du cross-seed.
function renderWatch(s) {
  const w = s.watch || {};
  const line = $("#watch-line");
  line.classList.toggle("hidden", !w.on);
  if (!w.on) return;
  const parts = [];
  if (w.running) parts.push("Surveillance : analyse en cours");
  else parts.push("Surveillance active" + (w.next ? " : prochaine analyse " + dayOf(w.next) + "vers " + hourOf(w.next) : ""));
  const l = w.last;
  line.classList.toggle("bad", !!(l && l.error));
  if (l && l.error) parts.push("la dernière a échoué (" + l.error + ")");
  else if (l && l.at) {
    const bits = [];
    bits.push(l.new ? l.new + " " + plural(l.new, "nouvelle", "nouvelles") : "rien de nouveau");
    if (l.added) bits.push(l.added + " " + plural(l.added, "ajoutée", "ajoutées"));
    if (l.danger) bits.push(l.danger + " hit and run en vue");
    if (l.warn) bits.push(l.warn);
    parts.push("dernière " + dayOf(l.at) + "à " + hourOf(l.at) + " : " + bits.join(", "));
  }
  $("#watch-txt").textContent = parts.join(". ") + ".";
}

function watchHint() {
  const s = ui.state || {};
  const w = s.watch || {};
  let t = "Une analyse part au lancement de D3X, puis à l'intervalle choisi. Seuls les torrents arrivés depuis la dernière fois sont relus : ton client n'est presque pas sollicité.";
  if (s.mode) t += " D3X tourne en continu sur ce serveur : la surveillance ne s'arrête pas.";
  else if (w.tray) t += " Fenêtre fermée, D3X reste dans la zone de notification (clic droit sur son icône ▸ Quitter pour l'arrêter).";
  else t += " Elle tourne tant que D3X est lancé.";
  $("#w-hint").textContent = t;
}

// Bouton « Vérifier » sous le numéro de version : cherche une nouvelle version
// tout de suite, puis suit son téléchargement et sa vérification jusqu'à ce
// que le bandeau propose de l'installer.
async function checkForUpdate() {
  const b = $("#ver-check");
  if (b.disabled) return;
  b.disabled = true;
  b.textContent = "…";
  try {
    const r = await api("/ui/update/check", {});
    renderState(await api("/ui/state"));
    if (r.update) {
      if (!r.update.ready && !r.update.error && !r.update.managed) {
        toast("D3X " + r.update.version + " est sortie : téléchargement et vérification…", "ok");
        for (let i = 0; i < 40; i++) {
          await new Promise((res) => setTimeout(res, 3000));
          const s = await api("/ui/state");
          renderState(s);
          if (!s.update || s.update.ready || s.update.error) break;
        }
      }
    } else if (r.latest) {
      toast("D3X est à jour (" + r.version + ").", "ok");
    } else {
      toast("Vérifié il y a moins d'une minute : D3X est à jour.", "ok");
    }
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  } finally {
    b.disabled = false;
    b.textContent = "Vérifier";
  }
}

// Installe la mise à jour vérifiée : D3X se relance sur la même session, la
// page se recharge seule.
async function installUpdate() {
  try {
    await api("/ui/update/restart", {});
    toast("Installation de la mise à jour, D3X redémarre…", "ok");
    const want = ui.state && ui.state.update ? ui.state.update.version : "";
    for (let i = 0; i < 60; i++) {
      await new Promise((r) => setTimeout(r, 1000));
      try {
        const s = await api("/ui/state");
        // Installation ratée (main l'a dit au serveur) : la raison, sans
        // attendre une minute un redémarrage qui ne viendra pas.
        if (s.version === (ui.state && ui.state.version) && s.update && !s.update.ready && s.update.error) {
          toast(s.update.error, "bad");
          return;
        }
        if (s.version === want || s.version !== (ui.state && ui.state.version)) {
          // Page entière rechargée : sinon l'ancienne interface restait
          // affichée face à la nouvelle version, avec son bandeau périmé
          // (retour de V3x0r, 20/10/26). Le message suit le rechargement.
          try { sessionStorage.setItem("d3x-updated", s.version); } catch (e) { /* rien */ }
          location.reload();
          return;
        }
      } catch (e) { /* redémarrage en cours */ }
    }
    toast("D3X ne répond plus : relance-le à la main.", "bad");
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

function fillSettings() {
  const s = ui.state;
  if (!s) return;
  $("#f-key").value = "";
  $("#f-key").placeholder = s.v3x.configured ? "Clé enregistrée (laisse vide pour la garder)" : "Colle ta clé ici";
  edit.clients = (s.clients || []).map((c) => ({ ...c, pass: "", name: c.name || "", url: c.url || "", user: c.user || "", pathMap: (c.pathMap || []).map((r) => ({ from: r.from || "", to: r.to || "" })), linksDir: c.linksDir || "", test: null }));
  if (!edit.clients.length) edit.clients.push(newClient());
  edit.open.clear();
  $("#detected").classList.add("hidden");
  renderClients();
  $("#f-cat").value = s.category || "";
  $("#f-pubcat").value = s.pubCategory || "";
  $("#f-discogs").value = "";
  $("#f-discogs").placeholder = s.discogs ? "Enregistré (laisse vide pour le garder)" : "facultatif";
  $("#f-discogs-clear").checked = false;
  $("#f-discogs-clear-l").classList.toggle("hidden", !s.discogs);
  $("#f-tag").value = s.tag || "";
  $("#f-updates").checked = !!s.checkUpdates;
  $("#f-theme").value = (s.appearance && s.appearance.uiTheme) || "";
  buildSwatches((s.appearance && s.appearance.uiAccent) || "");
  $("#l-on").checked = !!(s.links && s.links.on);
  $("#l-sym").checked = !!(s.links && s.links.symbolic);
  linksWarn();
  const w = s.watch || {};
  $("#w-on").checked = !!w.on;
  // Jusqu'à une fois par jour (Ekhstaz, 27/10/26) ; D3X borne à 24 h.
  $("#w-min").value = String([15, 30, 60, 180, 360, 720, 1440].includes(w.minutes) ? w.minutes : 30);
  $("#w-notify").checked = !!w.notify;
  $("#w-auto").checked = !!w.autoAdd;
  $("#w-start").checked = !!w.autostart;
  $("#w-start-row").classList.toggle("hidden", !w.canAutostart);
  $("#w-notify-row").classList.toggle("hidden", !!s.mode);
  watchHint();
  setStatus("#v3x-status", s.v3x.configured, s.v3x.ok, s.v3x.ok ? "Connecté en tant que " + s.v3x.username : s.v3x.error);
  fillSSH();
}

// Les clients en cours de réglage (rien n'est enregistré avant « Enregistrer
// et tester ») ; open : ceux dont le formulaire est déplié.
const edit = { clients: [], open: new Set(), seq: 0 };
const MAX_CLIENTS = 8;

function newClient(kind, url) {
  return { id: "nouveau-" + ++edit.seq, isNew: true, name: "", kind: kind || "qbittorrent", url: url || "", user: "", pass: "", insecure: false, pathMap: [], linksDir: "", hasPass: false, test: null };
}

// Nom d'un client dans les réglages : le sien, sinon son type.
function editName(c) {
  return (c.name || "").trim() || kindName(c.kind);
}

// État d'un client : essai fait à l'instant, sinon dernier test enregistré.
function editState(c) {
  if (c.test) return c.test.ok ? ["ok", "Connecté à " + c.test.version] : ["bad", c.test.error + testHint(c.test.error)];
  if (c.isNew) return ["", ""];
  if (c.pending) return ["", "Connexion en cours…"];
  return c.ok ? ["ok", "Connecté à " + c.version] : ["bad", (c.error || "injoignable") + testHint(c.error)];
}

// Que vérifier quand le test échoue faute de réponse (calcifer, quebecman,
// 22/10/26).
function testHint(err) {
  if (!/pas de réponse|connexion impossible/.test(err || "")) return "";
  if (ui.state && ui.state.mode === "docker") {
    return ". D3X tourne dans Docker : l'adresse doit être joignable depuis son conteneur (le nom du conteneur du client, ou l'adresse de la machine), jamais 127.0.0.1.";
  }
  return ". Ouvre cette adresse dans un navigateur sur la machine où tourne D3X : si l'interface du client ne s'affiche pas, le souci est entre les deux (adresse, port, pare-feu)." +
    (ui.state && ui.state.mode ? "" : " Si le client tourne sur ce PC, « Détecter sur ce PC » trouve la bonne adresse.");
}

function input(id, value, placeholder, onInput, type) {
  const n = el("input");
  n.type = type || "text";
  n.id = id;
  n.autocomplete = "off";
  n.spellcheck = false;
  n.value = value || "";
  if (placeholder) n.placeholder = placeholder;
  n.addEventListener("input", () => onInput(n.value));
  return n;
}

function labelFor(id, text) {
  const l = el("label", null, text);
  l.htmlFor = id;
  return l;
}

function renderClients() {
  const single = edit.clients.length <= 1;
  $("#clients-legend").textContent = single ? "Client torrent" : "Clients torrent";
  $("#clients-hint").textContent = single
    ? "Une seedbox et ce PC ? Ajoute l'autre client : D3X les interroge tous, et ajoute chaque cross-seed au client qui a déjà les fichiers."
    : "D3X les interroge tous et ajoute chaque cross-seed au client qui a déjà les fichiers ; s'ils sont dans plusieurs, au premier de la liste.";
  $("#clients").replaceChildren(...edit.clients.map((c, i) => clientCard(c, i, single)));
  clientAddButton();
  renderLinkDirs();
}

// « Ajouter un autre client » : pas tant que le seul client est vide.
function clientAddButton() {
  const b = $("#client-add");
  const single = edit.clients.length <= 1;
  b.textContent = single ? "Ajouter un autre client" : "Ajouter un client";
  b.disabled = edit.clients.length >= MAX_CLIENTS || (single && !(edit.clients[0] && edit.clients[0].url.trim()));
  b.title = edit.clients.length >= MAX_CLIENTS ? MAX_CLIENTS + " clients au plus" : "";
}

function clientCard(c, i, single) {
  const card = el("div", "client-card" + (single ? " single" : ""));
  const open = single || edit.open.has(c.id);
  if (!single) {
    const [cls, text] = editState(c);
    const head = el("div", "client-head");
    const dot = el("span", "dot " + cls);
    dot.setAttribute("aria-hidden", "true");
    const title = el("div", "client-title");
    const sub = cls === "ok" ? [c.test ? c.test.version : c.version, c.url] : [kindName(c.kind), c.url, text];
    title.append(el("b", null, editName(c)), el("span", "sub", sub.filter(Boolean).join(" · ")));
    title.title = text;
    const btn = (label, aria, fn) => {
      const b = el("button", "btn small", label);
      b.type = "button";
      if (aria) b.setAttribute("aria-label", aria);
      b.addEventListener("click", fn);
      return b;
    };
    head.append(dot, title);
    if (i > 0) head.append(btn("Monter", "Monter " + editName(c) + " dans la liste", () => {
      edit.clients.splice(i, 1);
      edit.clients.splice(i - 1, 0, c);
      renderClients();
    }));
    head.append(btn(open ? "Fermer" : "Modifier", null, () => {
      if (open) edit.open.delete(c.id);
      else edit.open.add(c.id);
      renderClients();
    }));
    head.append(btn("Retirer", "Retirer " + editName(c), () => {
      edit.clients = edit.clients.filter((x) => x !== c);
      if (!edit.clients.length) edit.clients.push(newClient());
      renderClients();
    }));
    card.append(head);
  }
  if (open) card.append(clientBody(c, i, single));
  return card;
}

function clientBody(c, i, single) {
  const body = el("div", "client-body");
  const id = (k) => "c" + i + "-" + k;
  if (!single) {
    body.append(labelFor(id("name"), "Nom"), input(id("name"), c.name, kindName(c.kind) + " (par exemple Seedbox, PC, NAS)", (v) => {
      c.name = v;
      renderLinkDirs();
    }));
  }
  const kind = el("select");
  kind.id = id("kind");
  for (const [k, v] of Object.entries(KINDS)) {
    const o = el("option", null, v.label || v.name);
    o.value = k;
    kind.append(o);
  }
  kind.value = c.kind;
  const hint = el("p", "hint");
  const url = input(id("url"), c.url, "", (v) => {
    c.url = v;
    c.test = null;
    clientAddButton();
  });
  const user = input(id("user"), c.user, "", (v) => { c.user = v; c.test = null; });
  const pass = input(id("pass"), "", c.hasPass ? "Enregistré (laisse vide pour le garder)" : "", (v) => { c.pass = v; c.test = null; }, "password");
  const kindHint = () => {
    const k = KINDS[c.kind] || KINDS.qbittorrent;
    url.placeholder = k.url;
    hint.textContent = k.help + (ui.state && ui.state.mode === "docker" ? " Dans Docker, mets l'adresse du conteneur du client (http://qbittorrent:8080) ou celle de la machine, jamais 127.0.0.1." : "");
    user.disabled = !k.user;
    if (!k.user) { user.value = ""; c.user = ""; }
  };
  kind.addEventListener("change", () => {
    c.kind = kind.value;
    c.test = null;
    kindHint();
  });
  kindHint();
  const kindBox = el("div");
  kindBox.append(labelFor(id("kind"), "Client"), kind);
  body.append(kindBox, hint, labelFor(id("url"), "Adresse"), url);
  const two = el("div", "two");
  const u = el("div");
  u.append(labelFor(id("user"), "Utilisateur"), user);
  const pw = el("div");
  pw.append(labelFor(id("pass"), "Mot de passe"), pass);
  two.append(u, pw);
  const ins = el("label", "check");
  const cb = el("input");
  cb.type = "checkbox";
  cb.checked = !!c.insecure;
  cb.addEventListener("change", () => { c.insecure = cb.checked; });
  ins.append(cb, document.createTextNode(" Accepter un certificat non vérifié (seedbox en https auto-signé)"));
  body.append(two, ins);

  // Essai de connexion de ce client seul, sans rien enregistrer.
  const status = el("p", "status");
  const [cls, text] = editState(c);
  status.className = "status" + (cls ? " " + cls : "");
  status.textContent = text;
  const test = el("button", "btn small", "Tester la connexion");
  test.type = "button";
  test.addEventListener("click", async () => {
    if (!c.url.trim()) return toast("Indique l'adresse du client.", "bad");
    test.disabled = true;
    test.textContent = "Test en cours…";
    try {
      c.test = await api("/ui/client/test", clientPayload(c));
    } catch (e) {
      if (e.message === "session") return;
      c.test = { ok: false, error: e.message };
    }
    renderClients();
  });
  const actions = el("div", "row-actions");
  actions.append(test);
  body.append(actions, status);

  // Dossiers de ce client : une ligne « ici → dans le client » par dossier.
  body.append(el("p", "sublegend", "Dossiers du client"), el("p", "hint", "Si ce client tourne ailleurs (NAS, Docker, seedbox), il ne nomme pas les dossiers comme cette machine. Une ligne par dossier : à gauche son chemin ici, à droite le même dossier vu par le client. D3X s'en sert pour trouver les fichiers d'une release et pour la mettre en seed."));
  const maps = el("div", "maps");
  const addRow = (rule) => maps.append(mapRow(c, rule));
  for (const r of c.pathMap) addRow(r);
  const add = el("button", "btn small", "Ajouter un dossier");
  add.type = "button";
  add.addEventListener("click", () => {
    const r = { from: "", to: "" };
    c.pathMap.push(r);
    addRow(r);
    maps.lastChild.querySelector("input").focus();
  });
  body.append(maps, add);
  return body;
}

// Une ligne de dossiers d'un client.
function mapRow(c, rule) {
  const row = el("div", "map-row");
  const from = input("", rule.from, ui.state && ui.state.mode ? "/media/films (ici)" : "Z:\\Films (ce PC)", (v) => { rule.from = v; });
  from.removeAttribute("id");
  from.setAttribute("aria-label", "Dossier sur cette machine");
  const to = input("", rule.to, "/data/films (le client)", (v) => { rule.to = v; });
  to.removeAttribute("id");
  to.setAttribute("aria-label", "Le même dossier vu par le client");
  const del = el("button", "btn small", "Retirer");
  del.type = "button";
  del.setAttribute("aria-label", "Retirer cette ligne");
  del.addEventListener("click", () => {
    c.pathMap = c.pathMap.filter((x) => x !== rule);
    row.remove();
  });
  row.append(from, el("span", "arrow", "→"), to, del);
  return row;
}

// Liens physiques : le dossier des liens de chaque client, tel qu'il le voit.
function renderLinkDirs() {
  const box = $("#l-dirs");
  const single = edit.clients.length <= 1;
  box.replaceChildren();
  edit.clients.forEach((c, i) => {
    const id = "l-dir-" + i;
    box.append(labelFor(id, single ? "Dossier des liens, tel que ton client le voit" : "Dossier des liens, tel que « " + editName(c) + " » le voit"), input(id, c.linksDir, "/data/torrents/v3x-liens", (v) => { c.linksDir = v; linksWarn(); }));
  });
  linksWarn();
}

// Liens cochés sans dossier des liens : D3X n'en fait aucun pour ce client et
// repasse par le renommage dans le client, sans rien dire (odeuxcool, 24/10/26).
function linksWarn() {
  const n = $("#l-warn");
  const empty = $("#l-on").checked ? edit.clients.filter((c) => !c.linksDir.trim()) : [];
  n.classList.toggle("hidden", !empty.length);
  if (!empty.length) return;
  n.textContent = edit.clients.length <= 1
    ? "Le dossier des liens est vide : sans lui, D3X ne fait aucun lien et repasse par le renommage dans le client."
    : "Dossier des liens vide pour " + empty.map((c) => "« " + editName(c) + " »").join(", ") + " : pour ce client, D3X ne fait aucun lien et repasse par le renommage.";
}

// Ce que la page envoie d'un client (identifiant vide : client neuf).
function clientPayload(c) {
  return {
    id: c.isNew ? "" : c.id,
    name: (c.name || "").trim(),
    kind: c.kind,
    url: c.url.trim(),
    user: c.user.trim(),
    pass: c.pass,
    insecure: !!c.insecure,
    pathMap: c.pathMap.map((r) => ({ from: r.from.trim(), to: r.to.trim() })).filter((r) => r.from || r.to),
    linksDir: c.linksDir.trim(),
  };
}

// ---------- seedbox (SSH) ----------

function fillSSH() {
  const x = (ui.state && ui.state.ssh) || {};
  $("#s-host").value = x.host || "";
  $("#s-port").value = x.port || "";
  $("#s-user").value = x.user || "";
  $("#s-key").value = x.keyPath || "";
  $("#s-secret").value = "";
  $("#s-forget").classList.toggle("hidden", !x.configured);
  $("#s-hostkey").classList.add("hidden");
  // Seedboxes déjà connectées (GoodMoviesForBadPeople, 21/10/26) : en choisir
  // une remplit le formulaire, « Se connecter » passe dessus.
  const saved = x.saved || [];
  const sel = $("#s-saved");
  sel.replaceChildren();
  for (const [i, o] of saved.entries()) {
    const opt = el("option", null, o.label + (o.port ? ":" + o.port : "") + (i === 0 && x.configured ? " (actuelle)" : ""));
    opt.value = String(i);
    sel.append(opt);
  }
  $("#s-saved-row").classList.toggle("hidden", saved.length < 2);
  setStatus("#ssh-status", x.configured, x.ok, x.ok ? "Connecté à " + x.label + (x.system ? " (" + x.system + ")" : "") : x.error || "Pas connectée : redonne le mot de passe (ou la phrase de passe de ta clé), puis « Se connecter ».");
}

// Réponse du serveur : état complet (connecté) ou clé d'hôte à vérifier.
function sshResult(r) {
  if (r && r.hostKey) {
    const box = $("#s-hostkey");
    box.classList.toggle("bad", !!r.hostKey.changed);
    $("#s-hostkey-txt").textContent = r.hostKey.changed
      ? "La clé de la seedbox a CHANGÉ depuis ta dernière connexion. Ça arrive après une réinstallation, mais c'est aussi le signe d'une interception. Vérifie auprès de ton hébergeur avant d'accepter :"
      : "Première connexion à cette seedbox. Vérifie que cette empreinte est bien celle de ta seedbox (ton hébergeur la donne, ou « ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub » sur la seedbox) :";
    $("#s-hostkey-fp").textContent = r.hostKey.fingerprint;
    box.dataset.fp = r.hostKey.fingerprint;
    box.classList.remove("hidden");
    return;
  }
  renderState(r);
  fillSSH();
  if (r.ssh && r.ssh.ok) toast("Seedbox connectée : " + r.ssh.label + ".", "ok");
}

async function sshCall(path, body, btn) {
  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = "Connexion…";
  try {
    sshResult(await api(path, body));
  } catch (e) {
    if (e.message !== "session") setStatus("#ssh-status", true, false, e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = label;
  }
}

function bindSSH() {
  $("#s-connect").addEventListener("click", () => sshCall("/ui/ssh/test", {
    host: $("#s-host").value,
    port: Number($("#s-port").value) || 0,
    user: $("#s-user").value,
    keyPath: $("#s-key").value,
    secret: $("#s-secret").value,
  }, $("#s-connect")));
  $("#s-secret").addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); $("#s-connect").click(); } });
  $("#s-saved").addEventListener("change", (ev) => {
    const o = ((ui.state && ui.state.ssh && ui.state.ssh.saved) || [])[Number(ev.target.value)];
    if (!o) return;
    $("#s-host").value = o.host || "";
    $("#s-port").value = o.port || "";
    $("#s-user").value = o.user || "";
    $("#s-key").value = o.keyPath || "";
    $("#s-secret").value = "";
    $("#s-secret").focus();
    const st = $("#ssh-status");
    st.className = "status";
    st.textContent = "Seedbox " + o.label + " choisie : mot de passe (ou phrase de passe de ta clé) si elle en demande un, puis « Se connecter ».";
  });
  $("#s-accept").addEventListener("click", () => sshCall("/ui/ssh/accept", { fingerprint: $("#s-hostkey").dataset.fp || "" }, $("#s-accept")));
  $("#s-reject").addEventListener("click", () => {
    $("#s-hostkey").classList.add("hidden");
    setStatus("#ssh-status", true, false, "Connexion abandonnée : empreinte refusée.");
  });
  $("#s-forget").addEventListener("click", async () => {
    try { renderState(await api("/ui/ssh/forget", {})); fillSSH(); toast("Seedbox oubliée.", "ok"); } catch (e) { if (e.message !== "session") toast(e.message, "bad"); }
  });
}

// Nom des fichiers locaux : ce PC, ou la machine de D3X en mode serveur.
function localLabel() {
  return ui.state && ui.state.mode ? "Ce serveur" : "Ce PC";
}

// Sélecteur « Ce PC / Seedbox » d'une vue. Caché tant qu'aucune seedbox
// n'est réglée ; une seedbox pas connectée ouvre les réglages.
function renderSourceSwitch(box, current, onPick) {
  const x = (ui.state && ui.state.ssh) || {};
  box.replaceChildren();
  if (!x.configured) return false;
  const mk = (id, parts) => {
    const b = el("button");
    b.type = "button";
    b.setAttribute("role", "radio");
    b.setAttribute("aria-checked", String(current === id));
    b.append(...parts);
    b.addEventListener("click", () => {
      if (id === "ssh" && !x.ok) {
        openSettings();
        $("#s-secret").focus();
        toast("Connecte d'abord la seedbox (mot de passe ou phrase de passe de ta clé).", "bad");
        return;
      }
      if (id !== current) onPick(id);
    });
    return b;
  };
  const dot = el("span", "dot " + (x.ok ? "ok" : "bad"));
  dot.setAttribute("aria-hidden", "true");
  box.append(mk("local", [document.createTextNode(localLabel())]), mk("ssh", [dot, el("span", "host", x.label)]));
  return true;
}

function setStatus(sel, configured, ok, text) {
  const n = $(sel);
  n.className = "status" + (configured ? (ok ? " ok" : " bad") : "");
  n.textContent = configured ? text || "" : "";
}

function openSettings() {
  fillSettings();
  $("#settings").showModal();
}

async function saveSettings(ev) {
  ev.preventDefault();
  const btn = $("#settings-save");
  btn.disabled = true;
  btn.textContent = "Test en cours…";
  try {
    const s = await api("/ui/settings", {
      v3xKey: $("#f-key").value,
      clients: edit.clients.map(clientPayload),
      category: $("#f-cat").value,
      pubCategory: $("#f-pubcat").value,
      discogsToken: $("#f-discogs").value.trim(),
      discogsClear: $("#f-discogs-clear").checked,
      tag: $("#f-tag").value,
      checkUpdates: $("#f-updates").checked,
      uiTheme: $("#f-theme").value,
      uiAccent: (($("#f-accent").querySelector('[aria-checked="true"]') || {}).dataset || {}).accent || "",
      links: { on: $("#l-on").checked, symbolic: $("#l-sym").checked },
      watch: {
        on: $("#w-on").checked,
        minutes: Number($("#w-min").value) || 30,
        notify: $("#w-notify").checked,
        autoAdd: $("#w-auto").checked,
        autostart: $("#w-start-row").classList.contains("hidden") ? null : $("#w-start").checked,
      },
    });
    previewing = false;
    renderState(s);
    fillSettings();
    const cl = s.clients || [];
    if (s.v3x.ok && cl.length && cl.every((c) => c.ok)) {
      toast(cl.length > 1 ? "Réglages enregistrés : V3X et tes " + cl.length + " clients répondent." : "Réglages enregistrés, les deux connexions marchent.", "ok");
      $("#settings").close();
    }
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  } finally {
    btn.disabled = false;
    btn.textContent = "Enregistrer et tester";
  }
}

// Aide propre à chaque client.
const KINDS = {
  qbittorrent: { name: "qBittorrent", url: "http://127.0.0.1:8080", user: true, help: "Outils ▸ Options ▸ Interface Web : coche « Interface Web » et note le port. Plus bas, dans « Authentification », choisis un nom d'utilisateur et un mot de passe puis enregistre (qBittorrent n'en propose pas d'office depuis sa version 4.6) : ce sont eux que tu saisis ici. Version 4.5 ou plus." },
  transmission: { name: "Transmission", url: "http://127.0.0.1:9091", user: true, help: "Préférences ▸ Distant : autorise l'accès distant. Utilisateur et mot de passe seulement si tu les as activés." },
  deluge: { name: "Deluge", url: "http://127.0.0.1:8112", user: false, help: "L'interface web de Deluge (greffon WebUI) : seul son mot de passe est demandé, pas d'utilisateur." },
  rtorrent: { name: "rTorrent", label: "rTorrent / ruTorrent", url: "https://ta-seedbox/RPC2", user: true, help: "L'adresse XML-RPC de rTorrent (souvent /RPC2), avec l'identification de ta seedbox. ruTorrent n'est pas nécessaire." },
};

async function detectClients() {
  const ul = $("#detected");
  ul.replaceChildren(el("li", "muted", "Recherche sur ce PC…"));
  ul.classList.remove("hidden");
  try {
    const r = await api("/ui/client/detect");
    ul.replaceChildren();
    if (!r.found || !r.found.length) {
      ul.append(el("li", "muted", "Aucun client trouvé sur les ports habituels. Active son interface web, ou saisis son adresse à la main (NAS, seedbox)."));
      return;
    }
    for (const f of r.found) {
      const li = el("li");
      const known = edit.clients.some((c) => c.url.trim().replace(/\/+$/, "") === f.url);
      const b = el("button", "btn small", known ? "Déjà réglé" : "Ajouter");
      b.type = "button";
      b.disabled = known;
      b.addEventListener("click", () => {
        // Le seul client encore vide est rempli ; sinon, un client de plus.
        const only = edit.clients.length === 1 && !edit.clients[0].url.trim() ? edit.clients[0] : null;
        if (only) {
          only.kind = f.kind;
          only.url = f.url;
        } else if (edit.clients.length < MAX_CLIENTS) {
          const c = newClient(f.kind, f.url);
          c.name = edit.clients.some((x) => editName(x).toLowerCase() === "ce pc") ? "" : "Ce PC";
          edit.clients.push(c);
          edit.open.add(c.id);
        } else {
          return toast(MAX_CLIENTS + " clients au plus.", "bad");
        }
        ul.classList.add("hidden");
        renderClients();
      });
      li.append(el("span", null, f.name + " · " + f.url), b);
      ul.append(li);
    }
  } catch (e) {
    if (e.message !== "session") ul.replaceChildren(el("li", "bad", e.message));
  }
}

// ---------- analyse ----------

let lastStatus = null;

function renderStatus(st) {
  lastStatus = st;
  // Un lot tourne dans D3X et attend un MediaInfo : n'importe quel onglet
  // ouvert le calcule (sans binaire mediainfo côté D3X).
  if (st.lot && st.lot.mediainfoWanted && window.lotServeMediaInfo) window.lotServeMediaInfo(st.lot.mediainfoWanted);
  const sc = st.scan;
  const running = sc.phase === "list" || sc.phase === "read" || sc.phase === "match";
  $("#scan").classList.toggle("hidden", running || st.applying);
  $("#stop").classList.toggle("hidden", !running && !st.applying);
  $("#scan").textContent = sc.phase === "idle" ? (multiClients() ? "Analyser mes clients" : "Analyser mon client") : "Analyser à nouveau";

  const prog = $("#progress");
  if (running || st.applying) {
    prog.classList.remove("hidden");
    let label = "";
    let pct = 0;
    if (st.applying) {
      label = "Ajout " + Math.min(st.batchDone + 1, st.batch) + " / " + st.batch + " : " + (st.current || "…");
      pct = st.batch ? (st.batchDone / st.batch) * 100 : 0;
    } else if (sc.phase === "list") {
      label = multiClients() ? "Lecture de la liste de tes clients…" : "Lecture de la liste de ton client…";
    } else if (sc.phase === "read") {
      label = "Lecture des .torrent : " + sc.done + " / " + sc.total;
      pct = sc.total ? (sc.done / sc.total) * 100 : 0;
    } else {
      label = "Comparaison avec le catalogue de V3X…";
      pct = sc.total ? (sc.done / sc.total) * 100 : 100;
    }
    $("#progress-label").textContent = label;
    $("#progress-detail").textContent = sc.detail || "";
    $("#progress-fill").style.width = Math.max(3, Math.min(100, pct)) + "%";
  } else {
    prog.classList.add("hidden");
  }

  const err = $("#scan-error");
  err.textContent = sc.phase === "error" ? sc.error : "";
  err.classList.toggle("hidden", sc.phase !== "error");
  // Un client injoignable : l'analyse a continué avec les autres.
  const warn = $("#scan-warn");
  warn.textContent = sc.warn && sc.phase !== "error" ? sc.warn + ". L'analyse a continué avec les autres clients." : "";
  warn.classList.toggle("hidden", !warn.textContent);

  if (st.version !== ui.version) loadRows();
}

// Un seul chargement à la fois : pendant les vérifications, la liste change
// toutes les 3 s et se relisait à chaque seconde, les réponses s'empilaient et
// la page saccadait avec de gros lots (MasterMan, 23/10/26). Un changement
// arrivé pendant un chargement en relance un seul, à la fin.
let rowsLoading = false;
let rowsAgain = false;
async function loadRows() {
  if (rowsLoading) {
    rowsAgain = true;
    return;
  }
  rowsLoading = true;
  try {
    const r = await api("/ui/rows");
    ui.version = r.version;
    ui.rows = r.rows || [];
    const keys = new Set(ui.rows.filter(canAdd).map((x) => x.key));
    for (const k of ui.selected) if (!keys.has(k)) ui.selected.delete(k);
    const picks = new Set(ui.rows.filter(canPick).map((x) => x.key));
    for (const k of ui.picked) if (!picks.has(k)) ui.picked.delete(k);
    renderRows();
    // Demandes, teams et relais ne dépendent que des releases présentes : relus
    // quand elles changent (et toutes les 5 min, le site bouge aussi), pas à
    // chaque pas d'une vérification.
    const slot = Math.floor(Date.now() / 300000);
    const sig = (status) => {
      const k = ui.rows.filter((x) => x.status === status).map((x) => x.key);
      return k.length ? k.join("|") + "#" + slot : "";
    };
    const none = sig("none");
    const add = sig("add");
    if (ui.reqSig !== none && none) loadRequests(none);
    if (ui.polSig !== none && none) loadPolicies(none);
    if (ui.relSig !== add && add) loadRelays(add);
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  } finally {
    rowsLoading = false;
    if (rowsAgain) {
      rowsAgain = false;
      loadRows();
    }
  }
}

function tabOf(status) {
  return TABS.find((t) => t.statuses.includes(status)) || TABS[TABS.length - 1];
}

// Onglet d'une ligne : une release « noms différents » reste dans son onglet
// pendant son ajout et sa vérification.
function tabOfRow(r) {
  if (r.match && r.match.kind === "renamed" && ["queued", "checking", "failed"].includes(r.status)) return TABS.find((t) => t.id === "renamed");
  return tabOf(r.status);
}

// Ligne qu'on peut cocher pour l'ajouter.
function canAdd(r) {
  return r.status === "add" || (r.status === "renamed" && !!r.canAdd);
}

// Ligne « Pas sur V3X » qu'on peut choisir pour la publier.
function canPick(r) {
  return r.status === "none" && !!r.path;
}

function setOf(r) {
  return canPick(r) ? ui.picked : ui.selected;
}

// Filtre texte : chaque mot doit être dans le nom (celui du client ou de V3X),
// un mot précédé de « - » l'écarte. « 1080p MULTi -720p » ; vide = tout.
function filterMatch(r) {
  const words = ui.filter.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return true;
  const name = (r.name + " " + (r.match && r.match.name ? r.match.name : "")).toLowerCase();
  return words.every((w) => (w[0] === "-" && w.length > 1 ? !name.includes(w.slice(1)) : name.includes(w)));
}

function checkable(r) {
  // Une release ignorée cachée ne se coche pas avec « Tout cocher », ni une
  // release écartée par le filtre.
  if (!ui.showIgnored && ignorable(r) && ignoredSet().has(r.key)) return false;
  if (!filterMatch(r)) return false;
  return canAdd(r) || canPick(r);
}

// Demandes ouvertes sur V3X auxquelles répondent les releases « Pas sur V3X ».
async function loadRequests(sig) {
  ui.reqSig = sig;
  try {
    const r = await api("/ui/requests/match");
    if (ui.reqSig !== sig) return;
    ui.reqs = r.byKey || {};
    renderRows();
  } catch (e) {
    ui.reqs = {};
    if (ui.reqSig === sig) ui.reqSig = "";
  }
}

// Teams qui interdisent le réupload ou le soumettent à leur accord (demande
// d'Ekhstaz) : pastille sur la ligne, et case pour masquer les interdites.
async function loadPolicies(sig) {
  ui.polSig = sig;
  try {
    const r = await api("/ui/teams/policies");
    if (ui.polSig !== sig) return;
    ui.pols = r.byKey || {};
    renderRows();
  } catch (e) {
    ui.pols = {};
    if (ui.polSig === sig) ui.polSig = "";
  }
}

function hideForbidden() {
  try { return localStorage.getItem("d3x-hide-forbidden") === "1"; } catch (e) { return false; }
}

// Releases écartées par le membre : dans « Pas sur V3X », une source interdite
// sur V3X ou une release qu'il ne publiera pas (suggestion de Luci) ; dans « À
// ajouter » et « Noms différents », une release qu'il ne veut pas seeder pour
// l'instant (MasterMan, 21/10/26). Elles ne reviennent plus à chaque analyse.
// Gardé sur l'appareil.
const IGNORABLE = ["none", "add", "renamed"];
function ignorable(r) {
  return IGNORABLE.includes(r.status);
}
let ignCache = null;
function ignoredSet() {
  if (!ignCache) {
    try { ignCache = new Set(JSON.parse(localStorage.getItem("d3x-ignored") || "[]")); } catch (e) { ignCache = new Set(); }
  }
  return ignCache;
}

function toggleIgnored(r) {
  const s = ignoredSet();
  if (s.has(r.key)) s.delete(r.key);
  else {
    s.add(r.key);
    ui.picked.delete(r.key);
    ui.selected.delete(r.key);
  }
  try { localStorage.setItem("d3x-ignored", JSON.stringify([...s])); } catch (e) { /* rien */ }
  renderRows();
}

// Ignorée depuis Publier (Indrop, 28/10/26) : même liste que le bouton de la
// ligne, sans repasser par le cross-seed.
window.xsIgnore = (key) => {
  if (!key) return;
  const s = ignoredSet();
  s.add(key);
  ui.picked.delete(key);
  ui.selected.delete(key);
  try { localStorage.setItem("d3x-ignored", JSON.stringify([...s])); } catch (e) { /* rien */ }
  renderRows();
};

// Relais ouverts (releases rares lâchées, prime au repreneur).
async function loadRelays(sig) {
  ui.relSig = sig;
  try {
    const r = await api("/ui/relays");
    if (ui.relSig !== sig) return;
    ui.relays = r.byHash || {};
    ui.relayPrime = r.prime || 0;
    renderRows();
  } catch (e) {
    ui.relays = {};
    if (ui.relSig === sig) ui.relSig = "";
  }
}

function relayOf(r) {
  return r.match && r.match.infoHash ? ui.relays[r.match.infoHash.toLowerCase()] : null;
}

// Réanimation : relais primé, ou release morte qui rapporte la prime de
// résurrection à qui la ramène.
function reanimBadges(r) {
  const out = [];
  const m = r.match;
  if (!m || !["add", "renamed"].includes(r.status)) return out;
  const rl = relayOf(r);
  if (rl) {
    const b = el("span", "req relay", "Relais" + (ui.relayPrime ? " · +" + ui.relayPrime.toLocaleString("fr-FR") + " jetons" : ""));
    const until = rl.until ? new Date(rl.until).toLocaleDateString("fr-FR", { day: "numeric", month: "long" }) : "";
    b.title = "Lâchée par un membre qui ne peut plus la seeder : la prime va aux premiers repreneurs, après 24 h de seed" + (until ? " (relais ouvert jusqu'au " + until + ")" : "") + ".";
    out.push(b);
  }
  if (m.dead) {
    const b = el("span", "req dead", "Morte sur V3X" + (m.prime ? " · +" + m.prime.toLocaleString("fr-FR") + " jetons" : ""));
    b.title = "Plus personne ne la seede depuis 3 jours : ton seed la ramène. La prime de résurrection tombe une fois, après 24 h de seed, dès qu'elle a servi à quelqu'un.";
    out.push(b);
  }
  return out;
}

// Badges « Demandée sur V3X » d'une release (lien vers la demande).
function requestBadges(hits) {
  const out = [];
  for (const h of (hits || []).slice(0, 3)) {
    if (!UUID.test(h.id)) continue;
    const a = el("a", "req" + (h.mine ? " mine" : ""), h.mine ? "Ta demande sur V3X" : "Demandée sur V3X" + (h.reward > 0 ? " · " + h.reward.toLocaleString("fr-FR") + " jetons" : ""));
    a.href = "https://v3x.club/requests/" + h.id;
    a.target = "_blank";
    a.rel = "noopener";
    a.title = h.title + (h.year && !/\(\d{4}\)$/.test(h.title) ? " (" + h.year + ")" : "") + (h.mine ? " : tu ne peux pas remplir ta propre demande" : " : la prime va à qui la remplit");
    out.push(a);
  }
  return out;
}

// Propositions faites pendant cette session (la carte est redessinée souvent).
const proposed = new Set();

// Bouton « Proposer pour la demande » (release publiée et validée).
function proposeButton(h, torrentId) {
  const done = () => { b.textContent = "Proposée pour « " + h.title + " »"; b.disabled = true; };
  const b = el("button", "btn small primary", "Proposer pour « " + h.title + " »" + (h.reward > 0 ? " · " + h.reward.toLocaleString("fr-FR") + " jetons" : ""));
  b.type = "button";
  b.title = "Le demandeur reçoit ta proposition et la valide sur V3X : la prime t'est versée à ce moment-là.";
  if (proposed.has(h.id + "|" + torrentId)) done();
  b.addEventListener("click", async () => {
    b.disabled = true;
    try {
      await api("/ui/requests/propose", { requestId: h.id, torrentId });
      proposed.add(h.id + "|" + torrentId);
      done();
      toast("Proposée : le demandeur la valide sur V3X.", "ok");
    } catch (e) {
      if (e.message === "already_proposed") {
        proposed.add(h.id + "|" + torrentId);
        done();
      } else b.disabled = false;
      const msg = { already_proposed: "déjà proposée pour cette demande", not_open: "la demande n'est plus ouverte", self_fill: "c'est ta propre demande", torrent_not_found: "la release doit d'abord être validée" }[e.message];
      if (e.message !== "session") toast("Pas proposée : " + (msg || e.message) + ".", "bad");
    }
  });
  return b;
}

// Où D3X voit les releases choisies (ce PC ou la seedbox).
async function localPaths(rows) {
  const r = await api("/ui/rows/local", { keys: rows.map((x) => x.key) });
  const ok = [];
  const bad = [];
  for (const row of rows) {
    const p = r[row.key];
    if (p && p.path) ok.push({ row, path: p.path, source: p.source });
    else bad.push({ row, error: (p && p.error) || "emplacement inconnu : relance l'analyse" });
  }
  return { ok, bad };
}

// Detag d'une team qui interdit le réupload, ou d'un groupe refusé : V3X ne
// le verrait pas, D3X ne le publie pas (ni Publier ni le Lot).
function detagBlocked(r) {
  const p = ui.pols[r.key];
  return !!(p && p.detag && (p.policy === "forbidden" || p.policy === "banned"));
}

function detagText(r) {
  const p = ui.pols[r.key];
  return "Copie sans tag d'une release de " + p.team + (p.policy === "banned" ? ", groupe refusé sur V3X" : ", qui interdit le réupload") + " : D3X ne la publie pas.";
}

async function publishRow(r, btn) {
  if (detagBlocked(r)) return toast(detagText(r), "bad");
  if (btn) btn.disabled = true;
  try {
    const { ok, bad } = await localPaths([r]);
    if (bad.length) return toast(bad[0].error + ".", "bad");
    window.pubOpen(ok[0].path, ok[0].source, r.key);
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  } finally {
    if (btn) btn.disabled = false;
  }
}

async function lotPicked() {
  const all = ui.rows.filter((r) => ui.picked.has(r.key));
  const rows = all.filter((r) => !detagBlocked(r));
  const off = all.length - rows.length;
  if (off) toast(off + " " + plural(off, "copie sans tag laissée", "copies sans tag laissées") + " de côté : release d'une team qui interdit le réupload, ou d'un groupe refusé.", "bad");
  if (!rows.length) return;
  const btn = $("#pick-lot");
  btn.disabled = true;
  try {
    const { ok, bad } = await localPaths(rows);
    if (!ok.length) return toast(bad[0].error + ".", "bad");
    if (bad.length) toast(bad.length + " " + plural(bad.length, "release écartée", "releases écartées") + " : " + bad[0].error + ".", "bad");
    // Un lot lit un seul endroit : ce PC d'abord, la seedbox ensuite.
    const local = ok.filter((x) => x.source === "local");
    const use = local.length ? local : ok;
    if (use.length < ok.length) toast("Les releases de la seedbox passeront dans un second lot : celui-ci prend celles de " + localLabel().toLowerCase() + ".");
    const hits = {};
    for (const x of use) if (ui.reqs[x.row.key]) hits[x.path] = ui.reqs[x.row.key];
    window.lotFromPaths(use.map((x) => x.path), use[0].source, hits);
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  } finally {
    btn.disabled = false;
  }
}

function renderRows() {
  const counts = {};
  const ign = ignoredSet();
  const isIgnored = (r) => ignorable(r) && ign.has(r.key);
  const nIgnoredIn = {}; // par onglet
  for (const r of ui.rows) {
    if (isIgnored(r)) {
      const t = tabOfRow(r).id;
      nIgnoredIn[t] = (nIgnoredIn[t] || 0) + 1;
      if (!ui.showIgnored) continue;
    }
    const t = tabOfRow(r).id;
    counts[t] = (counts[t] || 0) + 1;
  }
  const has = ui.rows.length > 0;
  $("#tabs").classList.toggle("hidden", !has);
  $("#list-filter").classList.toggle("hidden", !has);
  $("#table-wrap").classList.toggle("hidden", !has);
  const tabs = $("#tabs");
  tabs.replaceChildren();
  for (const t of TABS) {
    // « Pas sur V3X » reste là tant que des releases y sont ignorées, sinon
    // « Les réafficher » devenait introuvable (Luci, 21/10/26).
    if (t.id !== "todo" && !counts[t.id] && !nIgnoredIn[t.id]) continue;
    const b = el("button", "tab");
    b.setAttribute("role", "tab");
    b.setAttribute("aria-selected", String(t.id === ui.tab));
    b.append(el("span", null, t.label), el("span", "n", String(counts[t.id] || 0)));
    b.addEventListener("click", () => {
      ui.tab = t.id;
      ui.shown = PAGE;
      renderRows();
    });
    tabs.append(b);
  }
  const tab = TABS.find((t) => t.id === ui.tab) || TABS[0];
  let list = ui.rows.filter((r) => tabOfRow(r).id === tab.id && (ui.showIgnored || !isIgnored(r)));
  const before = list.length;
  list = list.filter(filterMatch);
  $("#f-text-n").textContent = ui.filter.trim() ? list.length + " sur " + before : "";
  const ignLine = $("#ign-line");
  const nIgnored = nIgnoredIn[tab.id] || 0;
  ignLine.classList.toggle("hidden", !nIgnored);
  if (nIgnored) {
    $("#ign-text").textContent = nIgnored + " " + plural(nIgnored, "release ignorée", "releases ignorées") + (ui.showIgnored ? ", affichée" + (nIgnored > 1 ? "s" : "") + " avec les autres." : ".");
    $("#ign-toggle").textContent = ui.showIgnored ? "Les masquer" : "Les réafficher";
  }
  // Pas sur V3X : les releases d'une team qui interdit le réupload, masquées
  // sur demande ; puis celles qu'un membre demande d'abord, les mieux primées en tête.
  const blocked = (r) => ui.pols[r.key] && (ui.pols[r.key].policy === "forbidden" || ui.pols[r.key].policy === "banned");
  const forbidden = tab.id === "none" ? list.filter(blocked).length : 0;
  const filterBox = $("#none-filter");
  filterBox.classList.toggle("hidden", !forbidden);
  if (forbidden) {
    const cb = $("#f-hide-forbidden");
    cb.checked = hideForbidden();
    filterBox.lastChild.textContent = (forbidden === 1 ? " Masquer la release" : " Masquer les " + forbidden + " releases") + " que V3X refusera (team qui interdit le réupload, groupe refusé)";
    if (cb.checked) list = list.filter((r) => !blocked(r));
  }
  if (tab.id === "none") {
    const best = (r) => Math.max(-1, ...(ui.reqs[r.key] || []).filter((h) => !h.mine).map((h) => h.reward || 0));
    list = list.map((r, i) => [r, best(r), i]).sort((a, b) => b[1] - a[1] || a[2] - b[2]).map((x) => x[0]);
  }
  // À ajouter : les relais primés, puis les releases mortes, avant le reste.
  if (tab.id === "todo") {
    const rank = (r) => (relayOf(r) ? 0 : r.match && r.match.dead ? 1 : 2);
    list = list.map((r, i) => [r, rank(r), i]).sort((a, b) => a[1] - b[1] || a[2] - b[2]).map((x) => x[0]);
  }
  const tbody = $("#tbody");
  tbody.replaceChildren();
  for (const r of list.slice(0, ui.shown)) tbody.append(rowEl(r));

  const empty = $("#empty");
  let note = tab.note && list.length ? tab.note : "";
  if (tab.id === "none") {
    const asked = list.filter((r) => (ui.reqs[r.key] || []).some((h) => !h.mine)).length;
    if (asked) note = asked + " " + plural(asked, "répond", "répondent") + " à une demande ouverte sur V3X : en tête de liste, avec la prime. " + note;
  }
  if (tab.id === "todo") {
    const relays = list.filter((r) => relayOf(r)).length;
    const dead = list.filter((r) => !relayOf(r) && r.match && r.match.dead).length;
    const parts = [];
    if (relays) parts.push(relays + " " + plural(relays, "relais primé", "relais primés"));
    if (dead) parts.push(dead + " " + plural(dead, "release morte", "releases mortes") + " sur V3X");
    if (parts.length) note = parts.join(" et ") + " : en tête de liste, ton seed leur redonne vie et rapporte une prime.";
  }
  empty.textContent = list.length ? note : tab.empty;
  empty.classList.toggle("hidden", !empty.textContent);
  const more = $("#more");
  if (list.length > ui.shown) {
    more.replaceChildren(document.createTextNode(list.length - ui.shown + " de plus. "));
    const b = el("button", "btn small", "Afficher la suite");
    b.addEventListener("click", () => { ui.shown += PAGE; renderRows(); });
    more.append(b);
    more.classList.remove("hidden");
  } else {
    more.classList.add("hidden");
  }
  const addable = list.filter(checkable);
  const all = $("#check-all");
  all.disabled = !tab.selectable || addable.length === 0;
  all.checked = addable.length > 0 && addable.every((r) => setOf(r).has(r.key));
  renderSelection();
}

function rowEl(r) {
  const tr = el("tr");
  const m = r.match;
  const tdC = el("td", "c-check");
  if (checkable(r)) {
    const cb = el("input");
    cb.type = "checkbox";
    cb.checked = setOf(r).has(r.key);
    cb.setAttribute("aria-label", "Cocher " + r.name);
    cb.addEventListener("click", (ev) => { ev.stopPropagation(); toggle(r.key, cb.checked); });
    tdC.append(cb);
    tr.classList.add("selectable");
    tr.addEventListener("click", (ev) => {
      if (ev.target.closest("a,button")) return;
      cb.checked = !cb.checked;
      toggle(r.key, cb.checked);
    });
  }
  tr.append(tdC);

  const tdN = el("td");
  const name = el("div", "name");
  if (m && UUID.test(m.id)) {
    const a = el("a", null, m.name);
    a.href = "https://v3x.club/torrents/" + m.id;
    a.target = "_blank";
    a.rel = "noopener";
    name.append(a);
  } else {
    name.textContent = r.name;
  }
  if (r.copies > 1) {
    const c = el("span", "copies", "×" + r.copies);
    c.title = r.copies + " copies identiques dans " + (multiClients() ? "tes clients" : "ton client") + " : une seule suffit pour seeder sur V3X";
    name.append(c);
  }
  if (multiClients() && r.clientName) {
    const t = el("span", "ctag", r.clientName);
    t.title = canAdd(r) ? "Ajoutée à ce client, qui a déjà les fichiers" : "Dans ce client";
    name.append(t);
  }
  tdN.append(name);
  if (m && m.name !== r.name) tdN.append(el("div", "sub", "Dans ton client : " + r.name));
  if (r.detail) tdN.append(el("div", "sub", r.detail));
  const rb = reanimBadges(r);
  if (rb.length) {
    const box = el("div", "reqs");
    box.append(...rb);
    tdN.append(box);
  }
  if (r.status === "none" && ui.reqs[r.key]) {
    const reqs = el("div", "reqs");
    reqs.append(...requestBadges(ui.reqs[r.key]));
    if (reqs.children.length) tdN.append(reqs);
  }
  if (r.status === "none" && ui.pols[r.key]) {
    const p = ui.pols[r.key];
    const banned = p.policy === "banned";
    const forbid = banned || p.policy === "forbidden";
    const b = el("span", "pill " + (forbid ? "bad" : "warn"), (p.detag ? "Detag · " : "") + (banned ? "Groupe refusé sur V3X" : forbid ? "Réupload interdit" : "Réupload sur accord") + " · " + p.team);
    // Detag (Ekhstaz, 27/10/26) : sans le tag, V3X ne saurait pas la refuser ;
    // c'est D3X qui la reconnaît à la taille d'une copie taguée.
    const copy = p.detag ? "Cette release est une copie sans le tag (même taille au fichier près que « " + p.detag + " »). " : "";
    b.title = copy + (banned
      ? "Le groupe " + p.team + " est refusé sur V3X" + (p.reason ? " : " + p.reason : "") + (p.detag ? ", detag compris : ne la publie pas." : ". V3X refusera la publication.")
      : forbid
        ? "La team " + p.team + " n'autorise pas le réupload de ses releases" + (p.detag ? ", detag compris : ne la publie pas." : " : V3X refusera la publication.")
        : "La team " + p.team + " demande son accord avant un réupload" + (p.detag ? ", detag compris" : "") + " : écris-lui sur V3X avant de publier.");
    const box = el("div", "reqs");
    box.append(b);
    tdN.append(box);
  }
  tr.append(tdN);

  tr.append(el("td", "num", fmtSize(m ? m.size : r.size)));

  const tdS = el("td", "num seeds");
  if (m) {
    const b = el("b", m.seeders <= 1 ? "rare" : null, String(m.seeders));
    b.title = "sources";
    tdS.append(b, document.createTextNode(" / " + m.leechers));
    if (m.seeders <= 1 && r.status === "add") tdS.title = "Release presque sans source : ton seed compte double";
  } else {
    tdS.textContent = "";
  }
  tr.append(tdS);

  const tdE = el("td");
  const box = el("div", "state");
  const [label, kind] = r.status === "renamed" && r.canAdd
    ? ["À ajouter sous tes noms", "add"]
    : r.status === "same_video" && r.match && r.match.kind === "same_file"
      ? ["Même fichier sur V3X", ""] // image disque d'un jeu, même nom de release (1.1.25)
      : STATUS[r.status] || [r.status, ""];
  box.append(el("span", "pill " + kind, label));
  if (r.status === "checking" && r.progress > 0) {
    const bar = el("div", "mini");
    const i = el("i");
    i.style.width = Math.round(r.progress * 100) + "%";
    bar.append(i);
    bar.title = Math.round(r.progress * 100) + " %";
    box.append(bar);
  }
  if (canPick(r)) {
    const b = el("button", "btn small", "Publier");
    b.type = "button";
    b.title = "Ouvre la fiche de cette release dans Publier : le .torrent est fabriqué sur ses fichiers, puis elle part en seed.";
    b.addEventListener("click", () => publishRow(r, b));
    box.append(b);
  }
  if (ignorable(r)) {
    const ignored = ignoredSet().has(r.key);
    const b = el("button", "btn small", ignored ? "Ne plus ignorer" : "Ignorer");
    b.type = "button";
    b.title = ignored
      ? "La release revient dans la liste."
      : r.status === "none"
        ? "Écarte cette release de « Pas sur V3X » (source interdite sur V3X, release que tu ne publieras pas) : elle ne revient plus à chaque analyse."
        : "Écarte cette release de la liste (tu ne veux pas la seeder pour l'instant) : elle ne revient plus à chaque analyse, et « Tout cocher » la laisse de côté.";
    b.addEventListener("click", () => toggleIgnored(r));
    box.append(b);
  }
  if (DIAG_STATUSES.includes(r.status)) {
    const b = el("button", "btn small", "Diagnostic");
    b.type = "button";
    b.title = "Copie ce que D3X sait de cette release (nom, fichiers et tailles, ce qu'il en a conclu), sans info-hash ni chemin : à coller pour le staff si le classement te paraît faux.";
    b.addEventListener("click", () => copyDiag(r, b));
    box.append(b);
  }
  if (r.status === "failed") {
    const rb = el("button", "btn small", "Revérifier");
    rb.title = "Relance la vérification dans ton client : la release démarre si elle atteint 100 %.";
    rb.addEventListener("click", () => recheckRow(r, rb));
    box.append(rb);
    const b = el("button", "btn small", "Retirer du client");
    b.title = "Retire ce torrent de ton client. Tes fichiers ne sont pas touchés.";
    b.addEventListener("click", () => removeRow(r, b));
    box.append(b);
  }
  tdE.append(box);
  tr.append(tdE);
  return tr;
}

// Lignes dont le classement peut surprendre : leur diagnostic se copie (1.1.28,
// Ori de Nayr).
const DIAG_STATUSES = ["none", "same_video", "renamed", "in_client", "partial", "error", "gone_v3x"];

async function copyDiag(r, btn) {
  btn.disabled = true;
  try {
    const d = await api("/ui/rows/diag?key=" + encodeURIComponent(r.key));
    const ta = $("#diag-text");
    ta.value = d.text;
    $("#diag").showModal();
    ta.focus();
    ta.select();
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  } finally {
    btn.disabled = false;
  }
}

// Copie dans le presse-papiers ; repli pour une page ouverte en http sur le
// réseau (Docker, serveur), où le presse-papiers moderne est refusé.
async function copyText(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch (e) { /* repli */ }
  const ta = el("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.append(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
  ta.remove();
  return ok;
}

function toggle(key, on) {
  const row = ui.rows.find((r) => r.key === key);
  const set = row ? setOf(row) : ui.selected;
  if (on) set.add(key);
  else set.delete(key);
  renderSelection();
  const tab = TABS.find((t) => t.id === ui.tab);
  const addable = ui.rows.filter((r) => tabOfRow(r).id === tab.id && checkable(r));
  $("#check-all").checked = addable.length > 0 && addable.every((r) => setOf(r).has(r.key));
}

function renderSelection() {
  const sel = ui.rows.filter((r) => ui.selected.has(r.key));
  const n = sel.length;
  const size = sel.reduce((a, r) => a + (r.match ? r.match.size : r.size), 0);
  $("#sel-count").textContent = String(n);
  $("#sel-label").textContent = plural(n, "release cochée", "releases cochées");
  $("#sel-size").textContent = fmtSize(size);
  const none = ui.tab === "none";
  $("#actionbar").classList.toggle("hidden", n === 0 || ui.view !== "cross" || none);
  $("#apply").textContent = multiClients() ? "Ajouter à mes clients" : "Ajouter à mon client";
  const picked = ui.rows.filter((r) => ui.picked.has(r.key));
  const p = picked.length;
  $("#pick-count").textContent = String(p);
  $("#pick-label").textContent = plural(p, "release choisie", "releases choisies");
  $("#pick-size").textContent = fmtSize(picked.reduce((a, r) => a + r.size, 0));
  $("#pick-publish").disabled = p !== 1;
  $("#pick-publish").title = p === 1 ? "" : "Publier ouvre une release à la fois : le Lot en prend plusieurs";
  $("#pick-bar").classList.toggle("hidden", p === 0 || ui.view !== "cross" || !none);
}

async function applySelected() {
  const keys = [...ui.selected];
  if (!keys.length) return;
  const btn = $("#apply");
  btn.disabled = true;
  try {
    const r = await api("/ui/apply", { keys });
    ui.selected.clear();
    toast(r.queued + " " + plural(r.queued, "release mise", "releases mises") + " en file. Ton client les vérifie une par une.", "ok");
    poll();
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  } finally {
    btn.disabled = false;
  }
}

async function recheckRow(r, btn) {
  btn.disabled = true;
  try {
    await api("/ui/recheck", { key: r.key });
    toast("Vérification relancée dans ton client.", "ok");
    poll();
  } catch (e) {
    btn.disabled = false;
    if (e.message !== "session") toast(e.message, "bad");
  }
}

async function removeRow(r, btn) {
  btn.disabled = true;
  try {
    await api("/ui/remove", { key: r.key });
    toast("Retiré de ton client, fichiers intacts.", "ok");
    poll();
  } catch (e) {
    btn.disabled = false;
    if (e.message !== "session") toast(e.message, "bad");
  }
}

// ---------- historique ----------

const KIND = { crossseed: "En seed", crossseed_failed: "Laissé à l'arrêt", removed: "Retiré" };

async function loadHistory() {
  const ul = $("#history");
  try {
    const r = await api("/ui/history");
    ul.replaceChildren();
    if (!r.events || !r.events.length) {
      ul.append(el("li", "empty", "Rien pour l'instant."));
      return;
    }
    for (const e of r.events) {
      const li = el("li");
      const t = el("time", null, new Date(e.at).toLocaleString("fr-FR", { dateStyle: "short", timeStyle: "short" }));
      t.dateTime = e.at;
      li.append(t, el("span", "pill " + (e.kind === "crossseed" ? "ok" : e.kind === "crossseed_failed" ? "bad" : ""), KIND[e.kind] || e.kind));
      const w = el("div", "what");
      if (e.id && UUID.test(e.id)) {
        const a = el("a", null, e.name);
        a.href = "https://v3x.club/torrents/" + e.id;
        a.target = "_blank";
        a.rel = "noopener";
        w.append(a);
      } else {
        w.textContent = e.name;
      }
      if (e.detail) w.append(el("div", "sub", e.detail));
      li.append(w);
      ul.append(li);
    }
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

// ---------- vues et boucle ----------

// Historique du navigateur (sultar, 22/10/26) : chaque vue a son adresse
// (#lot, #publier…). Le retour arrière revient à la vue d'avant au lieu de
// quitter D3X, et un rechargement de la page reste sur la même vue.
const VIEW_HASH = { cross: "cross-seed", seed: "mon-seed", library: "mediatheque", publish: "publier", batch: "lot", history: "historique" };

function viewFromHash() {
  const h = decodeURIComponent(location.hash.slice(1));
  return Object.keys(VIEW_HASH).find((v) => VIEW_HASH[v] === h) || null;
}

function setView(v, fromHistory) {
  if (!VIEW_HASH[v]) v = "cross";
  if (!fromHistory && viewFromHash() !== v) history.pushState(null, "", "#" + VIEW_HASH[v]);
  ui.view = v;
  for (const b of document.querySelectorAll(".view")) {
    if (b.dataset.view === v) b.setAttribute("aria-current", "page");
    else b.removeAttribute("aria-current");
  }
  $("#view-cross").classList.toggle("hidden", v !== "cross");
  $("#view-history").classList.toggle("hidden", v !== "history");
  $("#view-publish").classList.toggle("hidden", v !== "publish");
  $("#view-batch").classList.toggle("hidden", v !== "batch");
  $("#view-seed").classList.toggle("hidden", v !== "seed");
  $("#view-library").classList.toggle("hidden", v !== "library");
  if (v === "history") loadHistory();
  if (window.pubShow) window.pubShow(v === "publish");
  if (window.batchShow) window.batchShow(v === "batch");
  if (window.seedShow) window.seedShow(v === "seed");
  if (window.libShow) window.libShow(v === "library");
  renderSelection();
}

let timer = null;
async function poll() {
  clearTimeout(timer);
  let busy = false;
  try {
    const st = await api("/ui/status");
    renderStatus(st);
    const sc = st.scan.phase;
    busy = sc === "list" || sc === "read" || sc === "match" || st.applying || (st.counts.checking || 0) > 0;
  } catch (e) {
    if (e.message === "session") return;
  }
  timer = setTimeout(poll, busy ? 1000 : 5000);
}

// Présence : un signal toutes les 20 s, et un au revoir à la fermeture. Sous
// Windows, D3X s'arrête quand sa fenêtre est fermée (sauf travail en cours).
// Le signal dit aussi si la page est vraiment à l'écran (onglet visible ET une
// image dessinée) : une fenêtre qui charge sa page sans jamais l'afficher
// (Luci, 21/10/26) est remplacée par D3X, et se ferme quand il le lui dit.
const PAGE_ID = Math.random().toString(36).slice(2, 12);

function drawn() {
  return new Promise((res) => {
    let done = false;
    requestAnimationFrame(() => { done = true; res(true); });
    setTimeout(() => { if (!done) res(false); }, 1000);
  });
}

async function ping() {
  try {
    const shown = document.visibilityState === "visible" && (await drawn());
    const r = await fetch("/ui/ping?id=" + PAGE_ID + "&shown=" + (shown ? 1 : 0));
    if (r.status === 200 && (await r.json()).close) window.close();
  } catch (e) { /* rien */ }
}

function presence() {
  setTimeout(ping, 1500);
  setInterval(ping, 20000);
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") ping(); });
  window.addEventListener("pagehide", () => {
    try { navigator.sendBeacon("/ui/bye", new Blob([JSON.stringify({ id: PAGE_ID })], { type: "application/json" })); } catch (e) { /* rien */ }
  });
}

async function boot() {
  presence();
  for (const b of document.querySelectorAll("[data-open-settings]")) b.addEventListener("click", openSettings);
  for (const b of document.querySelectorAll(".view")) b.addEventListener("click", () => setView(b.dataset.view));
  window.addEventListener("popstate", () => setView(viewFromHash() || "cross", true));
  $("#settings-form").addEventListener("submit", saveSettings);
  bindSSH();
  $("#detect").addEventListener("click", detectClients);
  $("#client-add").addEventListener("click", () => {
    if (edit.clients.length >= MAX_CLIENTS) return;
    const c = newClient();
    edit.clients.push(c);
    edit.open.add(c.id);
    renderClients();
    const inp = $("#clients").lastChild.querySelector("input");
    if (inp) inp.focus();
  });
  $("#settings-cancel").addEventListener("click", () => $("#settings").close());
  // Liens symboliques = des liens : cocher l'un coche l'autre, décocher les
  // liens décoche les symboliques (odeuxcool, 24/10/26).
  $("#l-sym").addEventListener("change", () => {
    if ($("#l-sym").checked) $("#l-on").checked = true;
    linksWarn();
  });
  $("#l-on").addEventListener("change", () => {
    if (!$("#l-on").checked) $("#l-sym").checked = false;
    linksWarn();
  });
  $("#f-theme").addEventListener("change", previewAppearance);
  // Fermé sans enregistrer : retour à l'apparence en vigueur.
  $("#settings").addEventListener("close", () => {
    if (previewing && ui.state && ui.state.appearance) {
      previewing = false;
      applyAppearance(ui.state.appearance.theme, ui.state.appearance.accent);
    }
  });
  $("#apply").addEventListener("click", applySelected);
  $("#pick-lot").addEventListener("click", lotPicked);
  $("#pick-publish").addEventListener("click", () => {
    const r = ui.rows.find((x) => ui.picked.has(x.key));
    if (r) publishRow(r, $("#pick-publish"));
  });
  $("#check-all").addEventListener("change", (ev) => {
    const tab = TABS.find((t) => t.id === ui.tab);
    for (const r of ui.rows) {
      if (tabOfRow(r).id !== tab.id || !checkable(r)) continue;
      if (ev.target.checked) setOf(r).add(r.key);
      else setOf(r).delete(r.key);
    }
    renderRows();
  });
  $("#f-text").addEventListener("input", (ev) => {
    ui.filter = ev.target.value;
    // Ce que le filtre cache ne reste pas coché : « Tout cocher » puis
    // « Ajouter » ne prennent que ce qui est affiché.
    for (const r of ui.rows) {
      if (filterMatch(r)) continue;
      ui.selected.delete(r.key);
      ui.picked.delete(r.key);
    }
    ui.shown = PAGE;
    renderRows();
  });
  $("#diag-close").addEventListener("click", () => $("#diag").close());
  $("#diag-copy").addEventListener("click", async () => {
    const ta = $("#diag-text");
    if (await copyText(ta.value)) {
      $("#diag").close();
      toast("Diagnostic copié : colle-le dans ton message au staff.", "ok");
    } else {
      ta.focus();
      ta.select();
      toast("Copie bloquée par le navigateur : le texte est sélectionné, fais Ctrl+C.", "bad");
    }
  });
  $("#f-hide-forbidden").addEventListener("change", (ev) => {
    try { localStorage.setItem("d3x-hide-forbidden", ev.target.checked ? "1" : "0"); } catch (e) { /* rien */ }
    renderRows();
  });
  $("#ign-toggle").addEventListener("click", () => {
    ui.showIgnored = !ui.showIgnored;
    renderRows();
  });
  $("#scan").addEventListener("click", async () => {
    try {
      renderStatus(await api("/ui/scan", {}));
      poll();
    } catch (e) {
      if (e.message !== "session") toast(e.message, "bad");
    }
  });
  $("#stop").addEventListener("click", async () => {
    try { renderStatus(await api("/ui/stop", {})); } catch (e) { /* rien */ }
  });

  try {
    const s = await api("/ui/state");
    renderState(s);
    // Vue de l'adresse (rechargement, lien gardé) ; sinon la première vue
    // prend son adresse, sans entrée de plus dans l'historique.
    const start = viewFromHash();
    if (start && start !== ui.view) setView(start, true);
    else if (!start) history.replaceState(null, "", "#" + (VIEW_HASH[ui.view] || VIEW_HASH.cross));
    try {
      const v = sessionStorage.getItem("d3x-updated");
      if (v) {
        sessionStorage.removeItem("d3x-updated");
        toast("D3X est passé en " + v + ".", "ok");
      }
    } catch (e) { /* rien */ }
    // Connexions vérifiées en tâche de fond au lancement : on relit l'état une
    // fois, puis encore à 15 s et 45 s, le temps que la recherche de version
    // (5 s après le lancement) trouve et vérifie une mise à jour.
    for (const ms of [2500, 15000, 45000]) {
      setTimeout(async () => { try { renderState(await api("/ui/state")); } catch (e) { /* rien */ } }, ms);
    }
    $("#ver-check").addEventListener("click", checkForUpdate);
    // L'état (dont la mise à jour) est relu toutes les 5 minutes.
    setInterval(async () => { try { renderState(await api("/ui/state")); } catch (e) { /* rien */ } }, 300000);
  } catch (e) {
    return;
  }
  poll();
}

boot();
