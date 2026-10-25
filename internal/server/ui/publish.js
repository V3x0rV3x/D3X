"use strict";

// Vue « Publier ». Utilise les outils de app.js ($, el, api, toast, fmtSize).
// Tout texte venu d'une release ou du site passe par textContent, sauf l'aperçu
// de la présentation : c'est le HTML rendu par V3X lui-même (le même que sur le
// site), et la politique de sécurité de la page bloque tout script en ligne.
// Une présentation écrite en HTML s'affiche dans un cadre isolé, sans scripts.

const pub = {
  ctx: null, // contexte du membre (catégories, équipe, langues, modèles)
  ctxError: "",
  dir: "",
  source: "local", // local ou ssh
  job: null,
  fiche: null,
  mediainfo: "",
  miState: "idle", // idle, running, done, none, error
  tmdb: null, // { type, id, title, year, posterUrl }
  game: null, // jeu vidéo choisi : { id, name, year, coverUrl }
  book: null, // livre : { ref } (lien Open Library ou ISBN)
  versionsKey: "", // dernière demande des versions déjà sur V3X
  dirty: {}, // champs retouchés par le membre : jamais écrasés par la fiche
  ficheSeq: 0,
  previewSeq: 0,
  timer: null,
  shown: false,
  published: false,
  fromLot: "", // release du Lot ouverte par « Revoir » (sinon vide)
  format: "standard", // standard (BBCode) ou html, si V3X l'autorise au membre
  localNfo: "", // .nfo de la release, lu sur le disque (option « Reprendre le .nfo »)
};

// ---------- .nfo de la release (quebecman, 23/10/26) ----------

// Option gardée sur l'appareil : le .nfo de la release (NFO perso d'une team,
// avec la source…) remplace celui que fabrique V3X, et le MediaInfo mesuré y
// est ajouté s'il n'en contient pas déjà un.
function keepNfoOn() {
  try { return localStorage.getItem("d3x-keep-nfo") === "1"; } catch (e) { return false; }
}

// Les 128 caractères hauts de la page de code 437 (NFO à dessins ASCII).
const CP437 = "ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■ ";

function decodeNfo(bytes) {
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch (e) {
    let s = "";
    for (const b of bytes) s += b < 128 ? String.fromCharCode(b) : CP437[b - 128];
    return s;
  }
}

// Le .nfo de la release : celui de son dossier (pas ceux des épisodes), de
// préférence au nom de la release ; 256 Kio au plus. "" s'il n'y en a pas.
async function localNfoOf(j) {
  const nfos = (j.files || []).map((f, i) => ({ f, i })).filter((x) => /\.nfo$/i.test(x.f.rel) && x.f.size > 0 && x.f.size <= 262144);
  if (!nfos.length) return "";
  const depth = (x) => x.f.rel.split("/").length;
  const top = Math.min(...nfos.map(depth));
  const want = String(j.name).toLowerCase().replace(/\.[a-z0-9]{2,4}$/, "");
  const named = (x) => (x.f.rel.toLowerCase().includes(want) ? 1 : 0);
  const pick = nfos.filter((x) => depth(x) === top).sort((a, b) => named(b) - named(a))[0];
  try {
    const r = await fetch("/ui/pub/" + j.id + "/file/" + pick.i + "?offset=0&size=" + pick.f.size);
    if (!r.ok) return "";
    return decodeNfo(new Uint8Array(await r.arrayBuffer())).replace(/\r\n/g, "\n").replace(/\s+$/, "");
  } catch (e) {
    return "";
  }
}

// Un MediaInfo est-il déjà dans ce texte ? (même repère que le site)
function hasMediaInfo(t) {
  return /^(General|Général)\s*$/m.test(t) || /(Complete name|Nom complet)\s*:/.test(t);
}

// NFO à publier : le .nfo de la release, suivi du MediaInfo s'il n'en a pas.
function nfoWithMediaInfo(nfo, mi) {
  if (!nfo) return "";
  return hasMediaInfo(nfo) || !mi ? nfo : nfo + "\n\n" + mi;
}

// ---------- série choisie, reprise pour les autres saisons ----------

// Une série choisie à la main sur TMDB pour une saison sert aux autres saisons
// de la même série, dans Publier et dans le Lot (quebecman, 23/10/26 : choisir
// Power une fois, pas à chaque saison). Gardée par D3X (series.json) : le lot,
// qui tourne dans D3X, la voit aussi. La page y verse une fois ce qu'elle
// gardait dans le navigateur.
const SERIES_PICKS = "d3x-series-tmdb";
let seriesCache = {};

function seriesKeyOf(title) {
  return String(title || "").toLowerCase().normalize("NFD").replace(/[\u0300-\u036f]/g, "").replace(/[^a-z0-9]+/g, "");
}

async function loadSeries() {
  let old = null;
  try { old = JSON.parse(localStorage.getItem(SERIES_PICKS) || "null"); } catch (e) { old = null; }
  if (old && Object.keys(old).length) {
    try {
      await api("/ui/series", { entries: old });
      localStorage.removeItem(SERIES_PICKS);
    } catch (e) { /* on réessaiera au prochain chargement */ }
  }
  try { seriesCache = { ...((await api("/ui/series")) || {}), ...seriesCache }; } catch (e) { /* cache vide */ }
}

function rememberSeries(title, it) {
  const k = seriesKeyOf(title);
  if (!it || it.type !== "tv" || k.length < 2) return;
  const w = { type: "tv", id: it.id, title: it.title, year: it.year, posterUrl: it.posterUrl || null };
  seriesCache[k] = w;
  api("/ui/series", { entries: { [k]: w } }).catch(() => { /* rien */ });
}

function rememberedSeries(title) {
  const k = seriesKeyOf(title);
  return k.length >= 2 ? seriesCache[k] || null : null;
}

// Titre de série d'une fiche V3X (le titre lu dans le nom de la release).
function seriesTitleOf(f) {
  return (f && f.analysis && f.analysis.title) || (f && f.tmdbQuery) || "";
}

const ERRORS = {
  validation: "un champ de la fiche est refusé",
  rights_not_declared: "il faut déclarer avoir le droit de partager ce contenu",
  invalid_category: "choisis une sous-catégorie",
  invalid_torrent: "le .torrent est refusé par V3X",
  file_too_large: "le .torrent dépasse 32 Mio (trop de fichiers)",
  archive_only: "une release qui ne contient que des archives n'est pas acceptée",
  review_quota: "tu as déjà trop de releases en attente de validation : attends qu'elles soient revues",
  tmdb_required: "une fiche TMDB est obligatoire pour un film ou une série (ou coche « Pas de fiche TMDB » si elle n'existe vraiment pas)",
  tmdb_link_invalid: "le lien TMDB n'est pas valide",
  nfo_required: "le NFO est obligatoire pour un film ou une série",
  mediainfo_required: "le NFO doit contenir un vrai MediaInfo (pistes vidéo et audio mesurées)",
  resolution_too_low: "la résolution est sous le minimum de V3X",
  ddl_source: "la release porte la marque d'un site de téléchargement direct",
  free_software: "les logiciels libres et gratuits ne se publient pas sur V3X",
  audio_naming: "le nom d'une release musicale doit suivre le nommage du site",
  name_missing_disc: "le nom doit indiquer le disque d'origine",
  name_missing_gpu: "le nom doit indiquer l'encodeur matériel",
  exclusive_group_mismatch: "l'exclusivité est réservée aux releases de ta team",
  team_reupload_forbidden: "cette team refuse les réuploads de ses releases",
  banned_tag: "le groupe de cette release est refusé sur V3X",
  team_reupload_ask: "cette team demande son accord avant un réupload",
  duplicate: "cette release est déjà sur V3X (même .torrent)",
  duplicate_removed: "cette release a déjà été publiée puis retirée",
  duplicate_content: "le même contenu est déjà sur V3X",
  invalid_key_or_scope: "ta clé API n'a pas le droit « Upload » (Réglages ▸ Intégrations sur V3X)",
};

// Codes de contrôle renvoyés par V3X avec la fiche (les remarques sur le nom
// arrivent déjà en clair). BLOCKING : refusés à la publication, le lot n'y va pas.
const WARNINGS = {
  mediainfo_missing: "Pas de MediaInfo : il est obligatoire pour un film ou une série.",
  mediainfo_not_measured: "Le MediaInfo ne contient pas de pistes mesurées : V3X le refusera.",
  resolution_too_low: "Résolution sous le minimum de V3X : la publication sera refusée.",
  name_missing_gpu: "Encodage matériel détecté : le nom doit l'indiquer (NVENC, QSV…).",
  gpu_name_conflict: "Le nom et le MediaInfo ne disent pas le même encodeur.",
  tmdb_missing: "Pas encore de fiche TMDB choisie.",
  tmdb_not_found: "Cette fiche TMDB est introuvable.",
  mb_not_found: "Cette édition MusicBrainz est introuvable.",
  igdb_not_found: "Ce jeu est introuvable dans la base des jeux vidéo.",
  book_not_found: "Ce livre est introuvable : vérifie le lien ou l'ISBN, ou publie sans (choisis alors toi-même la sous-catégorie).",
  book_invalid_ref: "Ce n'est ni un lien Open Library (œuvre ou édition) ni un ISBN.",
  audio_naming: "Le nom d'une release musicale doit suivre le nommage du site.",
  category_unknown: "Catégorie inconnue sur V3X.",
};
const BLOCKING = new Set(["mediainfo_missing", "mediainfo_not_measured", "resolution_too_low", "name_missing_gpu", "gpu_name_conflict", "tmdb_not_found", "audio_naming", "category_unknown"]);

