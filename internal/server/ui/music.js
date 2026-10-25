"use strict";

// Musique, dans Publier et dans Lot. D3X lit les tags et la qualité des
// pistes (sur ce PC ou sur la seedbox), cherche l'album dans MusicBrainz et
// compare chaque édition aux pistes : nombre, puis durées. La fiche album est
// celle du site (lien MusicBrainz).

const MB_MATCH = {
  exact: ["ok", "Mêmes pistes, mêmes durées"],
  close: ["warn", "Durées proches"],
  count: ["", "Même nombre de pistes"],
};

function isAlbum() {
  return !!(pub.job && pub.job.audio && pub.job.audio.tracks && pub.job.audio.tracks.length);
}

function fmtLength(s) {
  s = Math.round(s || 0);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h ? h + " h " + String(m).padStart(2, "0") : m + " min";
}

function mbCover(c) {
  return c && c.coverArchive && /^[0-9a-f-]{36}$/.test(c.id) ? "https://coverartarchive.org/release/" + c.id + "/front-250" : "";
}

function mbURL(c) {
  return "https://musicbrainz.org/release/" + c.id;
}

// Audio ▸ Musique sur V3X.
function musicCategory() {
  for (const root of (pub.ctx && pub.ctx.categories) || []) {
    if (root.slug !== "audio") continue;
    const leaf = (root.children || []).find((c) => c.name === "Musique");
    if (leaf) return { root, leaf };
  }
  return null;
}

// Nom de release avec la qualité exigée par V3X (« FLAC 24bit-96kHz ») : le
// format déjà écrit est complété, sinon la qualité est ajoutée à la fin, dans
// le style du nom (espaces, points ou tirets).
function nameWithToken(name, token) {
  if (!token) return name;
  const codec = token.split(" ")[0];
  const spaced = /\s/.test(name);
  const sep = spaced ? " " : name.includes(".") ? "." : "-";
  const styled = spaced ? token : token.replace(/ /g, sep);
  const re = new RegExp("(^|[^A-Za-z0-9])" + codec + "(?![A-Za-z0-9])", "i");
  if (re.test(name)) return name.replace(re, (m, pre) => pre + styled);
  return spaced ? name + " [" + token + "]" : name + sep + styled;
}

function albumSummary(a) {
  const bits = [a.tracks.length + (a.tracks.length > 1 ? " pistes" : " piste")];
  if (a.duration) bits.push(fmtLength(a.duration));
  if (a.discs > 1) bits.push(a.discs + " disques");
  if (a.token) bits.push(a.token);
  else if (a.codec) bits.push(a.codec);
  if (a.mixed) bits.push("qualités mélangées");
  return bits.join(" · ");
}

// ---------- Publier ----------

window.musicSetup = () => {
  const on = isAlbum();
  $("#tmdb-field").classList.toggle("hidden", on);
  $("#mb-field").classList.toggle("hidden", !on);
  pub.mb = null;
  $("#mb-results").replaceChildren();
  $("#mb-picked").classList.add("hidden");
  $("#f-nomb").checked = false;
  $("#mb-state").textContent = "";
  $("#dg-results").replaceChildren();
  // Pochette Discogs : seulement avec un jeton Discogs dans les Réglages.
  $("#dg-box").classList.toggle("hidden", !on || !(ui.state && ui.state.discogs));
  if (!on) return;
  const a = pub.job.audio;
  const loc = $("#mb-local");
  loc.replaceChildren(el("b", null, albumSummary(a)));
  const who = [a.artist || (a.various ? "Artistes variés" : ""), a.title].filter(Boolean).join(" – ");
  if (who) loc.append(el("span", "muted", "Tags : " + who + (a.year ? " (" + a.year + ")" : "")));
  else loc.append(el("span", "muted", "Pas de tags d'album : la recherche part du nom du dossier."));
  if (a.mbRelease) loc.append(el("span", "pill ok", "Édition MusicBrainz dans les tags"));
  $("#mb-q").value = a.artist && a.title ? a.artist + " - " + a.title : "";
  // Audio ▸ Musique, tant que le membre n'a rien choisi.
  const mc = musicCategory();
  if (mc && !pub.dirty.category) {
    $("#f-root").value = mc.root.slug;
    fillLeaves(mc.root.slug, mc.leaf.slug);
  }
  mbDecide();
};