const TOKENS = { resolution: "Résolution", source: "Source", codec: "Codec", audio: "Audio", language: "Langue", hdr: "HDR", year: "Année", group: "Team" };

function pubErr(code, body) {
  if (code && code.startsWith("upscale")) return "les releases agrandies par IA ne sont pas acceptées telles quelles";
  let msg = ERRORS[code] || code;
  try {
    const b = JSON.parse(body || "{}");
    if (code === "validation" && b.field) msg += " : " + b.field + (b.detail ? " (" + b.detail + ")" : "");
    if (b.existing && b.existing.id) msg += ". Fiche existante : https://v3x.club/torrents/" + b.existing.id;
  } catch (e) { /* corps illisible */ }
  return msg;
}

// ---------- contexte ----------

async function loadCtx() {
  try {
    pub.ctx = await api("/ui/pub/context");
    pub.ctxError = "";
  } catch (e) {
    if (e.message === "session") return;
    pub.ctx = null;
    pub.ctxError = e.message;
  }
  const scope = $("#pub-scope");
  scope.textContent = pub.ctxError ? "Publication impossible pour l'instant : " + pub.ctxError + ". Ta clé API doit avoir les droits « Torznab » et « Upload » (Réglages ▸ Intégrations sur V3X)." : "";
  scope.classList.toggle("hidden", !pub.ctxError);
  if (!pub.ctx) return;
  // Feuille de style de l'aperçu (celle du site) : relue maintenant que V3X
  // répond ; chargée avec la page, elle peut être arrivée vide.
  $("#bbcode-css").href = "/ui/bbcode.css?" + Date.now();
  const root = $("#f-root");
  root.replaceChildren(new Option("Choisir…", ""));
  for (const c of pub.ctx.categories || []) root.append(new Option(c.name, c.slug));
  const lang = $("#f-lang");
  lang.replaceChildren();
  for (const l of pub.ctx.languages || []) lang.append(new Option(l || "Non précisée", l));
  const tpl = $("#f-tpl");
  tpl.replaceChildren(new Option("Présentation du site", ""));
  for (const t of pub.ctx.templates || []) tpl.append(new Option(t.name, t.id));
  const team = pub.ctx.user && pub.ctx.user.team;
  $("#team-opt").classList.toggle("hidden", !team);
  if (team) $("#team-label").textContent = "Release de ma team (" + team.name + ")";
  $("#f-anon").checked = !!(pub.ctx.user && pub.ctx.user.anonymousDefault);
  // Présentation en HTML : même règle que le site (uploadeur confirmé), le
  // choix ne s'affiche qu'à ceux qui y ont droit (demande de Luci, 21/10/26).
  $("#fmt").classList.toggle("hidden", !canHtml());
  setFormat(canHtml() ? savedFormat() : "standard");
}

function canHtml() {
  return !!(pub.ctx && pub.ctx.user && pub.ctx.user.canHtml);
}

function savedFormat() {
  try {
    return localStorage.getItem("d3x-pub-format") === "html" ? "html" : "standard";
  } catch {
    return "standard";
  }
}

// La barre de mise en forme et le raccourci Ctrl+B écrivent du BBCode : cachés
// en HTML. La présentation générée par le site est en BBCode : elle ne remplit
// pas l'éditeur en HTML, et « Régénérer » repasse en BBCode.
function setFormat(f) {
  pub.format = f === "html" && canHtml() ? "html" : "standard";
  const html = pub.format === "html";
  for (const [id, on] of [["#fmt-bb", !html], ["#fmt-html", html]]) {
    $(id).setAttribute("aria-checked", String(on));
    $(id).setAttribute("aria-selected", String(on));
  }
  $("#toolbar").classList.toggle("hidden", html);
  $("#f-desc").setAttribute("aria-label", html ? "Présentation (HTML)" : "Présentation (BBCode)");
  $("#f-desc").spellcheck = !html;
  previewSoon(0);
}

function chooseFormat(f) {
  setFormat(f);
  try {
    localStorage.setItem("d3x-pub-format", pub.format);
  } catch {
    // stockage indisponible : le choix vaut pour cette page
  }
}

function categoryBySlug(slug) {
  for (const c of (pub.ctx && pub.ctx.categories) || []) {
    if (c.slug === slug) return { root: c, leaf: null };
    for (const k of c.children || []) if (k.slug === slug) return { root: c, leaf: k };
  }
  return null;
}

function fillLeaves(rootSlug, leafSlug) {
  const leaf = $("#f-leaf");
  leaf.replaceChildren(new Option("Sous-catégorie…", ""));
  const found = categoryBySlug(rootSlug);
  if (found) for (const k of found.root.children || []) leaf.append(new Option(k.name, k.slug));
  if (leafSlug) leaf.value = leafSlug;
  const isSeries = rootSlug === "series";
  $("#season-field").classList.toggle("hidden", !isSeries);
}

function currentCategory() {
  const leafSlug = $("#f-leaf").value;
  const f = leafSlug ? categoryBySlug(leafSlug) : null;
  return f && f.leaf ? f.leaf : null;
}

function isFilmSeries() {
  const r = $("#f-root").value;
  return r === "films" || r === "series";
}

// Jeux vidéo ou Émulation : la recherche de jeux remplace celle de TMDB, comme
// sur la page d'upload du site.
function isGame() {
  const r = $("#f-root").value;
  return (r === "jeux-video" || r === "emulation") && !isAlbum();
}

// Rapport VirusTotal exigé par V3X (même règle que le site, lib/virustotal.ts) :
// applications Windows, macOS, smartphone et « autre », et tous les jeux ; un
// jeu console seulement s'il contient un exécutable PC (retour de Luci).
function needsVirusTotal() {
  const slug = $("#f-leaf").value;
  if (!/^applications-(windows|macos|smartphone|autre)$/.test(slug) && !/^jeux-video-/.test(slug)) return false;
  if (!/^jeux-video-(nintendo|sony|microsoft)$/.test(slug)) return true;
  const files = (pub.job && pub.job.files) || [];
  return files.some((f) => /\.(exe|dll|msi|bat|cmd|ps1|scr|com|jar|vbs|msix|appx)$/i.test(f.rel || ""));
}

// ---------- sélecteur de dossiers ----------