async function mbDecide() {
  const j = pub.job;
  $("#mb-state").textContent = "Recherche de l'album sur MusicBrainz…";
  try {
    const r = await api("/ui/pub/" + j.id + "/mb/decide", {});
    if (!pub.job || pub.job.id !== j.id) return;
    renderMbList(r.candidates || []);
    if (r.certain && r.best) {
      pickMb(r.best, true);
      $("#mb-state").textContent = "Édition retrouvée : " + r.reason + ".";
    } else {
      $("#mb-state").textContent = (r.reason ? "À choisir : " + r.reason + "." : "") + (r.candidates && r.candidates.length ? " Choisis l'édition qui correspond." : "");
    }
  } catch (e) {
    if (e.message !== "session") $("#mb-state").textContent = "MusicBrainz : " + e.message;
  }
}

async function mbSearch() {
  const j = pub.job;
  if (!j) return;
  $("#mb-state").textContent = "Recherche sur MusicBrainz…";
  $("#mb-results").replaceChildren();
  try {
    const r = await api("/ui/pub/" + j.id + "/mb/search", { q: $("#mb-q").value.trim() });
    if (!pub.job || pub.job.id !== j.id) return;
    renderMbList(r.candidates || []);
    $("#mb-state").textContent = r.candidates && r.candidates.length ? "" : "Aucune édition trouvée. Essaie « Artiste - Album », ou colle le lien MusicBrainz de l'édition.";
  } catch (e) {
    if (e.message !== "session") $("#mb-state").textContent = "MusicBrainz : " + e.message;
  }
}

async function mbLookup(id) {
  const j = pub.job;
  $("#mb-state").textContent = "Lecture de l'édition…";
  try {
    const c = await api("/ui/pub/" + j.id + "/mb/release/" + id);
    if (!pub.job || pub.job.id !== j.id) return;
    $("#mb-state").textContent = "";
    pickMb(c);
  } catch (e) {
    if (e.message !== "session") $("#mb-state").textContent = "MusicBrainz : " + e.message;
  }
}

function mbMeta(c) {
  return [c.artist, c.date ? c.date.slice(0, 4) : "", c.format, c.country, c.label, c.trackCount ? c.trackCount + " pistes" : ""].filter(Boolean).join(" · ");
}

function mbBadges(c) {
  const out = [];
  const m = MB_MATCH[c.match];
  if (m) out.push(el("span", "pill " + m[0], m[1]));
  if (c.fromTags) out.push(el("span", "pill ok", "Désignée par les tags"));
  if (c.match === "none" && c.reasons && c.reasons[0]) out.push(el("span", "pill warn", c.reasons[0]));
  return out;
}

function coverEl(c, cls) {
  const src = mbCover(c);
  if (!src) return el("span", "noposter " + (cls || ""), "♪");
  const img = el("img", cls || "");
  img.src = src;
  img.alt = "";
  img.loading = "lazy";
  img.addEventListener("error", () => img.replaceWith(el("span", "noposter " + (cls || ""), "♪")));
  return img;
}

function renderMbList(list) {
  const ul = $("#mb-results");
  ul.replaceChildren();
  for (const c of list.slice(0, 12)) {
    const li = el("li");
    const b = el("button", "mb-item");
    b.type = "button";
    const meta = el("span", "tmdb-meta");
    meta.append(el("b", null, c.title), el("span", "muted small", mbMeta(c)));
    const badges = el("span", "mb-badges");
    badges.append(...mbBadges(c));
    meta.append(badges);
    b.append(coverEl(c), meta);
    b.addEventListener("click", () => pickMb(c));
    li.append(b);
    ul.append(li);
  }
}

function pickMb(c, auto) {
  pub.mb = c;
  $("#f-nomb").checked = false;
  $("#mb-results").replaceChildren();
  const p = $("#mb-picked");
  p.replaceChildren(coverEl(c));
  const t = el("div");
  const a = el("a", null, c.title + (c.date ? " (" + c.date.slice(0, 4) + ")" : ""));
  a.href = mbURL(c);
  a.target = "_blank";
  a.rel = "noopener";
  t.append(a, el("div", "muted small", mbMeta(c)));
  const badges = el("div", "mb-badges");
  badges.append(...mbBadges(c));
  if (auto) badges.append(el("span", "pill", "Choisie par D3X"));
  t.append(badges);
  const ch = el("button", "link", "Changer");
  ch.type = "button";
  ch.addEventListener("click", () => {
    pub.mb = null;
    p.classList.add("hidden");
    mbSearch();
    loadFicheSoon();
  });
  t.append(ch);
  p.append(t);
  p.classList.remove("hidden");
  loadFiche();
}