async function browse(path) {
  try {
    const r = await api("/ui/fs?source=" + pub.source + "&path=" + encodeURIComponent(path || ""));
    pub.dir = r.path;
    $("#fs-path").value = r.path;
    $("#fs-up").disabled = r.parent === null;
    $("#fs-up").dataset.parent = r.parent || "";
    const ul = $("#fs-list");
    ul.replaceChildren();
    if (!r.entries.length) ul.append(el("li", "empty", "Dossier vide."));
    for (const e of r.entries) {
      const li = el("li", e.skip ? "skip" : "");
      const icon = el("span", "fs-ico " + (e.dir ? "dir" : "file"));
      icon.setAttribute("aria-hidden", "true");
      const name = el("button", "fs-name", e.name);
      name.type = "button";
      if (e.dir) name.addEventListener("click", () => browse(e.path));
      else name.addEventListener("click", () => choose(e.path));
      li.append(icon, name, el("span", "fs-size", e.dir ? "" : fmtSize(e.size)));
      if (r.path !== "" && !e.skip) {
        const pick = el("button", "btn small", e.dir ? "Publier ce dossier" : "Publier ce fichier");
        pick.type = "button";
        pick.addEventListener("click", () => choose(e.path));
        li.append(pick);
      } else if (e.skip) {
        li.append(el("span", "muted small", "écarté"));
      }
      ul.append(li);
    }
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

// ---------- tâche ----------

// opts.fromLot : ouverte par « Revoir » depuis le Lot, la fiche peut y être
// gardée pour que le lot la publie telle quelle.
async function choose(path, source, opts) {
  if (source && source !== pub.source) {
    pub.source = source;
    pub.dir = "";
    window.pubSources();
  }
  try {
    const j = await api("/ui/pub/start", { path, source: pub.source });
    resetForm();
    pub.fromLot = opts && opts.fromLot ? path : "";
    pub.job = j;
    pub.published = false;
    $("#pub-pick").classList.add("hidden");
    $("#pub-src").classList.add("hidden");
    $("#pub-job").classList.remove("hidden");
    $("#pub-form").classList.remove("hidden");
    $("#pub-done").classList.add("hidden");
    $("#f-name").value = stripExt(j.name, j.single);
    renderJob();
    runMediaInfo();
    window.musicSetup();
    pub.localNfo = await localNfoOf(j);
    // Release déjà gardée pour le lot : sa fiche revient telle qu'elle a été
    // gardée, au lieu d'une fiche neuve (AntoineAngel, 22/10/26 : « Revoir »
    // donnait l'impression que les fiches gardées étaient perdues).
    if (opts && opts.fromLot) await restoreKept(path);
    loadFiche();
    pollJob();
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

// Remet dans le formulaire la fiche gardée pour le lot, si elle existe. Les
// champs repris comptent comme retouchés : la fiche de V3X ne les écrase pas.
async function restoreKept(path) {
  let p;
  try {
    p = await api("/ui/batch/prepared?source=" + pub.source + "&path=" + encodeURIComponent(path));
  } catch (e) {
    return; // pas de fiche gardée : fiche neuve, comme avant
  }
  const f = (p && p.fields) || {};
  const put = (id, key, v) => {
    if (v == null || v === "") return;
    $(id).value = v;
    pub.dirty[key] = true;
  };
  put("#f-name", "name", f.releaseName);
  put("#f-title", "title", f.title);
  put("#f-nfo", "nfo", f.nfo);
  put("#f-season", "season", f.season);
  if (f.language != null) {
    $("#f-lang").value = f.language;
    pub.dirty.language = true;
  }
  for (const c of (pub.ctx && pub.ctx.categories) || []) {
    const leaf = (c.children || []).find((k) => String(k.id) === f.categoryId);
    if (!leaf) continue;
    $("#f-root").value = c.slug;
    fillLeaves(c.slug, leaf.slug);
    pub.dirty.category = true;
    break;
  }
  $("#f-anon").checked = f.anonymous === "true";
  $("#f-excl").checked = f.exclusive === "true";
  if (f.teamRelease != null) {
    $("#f-team").checked = f.teamRelease === "true";
    pub.dirty.team = true;
  }
  $("#f-notmdb").checked = f.noTmdb === "true";
  put("#f-vt", "vt", f.virusTotalUrl);
  const m = /themoviedb\.org\/(movie|tv)\/(\d+)/.exec(f.tmdbUrl || "");
  if (m) pub.tmdb = { type: m[1], id: Number(m[2]), title: f.title || "", year: null, posterUrl: null };
  if (f.posterUrl) pub.keptPoster = f.posterUrl;
  if (f.description != null) {
    $("#f-desc").value = f.description;
    pub.dirty.desc = true;
    setFormat(f.descriptionFormat === "html" ? "html" : "standard");
  }
  gameSetup();
  renderTmdbPicked();
  toast("Fiche gardée rechargée : tes retouches sont là.", "ok");
}

// Même phrase que D3X côté serveur (v3x.Gone.Text).
function goneText(g) {
  const verb = { trumped: "remplacée par une meilleure version", archived: "archivée" }[g.status] || "retirée";
  return "une fiche V3X de ce contenu a été " + verb + (g.own && g.reason ? " (ta fiche, motif : " + g.reason + ")" : "");
}

function stripExt(name, single) {
  return single ? name.replace(/\.(mkv|mp4|avi|m2ts|ts|mov|wmv|webm|mpe?g|m4v|iso|flac|mp3|m4a|ogg|opus|wav|dsf)$/i, "") : name;
}

function resetForm() {
  pub.fiche = null;
  pub.mediainfo = "";
  pub.miState = "idle";
  pub.tmdb = null;
  pub.game = null;
  pub.book = null;
  pub.versionsKey = "";
  pub.keptPoster = "";
  pub.localNfo = "";
  pub.dirty = {};
  for (const id of ["#f-name", "#f-title", "#f-season", "#f-desc", "#f-nfo", "#f-vt", "#tmdb-q", "#game-q", "#book-q"]) $(id).value = "";
  $("#game-results").replaceChildren();
  $("#game-picked").classList.add("hidden");
  $("#game-state").textContent = "";
  $("#game-field").classList.add("hidden");
  $("#book-picked").classList.add("hidden");
  $("#book-state").textContent = BOOK_HINT;
  $("#book-field").classList.add("hidden");
  $("#versions").classList.add("hidden");
  $("#f-notmdb").checked = false;
  $("#f-excl").checked = false;
  $("#f-team").checked = false;
  $("#f-root").value = "";
  fillLeaves("", "");
  $("#tmdb-results").replaceChildren();
  $("#tmdb-picked").classList.add("hidden");
  $("#preview").replaceChildren();
  $("#preview").classList.remove("html-prev");
  $("#pub-error").classList.add("hidden");
  $("#name-sugg").classList.add("hidden");
  $("#name-tokens").replaceChildren();
}

async function cancelJob() {
  if (pub.job && !pub.job.published) {
    try { await api("/ui/pub/" + pub.job.id + "/cancel", {}); } catch (e) { /* rien */ }
  }
  pub.job = null;
  clearTimeout(pub.timer);
  $("#pub-pick").classList.remove("hidden");
  $("#pub-job").classList.add("hidden");
  $("#pub-form").classList.add("hidden");
  $("#pub-done").classList.add("hidden");
  $("#pub-bar").classList.add("hidden");
  window.pubSources();
  browse(pub.dir);
}

async function pollJob() {
  clearTimeout(pub.timer);
  if (!pub.job) return;
  try {
    pub.job = await api("/ui/pub/" + pub.job.id);
  } catch (e) {
    if (e.message === "session") return;
  }
  renderJob();
  const j = pub.job;
  const seedBusy = (j.published || (j.seedOnly && j.state === "done" && !j.seedNote)) && (!j.seed || j.seed.status === "checking") && !j.seedError;
  if (j.state === "hashing" || j.state === "publishing" || j.state === "seeding" || seedBusy) {
    pub.timer = setTimeout(pollJob, 700);
  }
}

function setStep(id, state, text) {
  const s = $(id);
  s.className = "step " + state;
  s.querySelector(".sub").textContent = text;
}

function renderJob() {
  const j = pub.job;
  if (!j) return;
  $("#job-name").textContent = j.name;
  const facts = $("#job-facts");
  facts.replaceChildren();
  const add = (label, value) => {
    const d = el("div", "fact");
    d.append(el("span", "muted", label), el("b", null, value));
    facts.append(d);
  };
  add("Fichiers", String(j.files.length));
  add("Taille", fmtSize(j.total));
  add("Pièces", (j.pieceLength >= 1048576 ? j.pieceLength / 1048576 + " Mio" : j.pieceLength / 1024 + " Kio"));
  if (j.source && j.source !== "Ce PC") add("Sur", j.source);
  if (j.skipped && j.skipped.length) add("Écartés", String(j.skipped.length));

  const dup = $("#job-dup");
  if (j.duplicate) {
    const m = j.duplicate;
    dup.replaceChildren(document.createTextNode(m.kind === "exact" || m.kind === "self" ? "Cette release est déjà sur V3X, à l'identique : " : "Les mêmes fichiers sont déjà sur V3X, sous un autre nom : "));
    const a = el("a", null, m.name);
    a.href = "https://v3x.club/torrents/" + m.id;
    a.target = "_blank";
    a.rel = "noopener";
    // Identique : D3X la met en seed avec ces fichiers (Turias, 22/10/26 :
    // Cross-seed ne voit que ce qui est déjà dans le client). Mêmes fichiers
    // sous d'autres noms : la Médiathèque sait les rapprocher.
    const same = m.kind === "exact" || m.kind === "self";
    if (j.seedOnly) {
      dup.append(a, document.createTextNode(". " + seedText(j, "la release V3X")));
    } else if (same && j.client) {
      dup.append(a, document.createTextNode(". Pas besoin de la publier : seede-la avec ces fichiers. "));
      const go = el("button", "btn small", "Seeder la release V3X avec ces fichiers");
      go.type = "button";
      go.addEventListener("click", async () => {
        go.disabled = true;
        try {
          pub.job = await api("/ui/pub/" + j.id + "/seed-existing", {});
          renderJob();
          pollJob();
        } catch (e) {
          go.disabled = false;
          if (e.message !== "session") toast(e.message, "bad");
        }
      });
      dup.append(go);
    } else {
      dup.append(a, document.createTextNode(same ? ". Pas besoin de la publier : ajoute un client torrent dans Réglages pour la seeder avec ces fichiers." : ". Pas besoin de la publier : la Médiathèque retrouve ces fichiers et les met en seed sous le nom de la fiche V3X."));
    }
    dup.classList.toggle("bad", !j.seedOnly);
    dup.classList.remove("hidden");
  } else if (j.gone) {
    // Fiche retirée du même contenu (issue GitHub #1) : republier reste
    // possible, mais en connaissance de cause.
    dup.textContent = "Attention : " + goneText(j.gone) + ". Republie-la seulement si la raison ne s'applique plus.";
    dup.classList.remove("bad");
    dup.classList.remove("hidden");
  } else {
    dup.classList.add("hidden");
  }

  if (j.seedOnly) setStep("#st-hash", "ok", "Rien à fabriquer : c'est le .torrent de la fiche V3X qui part dans ton client.");
  else if (j.state === "error") setStep("#st-hash", "bad", j.error);
  else if (j.state === "hashing") {
    setStep("#st-hash", "run", "Hachage : " + fmtSize(j.hashed) + " sur " + fmtSize(j.total));
  } else setStep("#st-hash", "ok", j.fromCache ? "Repris du cache : ces fichiers ont déjà été hachés." : "Prêt.");
  $("#st-hash-bar").style.width = (j.total ? Math.round((j.hashed / j.total) * 100) : 0) + "%";
  $("#st-hash .mini").classList.toggle("hidden", j.state !== "hashing");

  const miText = { idle: "En attente.", running: "Lecture du fichier vidéo…", done: "Lu sur le plus gros fichier vidéo.", none: "Aucun fichier vidéo : pas de MediaInfo.", error: "Lecture impossible : colle-le à la main dans le NFO." }[pub.miState];
  setStep("#st-mi", { running: "run", done: "ok", error: "bad", none: "", idle: "" }[pub.miState], miText);

  pubClients(j);
  let mapText;
  const multi = multiClients();
  const who = multi ? "« " + j.clientName + " »" : "Ton client";
  if (j.mapGuess) mapText = who + " verra ce dossier comme « " + j.clientDir + " » (déduit de " + j.mapVotes + " torrent" + (j.mapVotes > 1 ? "s" : "") + " de " + (multi ? "ce client" : "ton client") + "). ";
  else mapText = who + " cherchera les fichiers dans « " + j.clientDir + " ». ";
  setStep("#st-map", j.mapGuess || j.mapVotes ? "ok" : "", mapText);
  const fix = el("button", "link", "Corriger");
  fix.type = "button";
  fix.addEventListener("click", () => {
    $("#map-edit").classList.toggle("hidden");
    $("#map-dir").value = j.clientDir;
  });
  $("#st-map-txt").append(fix);

  // Publication terminée.
  if (j.published) renderDone();
  renderBar();
}

// Plusieurs clients : celui qui seedera la release, au choix jusqu'à l'envoi.
function pubClients(j) {
  const list = (ui.state && ui.state.clients) || [];
  const row = $("#pub-client-row");
  const show = list.length > 1 && !j.published && j.state !== "publishing";
  row.classList.toggle("hidden", !show);
  if (!show) return;
  const sel = $("#pub-client");
  if (document.activeElement === sel) return;
  const key = list.map((c) => c.id + (c.ok ? "+" : "-")).join(",");
  if (sel.dataset.key !== key) {
    sel.dataset.key = key;
    sel.replaceChildren(...list.map((c) => {
      const o = el("option", null, c.label + (c.ok ? "" : " (injoignable)"));
      o.value = c.id;
      o.disabled = !c.ok;
      return o;
    }));
  }
  sel.value = j.client;
}

async function pickClient(ev) {
  const sel = ev.target;
  sel.disabled = true;
  try {
    pub.job = await api("/ui/pub/" + pub.job.id + "/client", { client: sel.value });
    $("#map-edit").classList.add("hidden");
    renderJob();
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
    if (pub.job) sel.value = pub.job.client;
  } finally {
    sel.disabled = false;
  }
}

async function saveMap() {
  try {
    pub.job = await api("/ui/pub/" + pub.job.id + "/clientdir", { dir: $("#map-dir").value, remember: $("#map-remember").checked });
    $("#map-edit").classList.add("hidden");
    renderJob();
    toast("Dossier du client enregistré.", "ok");
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

// ---------- MediaInfo (dans la page, même bibliothèque que le site) ----------

let miFactory = null;

// MediaInfo du plus gros fichier vidéo d'une tâche, lu par morceaux via D3X.
// Renvoie "" s'il n'y a pas de vidéo.
async function mediaInfoOf(j) {
  if (j.mainFile < 0) return "";
  return window.mediaInfoRead(j.id, j.mainFile, j.files[j.mainFile].size);
}

// MediaInfo d'un fichier d'une tâche, lu par morceaux via D3X. Sert à Publier
// et au lot (qui tourne dans D3X) : sans binaire mediainfo côté D3X, c'est la
// page ouverte qui le calcule et le lui rend.
window.mediaInfoRead = async (jobId, index, size) => {
  if (!miFactory) {
    miFactory = await window.MediaInfo.mediaInfoFactory({ format: "text", locateFile: () => "/vendor/mediainfo/MediaInfoModule.wasm" });
  }
  const read = async (n, offset) => {
    const r = await fetch("/ui/pub/" + jobId + "/file/" + index + "?offset=" + offset + "&size=" + n);
    if (!r.ok) throw new Error("lecture " + r.status);
    return new Uint8Array(await r.arrayBuffer());
  };
  const text = await miFactory.analyzeData(() => size, read);
  return String(text || "").trim();
};

async function runMediaInfo() {
  const j = pub.job;
  if (!j) return;
  if (j.mainFile < 0) {
    pub.miState = "none";
    renderJob();
    return;
  }
  pub.miState = "running";
  renderJob();
  try {
    const text = await mediaInfoOf(j);
    if (!pub.job || pub.job.id !== j.id) return;
    pub.mediainfo = text;
    pub.miState = text ? "done" : "error";
  } catch (e) {
    if (!pub.job || pub.job.id !== j.id) return;
    pub.miState = "error";
  }
  renderJob();
  loadFiche();
}

// ---------- fiche proposée par V3X ----------

let ficheTimer = null;
function loadFicheSoon() {
  clearTimeout(ficheTimer);
  ficheTimer = setTimeout(loadFiche, 400);
}

async function loadFiche(force) {
  const j = pub.job;
  if (!j || !pub.ctx) return;
  const seq = ++pub.ficheSeq;
  $("#fiche-state").textContent = "Préparation de la fiche par V3X…";
  const leaf = currentCategory();
  try {
    const f = await api("/ui/pub/" + j.id + "/fiche", {
      releaseName: $("#f-name").value.trim() || stripExt(j.name, j.single),
      mediainfo: pub.mediainfo,
      tmdbRef: pub.tmdb && !isGame() ? pub.tmdb.type + "/" + pub.tmdb.id : null,
      mbRef: pub.mb ? mbURL(pub.mb) : null,
      igdbId: isGame() && pub.game ? pub.game.id : null,
      bookRef: isBook() && pub.book ? pub.book.ref : null,
      categorySlug: leaf ? leaf.slug : null,
      templateId: $("#f-tpl").value || null,
    });
    if (seq !== pub.ficheSeq) return;
    pub.fiche = f;
    applyFiche(f, force);
    renderBook(f);
    versionsSoon();
    $("#fiche-state").textContent = "";
  } catch (e) {
    if (seq !== pub.ficheSeq || e.message === "session") return;
    $("#fiche-state").textContent = "Fiche indisponible : " + e.message;
  }
}

function setField(id, key, value, force) {
  if (value == null) return;
  if (pub.dirty[key] && !force) return;
  $(id).value = value;
}

function applyFiche(f, force) {
  if (f.category && (!pub.dirty.category || force)) {
    const found = categoryBySlug(f.category.slug);
    if (found) {
      $("#f-root").value = found.root.slug;
      fillLeaves(found.root.slug, found.leaf ? found.leaf.slug : "");
    }
  }
  gameSetup();
  setField("#f-lang", "language", f.language, force);
  setField("#f-season", "season", f.season, force);
  setField("#f-title", "title", f.title, force);
  if (!pub.dirty.team || force) $("#f-team").checked = !!f.teamRelease;
  setField("#f-nfo", "nfo", keepNfoOn() && pub.localNfo ? nfoWithMediaInfo(pub.localNfo, pub.mediainfo) : f.nfo, force);
  if ((!pub.dirty.desc || force) && pub.format !== "html") {
    $("#f-desc").value = f.presentation || "";
    previewSoon(0);
  }
  // Série déjà choisie pour une autre saison : reprise d'office.
  const again = !pub.tmdb && !isAlbum() && !isGame() && (f.season || /\bS\d{1,2}(E\d{1,3})?\b/i.test($("#f-name").value)) ? rememberedSeries(seriesTitleOf(f)) : null;
  if (again) {
    pub.tmdb = { ...again, remembered: true };
    renderTmdbPicked();
    loadFicheSoon();
  }
  if (!$("#tmdb-q").value && f.tmdbQuery && !pub.tmdb && !isAlbum() && !isGame()) {
    $("#tmdb-q").value = f.tmdbQuery;
    searchTmdb();
  }
  // Nom suggéré par le site (année, piste audio…), adopté d'un clic.
  const sugg = $("#name-sugg");
  if (f.suggestedName && f.suggestedName !== $("#f-name").value) {
    sugg.replaceChildren(document.createTextNode("Suggestion de V3X : "));
    sugg.append(el("code", null, f.suggestedName), document.createTextNode(" "));
    const b = el("button", "link", "Adopter");
    b.type = "button";
    b.addEventListener("click", () => {
      $("#f-name").value = f.suggestedName;
      pub.dirty.name = true;
      sugg.classList.add("hidden");
      loadFicheSoon();
    });
    sugg.append(b);
    sugg.classList.remove("hidden");
  } else {
    sugg.classList.add("hidden");
  }
  const tok = $("#name-tokens");
  tok.replaceChildren();
  for (const t of (f.analysis && f.analysis.tokens) || []) {
    const c = el("span", "tok");
    c.append(el("span", "muted", (TOKENS[t.kind] || t.kind) + " "), el("b", null, t.value));
    tok.append(c);
  }
  // La liste warnings reprend les remarques sur le nom (en clair) et les codes.
  for (const w of f.warnings || []) {
    if (w === "tmdb_missing" && pub.tmdb) continue;
    tok.append(el("span", "tok warn" + (BLOCKING.has(w) ? " block" : ""), WARNINGS[w] || w));
    if (w === "audio_naming") window.musicNameFix(tok);
  }
  renderBar();
}

// ---------- TMDB ----------

async function searchTmdb() {
  const q = $("#tmdb-q").value.trim();
  if (q.length < 2) return;
  const ul = $("#tmdb-results");
  ul.replaceChildren(el("li", "muted", "Recherche…"));
  try {
    const isRef = /themoviedb\.org\/(movie|tv)\/\d+|^(movie|tv)\/\d+$/i.test(q);
    const r = isRef ? await api("/ui/pub/tmdb/lookup?ref=" + encodeURIComponent(q)) : await api("/ui/pub/tmdb/search?q=" + encodeURIComponent(q));
    ul.replaceChildren();
    const list = (r.results || []).filter((x) => x.type === "movie" || x.type === "tv").slice(0, 12);
    if (!list.length) ul.append(el("li", "muted", "Aucun résultat. Essaie le titre original, ou colle le lien de la fiche TMDB."));
    for (const it of list) {
      const li = el("li");
      const b = el("button", "tmdb-item");
      b.type = "button";
      if (it.posterUrl && /^https:\/\//.test(it.posterUrl)) {
        const img = el("img");
        img.src = it.posterUrl;
        img.alt = "";
        img.loading = "lazy";
        b.append(img);
      } else {
        b.append(el("span", "noposter", "?"));
      }
      const meta = el("span", "tmdb-meta");
      meta.append(el("b", null, it.title), el("span", "muted", (it.type === "tv" ? "Série" : "Film") + (it.year ? " · " + it.year : "")));
      if (it.originalTitle && it.originalTitle !== it.title) meta.append(el("span", "muted small", it.originalTitle));
      b.append(meta);
      b.addEventListener("click", () => pickTmdb(it));
      li.append(b);
      ul.append(li);
    }
  } catch (e) {
    if (e.message !== "session") ul.replaceChildren(el("li", "bad", e.message));
  }
}

function pickTmdb(it) {
  pub.tmdb = { type: it.type, id: it.id, title: it.title, year: it.year, posterUrl: it.posterUrl };
  if (it.type === "tv") rememberSeries(seriesTitleOf(pub.fiche), it);
  $("#f-notmdb").checked = false;
  $("#tmdb-results").replaceChildren();
  renderTmdbPicked();
  loadFiche();
}

// Encart de l'œuvre TMDB choisie (pub.tmdb), aussi pour une fiche gardée
// rechargée.
function renderTmdbPicked() {
  const it = pub.tmdb;
  const p = $("#tmdb-picked");
  p.replaceChildren();
  if (!it) {
    p.classList.add("hidden");
    return;
  }
  if (it.posterUrl && /^https:\/\//.test(it.posterUrl)) {
    const img = el("img");
    img.src = it.posterUrl;
    img.alt = "";
    p.append(img);
  }
  const t = el("div");
  const a = el("a", null, it.title + (it.year ? " (" + it.year + ")" : ""));
  a.href = "https://www.themoviedb.org/" + it.type + "/" + it.id;
  a.target = "_blank";
  a.rel = "noopener";
  t.append(a, el("div", "muted small", it.type === "tv" ? "Série" + (it.remembered ? " · reprise de ton choix pour une autre saison" : "") : "Film"));
  const ch = el("button", "link", "Changer");
  ch.type = "button";
  ch.addEventListener("click", () => {
    pub.tmdb = null;
    p.classList.add("hidden");
    searchTmdb();
    loadFicheSoon();
  });
  t.append(ch);
  p.append(t);
  p.classList.remove("hidden");
}

// ---------- jeux vidéo (Nayr, 22/10/26) ----------

// La recherche de jeux remplace celle de TMDB dans Jeux vidéo et Émulation. À
// l'entrée dans ces catégories, elle part du nom de la release.
function gameSetup() {
  const on = isGame();
  $("#tmdb-field").classList.toggle("hidden", isAlbum() || on || isBook());
  $("#game-field").classList.toggle("hidden", !on);
  $("#book-field").classList.toggle("hidden", !isBook());
  if (!on || pub.game || $("#game-q").value.trim()) return;
  const q = (pub.fiche && pub.fiche.tmdbQuery) || (pub.job ? stripExt(pub.job.name, pub.job.single) : "");
  if (!q) return;
  $("#game-q").value = q;
  searchGame();
}

function gameMeta(it) {
  return [it.year, (it.platforms || []).join(", ")].filter(Boolean).join(" · ");
}

function gameCover(url) {
  if (!url || !/^https:\/\//.test(url)) return el("span", "noposter", "?");
  const img = el("img");
  img.src = url;
  img.alt = "";
  img.loading = "lazy";
  return img;
}

async function searchGame() {
  const q = $("#game-q").value.trim();
  if (q.length < 2) return;
  const ul = $("#game-results");
  $("#game-state").textContent = "Recherche…";
  ul.replaceChildren();
  try {
    const r = await api("/ui/pub/igdb/search?q=" + encodeURIComponent(q));
    if (r.configured === false) {
      $("#game-state").textContent = "La recherche de jeux n'est pas disponible sur V3X pour l'instant : remplis le titre à la main.";
      return;
    }
    const list = (r.results || []).slice(0, 10);
    $("#game-state").textContent = list.length ? "" : "Aucun jeu trouvé. Essaie le titre seul, sans version ni plateforme.";
    for (const it of list) {
      const li = el("li");
      const b = el("button", "tmdb-item");
      b.type = "button";
      const meta = el("span", "tmdb-meta");
      meta.append(el("b", null, it.name), el("span", "muted", gameMeta(it)));
      b.append(gameCover(it.coverUrl), meta);
      b.addEventListener("click", () => pickGame(it));
      li.append(b);
      ul.append(li);
    }
  } catch (e) {
    if (e.message !== "session") $("#game-state").textContent = "Recherche de jeux : " + e.message;
  }
}

// Le jeu choisi donne le titre, la jaquette et la fiche jeu du site.
function pickGame(it) {
  pub.game = { id: it.id, name: it.name, year: it.year, coverUrl: it.coverUrl };
  $("#game-results").replaceChildren();
  $("#game-state").textContent = "";
  const p = $("#game-picked");
  p.replaceChildren(gameCover(it.coverUrl));
  const t = el("div");
  t.append(el("b", null, it.name + (it.year ? " (" + it.year + ")" : "")), el("div", "muted small", gameMeta(it)));
  const ch = el("button", "link", "Changer");
  ch.type = "button";
  ch.addEventListener("click", () => {
    pub.game = null;
    p.classList.add("hidden");
    searchGame();
    loadFicheSoon();
  });
  t.append(ch);
  p.append(t);
  p.classList.remove("hidden");
  loadFiche();
}

// Jaquette grand format du jeu, quand la fiche reçue est bien celle du jeu choisi.
function gamePoster() {
  const g = pub.fiche && pub.fiche.game;
  return isGame() && pub.game && g && g.id === pub.game.id && g.posterUrl ? g.posterUrl : "";
}

// ---------- livres (calcifer, 23/10/26) ----------

// Dans Ebook : un lien Open Library (œuvre, édition) ou un ISBN donne titre,
// auteur, couverture et fiche livre, comme le même lien collé sur le site.
// Sans catégorie choisie, une release faite de livres (.epub, .pdf…) aussi :
// le livre trouvé range la fiche dans Ebook ▸ Livres.
const BOOK_HINT = "Le lien de l'œuvre ou de l'édition, ou l'ISBN : titre, auteur, couverture et fiche du livre, comme sur le site. Facultatif.";

function isBook() {
  const r = $("#f-root").value;
  if (r) return r === "ebook";
  const files = (pub.job && pub.job.files) || [];
  return files.length > 0 && files.every((f) => /\.(epub|pdf|mobi|azw3?|fb2|djvu|cbz|cbr|nfo|txt|jpe?g|png)$/i.test(f.rel || "")) && files.some((f) => /\.(epub|pdf|mobi|azw3?|fb2|djvu)$/i.test(f.rel || ""));
}

function useBook() {
  const ref = $("#book-q").value.trim();
  if (!ref) return;
  pub.book = { ref };
  $("#book-state").textContent = "Lecture du livre…";
  loadFiche();
}

// Le livre lu par V3X (fiche reçue), ou le refus du lien.
function renderBook(f) {
  const p = $("#book-picked");
  if (!pub.book) {
    p.classList.add("hidden");
    return;
  }
  const w = (f.warnings || []).find((x) => x === "book_not_found" || x === "book_invalid_ref");
  if (w || !f.book) {
    p.classList.add("hidden");
    // Introuvable (sortie récente, petite édition) : rien n'oblige à un lien,
    // mais sans livre trouvé D3X ne devine pas la sous-catégorie (MiTor, 25/10/26).
    $("#book-state").textContent = w === "book_invalid_ref"
      ? WARNINGS[w]
      : "Ce livre est introuvable : vérifie le lien ou l'ISBN. Tu peux aussi publier sans : vide ce champ, choisis toi-même la sous-catégorie (Ebook ▸ Livres, BDs…), mets le titre et la couverture en première image de la présentation (bouton « Image du PC »).";
    return;
  }
  const b = f.book;
  p.replaceChildren(gameCover(b.posterUrl));
  const t = el("div");
  t.append(el("b", null, b.title), el("div", "muted small", [b.author, b.year].filter(Boolean).join(" · ")));
  const ch = el("button", "link", "Changer");
  ch.type = "button";
  ch.addEventListener("click", () => {
    pub.book = null;
    p.classList.add("hidden");
    $("#book-q").focus();
    loadFicheSoon();
  });
  t.append(ch);
  p.append(t);
  p.classList.remove("hidden");
  $("#book-state").textContent = "";
}

// ---------- versions déjà sur V3X (Nico31, Turias, quebecman, 23/10/26) ----------

// Les versions du même film, du même épisode ou du même livre, avec leur
// taille, et le REPACK de cette release s'il est déjà là. Rien n'est bloqué :
// c'est l'uploadeur qui décide.
let versionsTimer = null;
function versionsSoon() {
  clearTimeout(versionsTimer);
  versionsTimer = setTimeout(loadVersions, 600);
}

async function loadVersions() {
  const j = pub.job;
  const box = $("#versions");
  if (!j || j.seedOnly) {
    box.classList.add("hidden");
    return;
  }
  const leaf = currentCategory();
  const q = new URLSearchParams({ name: $("#f-name").value.trim() || stripExt(j.name, j.single), size: String(j.total || 0) });
  if (pub.tmdb && !isGame() && !isBook() && !isAlbum()) q.set("tmdb", pub.tmdb.type + "/" + pub.tmdb.id);
  if (leaf) q.set("category", leaf.slug);
  else if ($("#f-root").value) q.set("category", $("#f-root").value);
  const key = q.toString();
  if (key === pub.versionsKey) return;
  pub.versionsKey = key;
  let r;
  try {
    r = await api("/ui/pub/versions?" + key);
  } catch (e) {
    if (pub.versionsKey === key) pub.versionsKey = "";
    return;
  }
  if (pub.versionsKey !== key || pub.job !== j) return;
  renderVersions(r, j);
}

function renderVersions(r, j) {
  const box = $("#versions");
  const list = (r.versions || []).filter((v) => !(j.duplicate && v.id === j.duplicate.id));
  const redo = (r.redo || []).filter((v) => !(j.duplicate && v.id === j.duplicate.id));
  box.classList.toggle("hidden", list.length === 0 && redo.length === 0);
  const warn = $("#versions-redo");
  warn.classList.toggle("hidden", redo.length === 0);
  if (redo.length) {
    const top = redo[0];
    warn.replaceChildren(document.createTextNode("Un REPACK de ta release est déjà sur V3X : "));
    const a = el("a", null, top.name);
    a.href = "https://v3x.club/torrents/" + top.id;
    a.target = "_blank";
    a.rel = "noopener";
    warn.append(a, document.createTextNode(" (" + fmtSize(top.size) + ", " + top.seeders + " " + plural(top.seeders, "source", "sources") + "). La version d'origine n'apporte rien de plus."));
  }
  const ul = $("#versions-list");
  ul.replaceChildren();
  for (const v of list.slice(0, 8)) {
    const li = el("li", v.redo ? "redo" : null);
    const a = el("a", null, v.name);
    a.href = "https://v3x.club/torrents/" + v.id;
    a.target = "_blank";
    a.rel = "noopener";
    li.append(a);
    if (v.redo) li.append(el("span", "tag", "REPACK de ta release"));
    else if (v.close) li.append(el("span", "tag", "presque la même taille"));
    li.append(el("span", "num", fmtSize(v.size) + " · " + v.seeders + " " + plural(v.seeders, "source", "sources")));
    ul.append(li);
  }
  const close = list.filter((v) => v.close && !v.redo).length;
  $("#versions-hint").textContent =
    (list.length > 8 ? "Et " + (list.length - 8) + " autre(s). " : "") +
    (close ? "Une version de presque la même taille que la tienne (" + fmtSize(j.total) + ") est déjà là : la publier n'apporte peut-être pas grand-chose. " : "") +
    "Deux fichiers différents ont le droit d'exister : c'est toi qui décides.";
}

// ---------- éditeur de présentation ----------

const TOOLS = [
  ["B", "Gras", "[b]", "[/b]"],
  ["I", "Italique", "[i]", "[/i]"],
  ["U", "Souligné", "[u]", "[/u]"],
  ["S", "Barré", "[s]", "[/s]"],
  ["Centrer", "Centrer", "[center]", "[/center]", true],
  ["Justifier", "Justifier", "[justify]", "[/justify]", true],
  ["Citation", "Citation", "[quote]", "[/quote]", true],
  ["Code", "Code / NFO (garde l'alignement)", "[code]", "[/code]", true],
  ["Spoiler", "Spoiler", "[spoiler=Spoiler]", "[/spoiler]", true],
  ["Avis", "Avis perso (bloc mis en valeur)", "[avis]", "[/avis]", true],
  ["Lien", "Lien", "[url=https://]", "[/url]"],
  ["Image", "Image (lien direct)", "[img]", "[/img]"],
  ["Vidéo", "Vidéo YouTube (colle le lien entre les balises)", "[youtube]", "[/youtube]"],
  ["Liste", "Liste", "[list]\n[*] ", "\n[/list]", true],
  ["Tableau", "Tableau", "[table]\n[tr][th]Colonne 1[/th][th]Colonne 2[/th][/tr]\n[tr][td]", "[/td][td]…[/td][/tr]\n[/table]", true],
];

function wrap(open, close, block) {
  const ta = $("#f-desc");
  const { selectionStart: a, selectionEnd: b, value } = ta;
  const sel = value.slice(a, b);
  const pre = block && a > 0 && value[a - 1] !== "\n" ? "\n" : "";
  ta.value = value.slice(0, a) + pre + open + sel + close + value.slice(b);
  const pos = a + pre.length + open.length;
  ta.focus();
  ta.setSelectionRange(pos, pos + sel.length);
  pub.dirty.desc = true;
  previewSoon();
}

function buildToolbar() {
  const bar = $("#toolbar");
  for (const [label, title, open, close, block] of TOOLS) {
    const b = el("button", "tool", label);
    b.type = "button";
    b.title = title;
    if (label.length === 1) b.classList.add("tool-" + label.toLowerCase());
    b.addEventListener("click", () => wrap(open, close, block));
    bar.append(b);
  }
  // Image du PC (pochette cover.jpg, capture) : hébergée sur V3X avec la clé
  // du membre, insérée en [img] (CoprinusComatus, 21/10/26).
  const up = el("button", "tool", "Image du PC");
  up.type = "button";
  up.title = "Envoyer une image de ton PC (hébergée sur V3X) et l'insérer";
  const file = el("input");
  file.type = "file";
  file.accept = "image/png,image/jpeg,image/gif,image/webp";
  file.hidden = true;
  up.addEventListener("click", () => file.click());
  file.addEventListener("change", () => {
    const f = file.files && file.files[0];
    file.value = "";
    if (f) sendImage(f, up);
  });
  bar.append(up, file);
  const size = el("select", "tool");
  size.setAttribute("aria-label", "Taille du texte");
  size.append(new Option("Taille", ""));
  for (const n of [1, 2, 3, 4, 5, 6, 7]) size.append(new Option(String(n), String(n)));
  size.addEventListener("change", () => {
    if (size.value) wrap("[size=" + size.value + "]", "[/size]");
    size.value = "";
  });
  const color = el("input", "tool color");
  color.type = "color";
  color.value = "#3360e8";
  color.title = "Couleur";
  color.addEventListener("change", () => wrap("[color=" + color.value + "]", "[/color]"));
  bar.append(size, color);
}

async function sendImage(f, btn) {
  if (f.size > 16 * 1024 * 1024) return toast("Image trop lourde pour V3X (16 Mio au plus).", "bad");
  btn.disabled = true;
  toast("Envoi de l'image sur V3X…");
  try {
    const data = await new Promise((ok, ko) => {
      const rd = new FileReader();
      rd.onload = () => ok(String(rd.result).split(",")[1] || "");
      rd.onerror = () => ko(new Error("image illisible"));
      rd.readAsDataURL(f);
    });
    const r = await api("/ui/pub/image", { name: f.name, data });
    wrap("[img]" + r.url + "[/img]", "");
    toast("Image ajoutée à la présentation.", "ok");
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  } finally {
    btn.disabled = false;
  }
}

let prevTimer = null;
function previewSoon(delay) {
  clearTimeout(prevTimer);
  prevTimer = setTimeout(renderPreview, delay == null ? 500 : delay);
}

async function renderPreview() {
  const seq = ++pub.previewSeq;
  const bb = $("#f-desc").value;
  const html = pub.format === "html";
  $("#preview").classList.toggle("html-prev", html && !!bb.trim());
  if (!bb.trim()) {
    $("#preview").replaceChildren(el("p", "muted", "La présentation s'affichera ici."));
    return;
  }
  if (html) {
    htmlPreview(bb);
    return;
  }
  try {
    const r = await api("/ui/pub/preview", { bbcode: bb });
    if (seq !== pub.previewSeq) return; // une réponse plus récente arrive
    $("#preview").innerHTML = r.html;
  } catch (e) {
    if (seq !== pub.previewSeq || e.message === "session") return;
    $("#preview").replaceChildren(el("p", "bad", "Aperçu indisponible : " + e.message));
  }
}

// Aperçu d'une présentation HTML, rendu comme sur V3X : un cadre isolé où aucun
// script ne tourne (sandbox sans allow-scripts, et sa propre politique de
// sécurité), sans référent, liens http des images passés en https comme le
// fait le site. allow-same-origin ne sert qu'à mesurer la hauteur du contenu.
const HTML_PREVIEW_CSS =
  ":root{color-scheme:dark}" +
  "body{margin:0;padding:14px 16px;font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;color:#c9c9d4;background:transparent;font-size:14px;line-height:1.6}" +
  "a{color:#ff6b4a}img{max-width:100%;max-height:480px;width:auto;height:auto;object-fit:contain;border-radius:8px}" +
  "table{border-collapse:collapse}td,th{border:1px solid #333;padding:4px 8px}" +
  "h1,h2,h3,h4{color:#eee}code,pre{background:#111;border-radius:6px;padding:2px 6px;font-family:ui-monospace,monospace}" +
  "pre{padding:10px;overflow:auto}details{margin:6px 0}summary{cursor:pointer;color:#ff6b4a}" +
  "html,body{min-height:0 !important;height:auto !important}";

function htmlPreview(src) {
  const body = src.replace(/(\b(?:src|srcset|poster)\s*=\s*["']?\s*|url\(\s*["']?)http:\/\//gi, "$1https://");
  const doc =
    '<!doctype html><html><head><meta charset="utf-8">' +
    "<meta http-equiv=\"Content-Security-Policy\" content=\"upgrade-insecure-requests; default-src 'none'; img-src https: data:; media-src https:; style-src 'unsafe-inline'; font-src data:; script-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'\">" +
    '<meta name="referrer" content="no-referrer"><style>' + HTML_PREVIEW_CSS + "</style></head><body>" + body + "</body></html>";
  let f = $("#preview").querySelector("iframe");
  if (!f) {
    f = document.createElement("iframe");
    f.setAttribute("sandbox", "allow-same-origin");
    f.setAttribute("referrerpolicy", "no-referrer");
    f.title = "Aperçu de la présentation";
    f.addEventListener("load", () => fitFrame(f));
    $("#preview").replaceChildren(f);
  }
  f.srcdoc = doc;
}

// Le cadre prend la hauteur de son contenu, et la suit quand les images
// arrivent ou qu'un bloc <details> s'ouvre.
function fitFrame(f) {
  const d = f.contentDocument;
  if (!d || !d.body) return;
  const measure = () => {
    const h = Math.max(d.body.offsetHeight, d.documentElement.offsetHeight) + 4;
    if (h > 4) f.style.height = h + "px";
  };
  measure();
  const RO = (f.contentWindow && f.contentWindow.ResizeObserver) || window.ResizeObserver;
  if (RO) new RO(measure).observe(d.body);
  d.addEventListener("toggle", () => setTimeout(measure, 0), true);
}

function edTab(prev) {
  $("#ed-tab-edit").setAttribute("aria-selected", String(!prev));
  $("#ed-tab-prev").setAttribute("aria-selected", String(prev));
  $("#ed-panes").classList.toggle("show-prev", prev);
}

// ---------- publication ----------

function missing() {
  const out = [];
  const j = pub.job;
  if (!j) return ["release"];
  if (j.state === "error") out.push("le .torrent n'a pas pu être fabriqué");
  if (!currentCategory()) out.push("la sous-catégorie");
  if (isFilmSeries() && !pub.tmdb && !$("#f-notmdb").checked) out.push("la fiche TMDB");
  if (isFilmSeries() && !$("#f-nfo").value.trim()) out.push("le NFO (MediaInfo)");
  if (!$("#f-name").value.trim()) out.push("le nom de la release");
  // Musique : V3X refuse un nom sans la qualité (format, bits, fréquence).
  if (pub.fiche && (pub.fiche.warnings || []).includes("audio_naming")) out.push("la qualité dans le nom");
  if (needsVirusTotal() && !$("#f-vt").value.trim()) out.push("le rapport VirusTotal");
  return out;
}

function renderBar() {
  const bar = $("#pub-bar");
  const j = pub.job;
  const show = pub.shown && j && !j.published && !j.seedOnly;
  bar.classList.toggle("hidden", !show);
  if (j && j.seedOnly) $("#pub-form").classList.add("hidden"); // rien à publier : la release V3X part en seed
  $("#vt-field").classList.toggle("hidden", !needsVirusTotal() && !$("#f-vt").value.trim());
  if (!show) return;
  const miss = missing();
  const go = $("#pub-go");
  let txt;
  if (j.state === "publishing") txt = "Publication en cours…";
  else if (miss.length) txt = "Il manque : " + miss.join(", ");
  else if (j.state === "hashing") txt = "Fiche prête. Le .torrent se termine : " + Math.round((j.hashed / j.total) * 100) + " %";
  else txt = "Tout est prêt.";
  $("#pub-bar-txt").textContent = txt;
  go.disabled = miss.length > 0 || j.state !== "ready";
  // Garder pour le lot : la fiche complète suffit, le .torrent peut finir plus tard.
  const keep = $("#pub-keep");
  keep.classList.toggle("hidden", !pub.fromLot);
  keep.disabled = miss.length > 0 || j.state === "publishing";
}

async function keepForLot() {
  const f = fields();
  const leaf = currentCategory();
  const work = isAlbum()
    ? (pub.mb ? pub.mb.title + (pub.mb.date ? " (" + pub.mb.date.slice(0, 4) + ")" : "") : "")
    : isGame()
      ? (pub.game ? pub.game.name + (pub.game.year ? " (" + pub.game.year + ")" : "") : "")
      : (pub.tmdb ? pub.tmdb.title + (pub.tmdb.year ? " (" + pub.tmdb.year + ")" : "") : "");
  const summary = [leaf ? leaf.name : "", work, f.releaseName].filter(Boolean).join(" · ");
  try {
    await api("/ui/batch/prepare", { path: pub.fromLot, source: pub.source, name: pub.job.name, summary, fields: f });
    pub.fromLot = "";
    await cancelJob();
    toast("Fiche gardée : le lot la publiera telle quelle.", "ok");
    setView("batch");
  } catch (e) {
    if (e.message !== "session") toast(e.message, "bad");
  }
}

function fields() {
  const leaf = currentCategory();
  const f = {
    categoryId: leaf ? String(leaf.id) : "",
    description: $("#f-desc").value,
    descriptionFormat: pub.format === "html" && canHtml() ? "html" : "standard",
    title: $("#f-title").value.trim(),
    releaseName: $("#f-name").value.trim(),
    nfo: $("#f-nfo").value,
    language: $("#f-lang").value,
    anonymous: String($("#f-anon").checked),
    exclusive: String($("#f-excl").checked),
  };
  if ($("#f-root").value === "series" && $("#f-season").value.trim()) f.season = $("#f-season").value.trim();
  if (pub.tmdb && !isGame()) f.tmdbUrl = "https://www.themoviedb.org/" + pub.tmdb.type + "/" + pub.tmdb.id;
  // Jeu : la jaquette, comme la page d'upload (affiche de la fiche), ou celle
  // d'une fiche gardée rechargée tant qu'aucun autre jeu n'est choisi.
  const poster = gamePoster() || (!pub.game && pub.keptPoster) || "";
  if (poster) f.posterUrl = poster;
  // Musique : le lien MusicBrainz, comme sur la page d'upload (titre, jaquette).
  if (isAlbum() && pub.mb) {
    f.tmdbUrl = mbURL(pub.mb);
    f.creator = (pub.fiche && pub.fiche.creator) || pub.mb.artist || "";
  }
  // Livre : le lien ou l'ISBN, comme sur la page d'upload. Le site en tire
  // l'auteur et la couverture (la première image de la présentation passe
  // devant celle de l'ISBN, souvent petite).
  if (isBook() && pub.book && pub.fiche && pub.fiche.book) f.tmdbUrl = pub.book.ref;
  if ($("#f-notmdb").checked) f.noTmdb = "true";
  const vt = $("#f-vt").value.trim();
  if (vt) f.virusTotalUrl = vt;
  if (pub.ctx && pub.ctx.user && pub.ctx.user.team) f.teamRelease = String($("#f-team").checked);
  return f;
}

function askPublish() {
  const f = fields();
  const dl = $("#pub-confirm-list");
  dl.replaceChildren();
  const leaf = currentCategory();
  const rows = [
    ["Release", f.releaseName],
    ["Catégorie", leaf ? leaf.name : ""],
    isAlbum()
      ? ["Album", pub.mb ? pub.mb.title + (pub.mb.date ? " (" + pub.mb.date.slice(0, 4) + ")" : "") + " · MusicBrainz" : "aucune fiche MusicBrainz"]
      : isGame()
        ? ["Jeu", pub.game ? pub.game.name + (pub.game.year ? " (" + pub.game.year + ")" : "") : "aucun jeu choisi"]
        : ["Œuvre", pub.tmdb ? pub.tmdb.title + (pub.tmdb.year ? " (" + pub.tmdb.year + ")" : "") : "aucune"],
    ["Taille", fmtSize(pub.job.total)],
    ["Visibilité", f.anonymous === "true" ? "anonyme" : "sous ton pseudo"],
    ["Seed", "ajoutée à " + (multiClients() ? "« " + pub.job.clientName + " »" : "ton client") + " dans « " + pub.job.clientDir + " »"],
  ];
  for (const [k, v] of rows) dl.append(el("dt", null, k), el("dd", null, v));
  $("#f-rights").checked = false;
  $("#pub-confirm-yes").disabled = true;
  $("#pub-confirm").showModal();
}

async function doPublish() {
  $("#pub-confirm").close();
  $("#pub-error").classList.add("hidden");
  try {
    pub.job = await api("/ui/pub/" + pub.job.id + "/publish", { fields: fields() });
    renderJob();
    pollJob();
    watchPublish();
  } catch (e) {
    if (e.message !== "session") showPubError(e.message);
  }
}

// Attend la réponse du site : erreur (on reste sur la fiche) ou publiée.
function watchPublish() {
  const tick = async () => {
    if (!pub.job) return;
    try { pub.job = await api("/ui/pub/" + pub.job.id); } catch (e) { return; }
    const j = pub.job;
    if (j.state === "publishing") return setTimeout(tick, 700);
    if (!j.published && j.error) showPubError(pubErr(j.error, j.errorBody));
    renderJob();
  };
  setTimeout(tick, 700);
}

function showPubError(msg) {
  const n = $("#pub-error");
  n.textContent = "V3X a refusé la publication : " + msg + ".";
  n.classList.remove("hidden");
  n.scrollIntoView({ block: "center", behavior: "smooth" });
}

// État du seed d'une release déjà sur V3X, mise en seed avec ces fichiers
// (SeedExisting) : mêmes étapes qu'après une publication.
function seedText(j, what) {
  if (j.seedError) return "Seed : " + j.seedError + ".";
  if (j.seedNote) return "Seed : " + j.seedNote + ".";
  if (j.state === "seeding") return "Seed : ajout de " + what + " à ton client…";
  if (!j.seed || (j.seed.status === "checking" && !j.seed.detail)) return "Seed : ton client vérifie les fichiers" + (j.seed && j.seed.progress ? " (" + Math.round(j.seed.progress * 100) + " %)" : "") + "…";
  if (j.seed.status === "seeding") return "Seed : vérifié à 100 %, " + what + " est en seed.";
  return "Seed : " + (j.seed.detail || j.seed.status);
}

function renderDone() {
  const j = pub.job;
  const box = $("#pub-done");
  box.replaceChildren();
  const h = el("h2", null, j.published.status === "active" ? "Publiée sur V3X" : "Envoyée, en attente de validation");
  const a = el("a", "btn primary", "Voir la fiche");
  a.href = "https://v3x.club/torrents/" + j.published.id;
  a.target = "_blank";
  a.rel = "noopener";
  const p = el("p", "muted");
  if (j.published.uploadBonus) p.textContent = "Prime d'upload : " + j.published.uploadBonus.toLocaleString("fr-FR") + " jetons. ";
  let seed;
  if (j.seedError) seed = "Seed : " + j.seedError + ". Ajoute le .torrent à ton client à la main, depuis la fiche.";
  else if (!j.seed || (j.seed.status === "checking" && !j.seed.detail)) seed = "Seed : ton client vérifie les fichiers" + (j.seed && j.seed.progress ? " (" + Math.round(j.seed.progress * 100) + " %)" : "") + "…";
  else if (j.seed.status === "seeding") seed = "Seed : vérifié à 100 %, ta release est en seed.";
  else seed = "Seed : " + (j.seed.detail || j.seed.status);
  const s = el("p", null, seed);
  const again = el("button", "btn", "Publier une autre release");
  again.type = "button";
  again.addEventListener("click", cancelJob);
  const row = el("div", "row");
  row.append(a, again);
  box.append(h, p, s, row);
  const reqBox = el("div", "pub-reqs");
  box.append(reqBox);
  if (!pub.reqsFor || pub.reqsFor !== j.id) {
    pub.reqsFor = j.id;
    pub.reqHits = null;
    api("/ui/requests/for?name=" + encodeURIComponent(j.published.name || $("#f-name").value))
      .then((r) => { pub.reqHits = (r.requests || []).filter((x) => !x.mine); if (pub.job && pub.job.id === j.id) renderDone(); })
      .catch(() => { pub.reqHits = []; });
  }
  if (pub.reqHits && pub.reqHits.length) {
    reqBox.append(el("h3", null, plural(pub.reqHits.length, "Un membre la demande", "Des membres la demandent")));
    if (j.published.status === "active") {
      const r = el("div", "row wrap");
      for (const x of pub.reqHits.slice(0, 3)) r.append(proposeButton(x, j.published.id));
      reqBox.append(r, el("p", "hint", "Vérifie que c'est bien ce qui est demandé (qualité, langue) : le demandeur valide, et la prime t'est versée."));
    } else {
      reqBox.append(el("p", "hint", "Une fois ta release validée, propose-la depuis la demande sur V3X :"), el("div", "reqs", null));
      reqBox.lastChild.append(...requestBadges(pub.reqHits));
    }
  }
  box.classList.remove("hidden");
  $("#pub-form").classList.add("hidden");
  if (!pub.published) {
    pub.published = true;
    toast(j.published.status === "active" ? "Release publiée." : "Release envoyée en validation.", "ok");
  }
}

// ---------- branchements ----------

// Ce PC ou la seedbox : changer vide le sélecteur et repart de l'accueil
// de la source (dossier personnel sur la seedbox).
window.pubSources = () => {
  const box = $("#pub-src");
  if (pub.source === "ssh" && !(ui.state && ui.state.ssh && ui.state.ssh.ok)) pub.source = "local";
  const shown = renderSourceSwitch(box, pub.source, (id) => {
    pub.source = id;
    window.pubSources();
    if (!pub.job) browse(id === "local" ? (ui.state && ui.state.lastDir) || "" : "");
  });
  box.classList.toggle("hidden", !shown || !!pub.job);
};

// Ouvre une release dans Publier (depuis « Pas sur V3X »).
window.pubOpen = async (path, source) => {
  if (pub.job && !pub.job.published) {
    setView("publish");
    return toast("Une fiche est déjà ouverte dans Publier : publie-la ou « Changer de release » d'abord.", "bad");
  }
  if (pub.job) await cancelJob();
  setView("publish");
  await choose(path, source);
};

window.pubShow = async (on) => {
  pub.shown = on;
  renderBar();
  if (!on) return;
  if (!pub.ctx) await loadCtx();
  if (!pub.job && !$("#fs-list").children.length) {
    let start = "";
    try { const s = await api("/ui/state"); start = s.lastDir || ""; } catch (e) { /* rien */ }
    browse(start);
  }
};

function bindPublish() {
  buildToolbar();
  $("#fs-up").addEventListener("click", () => browse($("#fs-up").dataset.parent || ""));
  $("#fs-go").addEventListener("click", () => browse($("#fs-path").value));
  $("#fs-path").addEventListener("keydown", (e) => { if (e.key === "Enter") browse($("#fs-path").value); });
  $("#job-cancel").addEventListener("click", cancelJob);
  $("#map-save").addEventListener("click", saveMap);
  $("#pub-client").addEventListener("change", pickClient);
  $("#tmdb-go").addEventListener("click", searchTmdb);
  $("#tmdb-q").addEventListener("keydown", (e) => { if (e.key === "Enter") searchTmdb(); });
  $("#f-notmdb").addEventListener("change", () => { if ($("#f-notmdb").checked) { pub.tmdb = null; $("#tmdb-picked").classList.add("hidden"); loadFicheSoon(); } renderBar(); });
  $("#f-root").addEventListener("change", () => { pub.dirty.category = true; fillLeaves($("#f-root").value, ""); gameSetup(); versionsSoon(); renderBar(); });
  $("#game-go").addEventListener("click", searchGame);
  $("#game-q").addEventListener("keydown", (e) => { if (e.key === "Enter") searchGame(); });
  $("#book-go").addEventListener("click", useBook);
  $("#book-q").addEventListener("keydown", (e) => { if (e.key === "Enter") useBook(); });
  // Champ vidé : le livre est oublié, la fiche se refait sans (MiTor, 25/10/26).
  $("#book-q").addEventListener("input", () => {
    if ($("#book-q").value.trim() || !pub.book) return;
    pub.book = null;
    $("#book-picked").classList.add("hidden");
    $("#book-state").textContent = BOOK_HINT;
    loadFicheSoon();
  });
  $("#f-leaf").addEventListener("change", () => { pub.dirty.category = true; loadFicheSoon(); renderBar(); });
  $("#f-vt").addEventListener("input", renderBar);
  $("#f-name").addEventListener("input", () => { pub.dirty.name = true; loadFicheSoon(); renderBar(); });
  for (const [id, key] of [["#f-title", "title"], ["#f-season", "season"], ["#f-nfo", "nfo"], ["#f-lang", "language"]]) {
    $(id).addEventListener("input", () => { pub.dirty[key] = true; renderBar(); });
  }
  $("#f-team").addEventListener("change", () => { pub.dirty.team = true; loadFicheSoon(); });
  $("#f-tpl").addEventListener("change", () => loadFiche(false));
  // .nfo de la release (quebecman) : option gardée sur l'appareil, appliquée
  // tout de suite au NFO de la fiche ouverte.
  $("#f-keepnfo").checked = keepNfoOn();
  $("#f-keepnfo").addEventListener("change", (ev) => {
    try { localStorage.setItem("d3x-keep-nfo", ev.target.checked ? "1" : "0"); } catch (e) { /* rien */ }
    if (ev.target.checked && !pub.localNfo && pub.job) toast("Cette release n'a pas de .nfo : le NFO de V3X reste.", "");
    const nfo = ev.target.checked && pub.localNfo ? nfoWithMediaInfo(pub.localNfo, pub.mediainfo) : (pub.fiche && pub.fiche.nfo) || "";
    if (nfo) {
      $("#f-nfo").value = nfo;
      pub.dirty.nfo = false;
    }
  });
  $("#regen").addEventListener("click", () => { pub.dirty.desc = false; setFormat("standard"); loadFiche(false); });
  // Présentation chargée d'un fichier (GoodMoviesForBadPeople, 21/10/26) :
  // .html en HTML si V3X l'autorise, sinon le texte part en BBCode.
  $("#desc-file").addEventListener("click", () => $("#desc-file-in").click());
  $("#desc-file-in").addEventListener("change", async (ev) => {
    const f = ev.target.files && ev.target.files[0];
    ev.target.value = "";
    if (!f) return;
    if (f.size > 2 * 1024 * 1024) return toast("Fichier trop lourd pour une présentation (2 Mio au plus).", "bad");
    const isHtml = /\.html?$/i.test(f.name) || f.type === "text/html";
    if (isHtml && !canHtml()) return toast("V3X ne t'autorise pas encore la présentation en HTML : charge un .txt en BBCode.", "bad");
    let text;
    try {
      text = await f.text();
    } catch (e) {
      return toast("Fichier illisible.", "bad");
    }
    $("#f-desc").value = text;
    pub.dirty.desc = true;
    setFormat(isHtml ? "html" : "standard");
    toast("Présentation chargée depuis " + f.name + ".", "ok");
  });
  $("#fmt-bb").addEventListener("click", () => chooseFormat("standard"));
  $("#fmt-html").addEventListener("click", () => chooseFormat("html"));
  $("#f-desc").addEventListener("input", () => { pub.dirty.desc = true; previewSoon(); });
  $("#f-desc").addEventListener("keydown", (e) => {
    if (!(e.ctrlKey || e.metaKey) || pub.format === "html") return;
    const k = e.key.toLowerCase();
    if (k === "b" || k === "i" || k === "u") {
      e.preventDefault();
      wrap("[" + k + "]", "[/" + k + "]");
    }
  });
  $("#ed-tab-edit").addEventListener("click", () => edTab(false));
  $("#ed-tab-prev").addEventListener("click", () => edTab(true));
  $("#pub-go").addEventListener("click", askPublish);
  $("#pub-keep").addEventListener("click", keepForLot);
  $("#f-rights").addEventListener("change", () => { $("#pub-confirm-yes").disabled = !$("#f-rights").checked; });
  $("#pub-confirm-no").addEventListener("click", () => $("#pub-confirm").close());
  $("#pub-confirm-yes").addEventListener("click", doPublish);
}

bindPublish();

// Les séries mémorisées, dès le chargement (publish.js est chargé après app.js :
// l'appel ne peut pas être dans boot(), qui tourne avant que loadSeries existe).
loadSeries();