// Le champ accepte aussi un lien MusicBrainz d'édition.
function mbGo() {
  const q = $("#mb-q").value.trim();
  const m = q.match(/musicbrainz\.org\/release\/([0-9a-f-]{36})/i) || q.match(/^([0-9a-f-]{36})$/i);
  if (m) return mbLookup(m[1].toLowerCase());
  mbSearch();
}

// Bouton « ajouter la qualité au nom » sous les remarques de la fiche.
window.musicNameFix = (box) => {
  const a = pub.job && pub.job.audio;
  if (!a || !a.token) return;
  const next = nameWithToken($("#f-name").value.trim(), a.token);
  if (next === $("#f-name").value.trim()) return;
  const b = el("button", "link", "Ajouter « " + a.token + " » au nom");
  b.type = "button";
  b.addEventListener("click", () => {
    $("#f-name").value = next;
    pub.dirty.name = true;
    loadFicheSoon();
  });
  box.append(b);
};

// ---------- Pochette Discogs (CoprinusComatus, 21/10/26) ----------

function dgQuery() {
  const q = $("#mb-q").value.trim();
  if (q && !/musicbrainz\.org/i.test(q)) return q;
  if (pub.mb) return [pub.mb.artist, pub.mb.title].filter(Boolean).join(" ");
  const a = pub.job && pub.job.audio;
  return (a && [a.artist, a.title].filter(Boolean).join(" ")) || (pub.job ? pub.job.name : "");
}

async function dgSearch() {
  const ul = $("#dg-results");
  const q = dgQuery();
  if (!q) return toast("Écris l'artiste et l'album dans la recherche MusicBrainz.", "bad");
  ul.replaceChildren(el("li", "muted small", "Recherche sur Discogs…"));
  try {
    const r = await api("/ui/discogs/search?q=" + encodeURIComponent(q));
    ul.replaceChildren();
    if (!r.results || !r.results.length) return ul.append(el("li", "muted small", "Rien sur Discogs pour « " + q + " »."));
    for (const c of r.results) {
      const li = el("li");
      const b = el("button", "mb-item");
      b.type = "button";
      let cover;
      if (c.thumb) {
        cover = el("img");
        cover.src = c.thumb;
        cover.alt = "";
        cover.loading = "lazy";
      } else cover = el("span", "noposter", "♪");
      const meta = el("span", "tmdb-meta");
      meta.append(el("b", null, c.title), el("span", "muted small", [c.year, c.country, (c.format || []).join(", "), (c.label || []).join(", ")].filter(Boolean).join(" · ")));
      b.append(cover, meta);
      b.addEventListener("click", () => dgPick(c, b));
      li.append(b);
      ul.append(li);
    }
  } catch (e) {
    ul.replaceChildren();
    if (e.message !== "session") toast(e.message, "bad");
  }
}

// La pochette choisie est hébergée sur V3X puis placée en tête de la présentation.
async function dgPick(c, btn) {
  btn.disabled = true;
  toast("Pochette : récupération sur Discogs et envoi sur V3X…");
  try {
    const r = await api("/ui/discogs/cover", { id: c.id });
    const ta = $("#f-desc");
    ta.value = "[center][img]" + r.url + "[/img][/center]\n" + ta.value;
    pub.dirty.desc = true;
    previewSoon(0);
    $("#dg-results").replaceChildren();
    toast("Pochette ajoutée en tête de la présentation.", "ok");
  } catch (e) {
    btn.disabled = false;
    if (e.message !== "session") toast(e.message, "bad");
  }
}

function bindMusic() {
  $("#dg-go").addEventListener("click", dgSearch);
  $("#mb-go").addEventListener("click", mbGo);
  $("#mb-q").addEventListener("keydown", (e) => { if (e.key === "Enter") mbGo(); });
  $("#f-nomb").addEventListener("change", () => {
    if ($("#f-nomb").checked) {
      pub.mb = null;
      $("#mb-picked").classList.add("hidden");
      loadFicheSoon();
    }
  });
}

document.addEventListener("DOMContentLoaded", bindMusic);
