"use strict";
// MediaKeeper web interface: a single page without dependencies. Pages are
// chosen by the address after "#"; everything is built with DOM calls, so
// titles and file names can never be read as markup.

const app = document.getElementById("app");
let me = null;       // the signed-in user
let library = null;  // {name, movies, shows}
let pollTimer = null;

// h("div", {class: "x", onclick: f}, child, ...) creates an element.
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v == null) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "style") el.style.cssText = v;
    else if (k in el && k !== "list") el[k] = v;
    else el.setAttribute(k, v);
  }
  for (const c of children.flat(3)) {
    if (c == null || c === false) continue;
    el.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return el;
}

async function api(path, options = {}) {
  if (options.json !== undefined) {
    options.method = options.method || "POST";
    options.headers = { "Content-Type": "application/json" };
    options.body = JSON.stringify(options.json);
  }
  const resp = await fetch("/api/" + path, options);
  const data = await resp.json().catch(() => ({}));
  if (resp.status === 401 && me) { me = null; render(); }
  if (!resp.ok) throw new Error(data.error || resp.statusText);
  return data;
}

function time(sec) {
  sec = Math.max(0, Math.floor(sec || 0));
  const hrs = Math.floor(sec / 3600), min = Math.floor(sec / 60) % 60, s = sec % 60;
  return (hrs ? hrs + ":" + String(min).padStart(2, "0") : min) + ":" + String(s).padStart(2, "0");
}
function minutes(sec) {
  const m = Math.round((sec || 0) / 60);
  return m >= 60 ? Math.floor(m / 60) + " h " + (m % 60) + " min" : m ? m + " min" : "";
}
function bytes(n) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + " " + units[i];
}
const image = (id, kind) => `url("/api/image/${id}/${kind}")`;
const sameText = (a, b) => (a || "").toLowerCase() === (b || "").toLowerCase();
const fullTitle = x => x.localTitle && !sameText(x.localTitle, x.title) ? `${x.title} / ${x.localTitle}` : x.title;

// ---------------------------------------------------------------- routing

window.addEventListener("hashchange", render);

async function start() {
  // The first start: no account yet, the server is set up here.
  try {
    const setup = await api("setup");
    if (setup.needed) return renderSetup(setup);
  } catch { /* an older server: no setup */ }
  try { me = await api("me"); } catch { me = null; }
  render();
}

let rendered = { path: null, query: null }; // the page last drawn
let renders = 0;

async function render() {
  clearInterval(pollTimer);
  document.querySelectorAll(".modal").forEach(m => m.remove());
  const [path, query] = (location.hash.slice(1) || "movies").split("?");
  const [page, arg, ...rest] = path.split("/");
  // Without an account one may watch (if the server allows it), not manage.
  if (!me || (page === "login" && me.guest)) return renderLogin();
  // Another order or filter of the same list needs no new library (the page
  // drawn again as it is does: something changed); a page drawn later than
  // this one wins over it.
  const reorder = path === rendered.path && (query || "") !== rendered.query;
  const mine = ++renders;
  try {
    if (!library || (!reorder && ["movies", "shows", "movie", "show", "browse", "mine"].includes(page))) library = await api("library");
  } catch (err) {
    if (!me) return;
    return shell(page, h("p", { class: "error" }, err.message));
  }
  if (mine !== renders) return;
  rendered = { path, query: query || "" };
  document.title = library.name;
  switch (page) {
    case "shows": return backAtTitle(renderList("shows", library.shows.map(asShow), query));
    case "movie": return renderMovie(arg);
    case "show": return renderShow(arg);
    case "browse": return backAtTitle(renderBrowse(arg, decodeURIComponent(rest.join("/")), query));
    case "downloads": return me.admin ? renderDownloads() : (location.hash = "#movies");
    case "settings": return me.admin ? renderSettings(arg) : (location.hash = "#movies");
    case "users": return (location.hash = me.admin ? "#settings/users" : "#movies");
    case "mine": return me.guest ? (location.hash = "#login") : renderMine(arg || "continue");
    default: return backAtTitle(renderList("movies", library.movies, query));
  }
}

// Line icons for the navigation, 24×24, drawn with the current text colour.
const icons = {
  movies: '<svg viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="14" rx="3"/><path d="M3 9h18M8 5l-1 4M13 5l-1 4M18 5l-1 4"/></svg>',
  shows: '<svg viewBox="0 0 24 24"><rect x="3" y="6" width="18" height="12" rx="3"/><path d="M8 21h8M9 2l3 4 3-4"/></svg>',
  downloads: '<svg viewBox="0 0 24 24"><path d="M12 4v11M7 10l5 5 5-5M5 19h14"/></svg>',
  users: '<svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="4"/><path d="M4 20c1.5-4 4.5-6 8-6s6.5 2 8 6"/></svg>',
  settings: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1"/><circle cx="12" cy="12" r="6.5"/></svg>',
  folder: '<svg viewBox="0 0 24 24"><path d="M3 7.5A2.5 2.5 0 0 1 5.5 5H10l2 2.5h6.5A2.5 2.5 0 0 1 21 10v7.5a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 17.5z"/></svg>',
  up: '<svg viewBox="0 0 24 24"><path d="M12 19V5M6 11l6-6 6 6"/></svg>',
  play: '<svg viewBox="0 0 24 24"><path d="M8 5.5v13l11-6.5z" fill="currentColor" stroke="none"/></svg>',
  fullscreen: '<svg viewBox="0 0 24 24"><path d="M4 9V4h5M15 4h5v5M20 15v5h-5M9 20H4v-5"/></svg>',
  mine: '<svg viewBox="0 0 24 24"><path d="M6.5 3.5h11v17l-5.5-4-5.5 4z"/></svg>',
  pause: '<svg viewBox="0 0 24 24"><path d="M7 5h3.5v14H7zM13.5 5H17v14h-3.5z" fill="currentColor" stroke="none"/></svg>',
  sound: '<svg viewBox="0 0 24 24"><path d="M4 9.5h3.5L12 5.5v13l-4.5-4H4z" fill="currentColor"/><path d="M15.5 9a4.5 4.5 0 0 1 0 6M18 6.5a8 8 0 0 1 0 11"/></svg>',
  muted: '<svg viewBox="0 0 24 24"><path d="M4 9.5h3.5L12 5.5v13l-4.5-4H4z" fill="currentColor"/><path d="M16 9.5l5 5M21 9.5l-5 5"/></svg>',
  pip: '<svg viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="14" rx="2.5"/><rect x="12" y="11.5" width="7" height="5.5" rx="1" fill="currentColor"/></svg>',
};
const icon = name => h("span", { class: "icon", innerHTML: icons[name] });

// The blurred picture behind the whole page: the backdrop of the title that
// is open, or none (the plain background) on the lists. A title page asks
// for it while it is being built; shell() puts it up.
let pendingAmbient = null;
function setAmbient(url) {
  document.getElementById("ambient").style.backgroundImage = url || "";
  document.body.classList.toggle("has-ambient", !!url);
}

// The search narrows the posters on the page. It stays while the list is
// sorted or filtered anew, and after a visit to a title; choosing a section
// in the header clears it.
let searchTerm = "";
function applySearch() {
  const term = searchTerm.trim().toLowerCase();
  let shown = 0;
  for (const card of document.querySelectorAll("main .card")) {
    const hide = !!term && !card.dataset.text.includes(term);
    card.classList.toggle("hidden", hide);
    if (!hide) shown++;
  }
  const grid = document.querySelector("main .grid");
  let note = document.querySelector("main .search-empty");
  if (grid && !shown && term) {
    if (!note) grid.after(note = h("div", { class: "empty search-empty" }));
    note.textContent = `Nothing here matches “${searchTerm.trim()}”.`;
  } else if (note) note.remove();
}

function shell(page, ...content) {
  const link = (id, label, extra) => h("a", { href: "#" + id, class: page === id ? "active" : "", onclick: () => { freshVisit = true; searchTerm = ""; } },
    icon(id), h("span", { class: "label" }, label), extra);
  setAmbient(pendingAmbient);
  pendingAmbient = null;
  // The sections: in the header on a wide screen, in a floating tab bar at
  // the bottom on a phone.
  const sections = cls => h("nav", { class: cls },
    link("movies", "Movies"), link("shows", "Shows"), !me.guest && link("mine", "My"),
    me.admin && link("downloads", "Downloads", h("span", { class: "badge hidden attention" })),
    me.admin && link("settings", "Settings"));
  // The search field of the page before stays, as it is — what is typed,
  // the caret, the keyboard of a phone — when the list is drawn again in
  // another order or with other filters.
  const before = document.querySelector("header input[type=search]");
  const typing = before && document.activeElement === before;
  const search = before || h("input", {
    type: "search", placeholder: "Search", "aria-label": "Search", value: searchTerm,
    oninput: () => { searchTerm = search.value; applySearch(); },
  });
  if (before && before.value !== searchTerm) before.value = searchTerm;
  app.replaceChildren(
    h("header", { class: "glass" },
      h("a", { class: "brand", href: "#movies" }, library ? library.name : "MediaKeeper"),
      sections("glass"),
      h("span", { class: "spacer" }),
      ["movies", "shows", "browse"].includes(page) && search,
      me.guest ? h("a", { class: "button small", href: "#login" }, "Sign in") : [
        h("span", { class: "account" }, me.name),
        h("button", { class: "small", onclick: signOut }, "Sign out")]),
    h("main", {}, ...content),
    sections("tabbar glass"));
  if (!document.querySelector("main .grid")) search.remove(); // it filters the posters of a list
  else {
    applySearch();
    if (typing) search.focus({ preventScroll: true });
  }
  window.scrollTo(0, 0);
  if (me.admin) refreshBadge();
}

async function signOut() {
  await api("logout", { method: "POST" });
  // Back to watching as a guest where the server allows it, else to the login.
  try { me = await api("me"); } catch { me = null; }
  library = null;
  location.hash = "#movies";
  render();
}

async function refreshBadge(list) {
  try {
    list = list || await api("downloads");
    const n = list.filter(d => d.state === "attention").length;
    for (const badge of document.querySelectorAll(".attention")) { badge.textContent = n; badge.classList.toggle("hidden", !n); }
  } catch { /* the badge is not worth an error message */ }
}

// ------------------------------------------------------------------ login

function renderLogin() {
  const user = h("input", { type: "text", autocomplete: "username", id: "user" });
  const pass = h("input", { type: "password", autocomplete: "current-password", id: "pass" });
  const error = h("p", { class: "error" });
  document.title = "MediaKeeper";
  app.replaceChildren(h("form", {
    class: "login glass",
    onsubmit: async e => {
      e.preventDefault();
      try {
        me = await api("login", { json: { username: user.value, password: pass.value } });
        library = null;
        if (location.hash === "#login") location.hash = "#movies"; else render();
      } catch (err) { error.textContent = err.message; }
    },
  },
    h("h1", {}, "MediaKeeper"),
    h("label", { for: "user" }, "User"), user,
    h("label", { for: "pass" }, "Password"), pass,
    h("button", { class: "primary" }, "Sign in"), error,
    me && me.guest && h("p", {}, h("a", { href: "#movies" }, "← Back to the library"))));
  user.focus();
}

// ---------------------------------------------------------------- catalog

const asShow = s => Object.assign(s, { kind: "show" });
const isShow = x => x.kind === "show";
const episodesOf = show => show.seasons.flatMap(s => s.episodes);

function progressBar(x) {
  return x.position > 0 && x.duration > 0 && h("div", { class: "bar" }, h("i", { style: `width:${Math.min(100, x.position / x.duration * 100)}%` }));
}

function grid(items, emptyText) {
  if (!items.length) {
    if (!emptyText && library && library.folders === 0) {
      return h("div", { class: "empty" }, "There is no library folder yet.",
        me.admin && h("p", {}, h("a", { href: "#settings/library" }, "Choose the folders of films and series")));
    }
    return h("div", { class: "empty" }, emptyText || "Nothing here yet.",
      me.admin && !emptyText && h("p", {}, h("a", { href: "#downloads" }, "Download something")));
  }
  return h("div", { class: "grid" }, items.map(x => {
    const watched = isShow(x) ? episodesOf(x).every(e => e.played) : x.played;
    const card = h("a", { class: "card", href: `#${isShow(x) ? "show" : "movie"}/${x.id}`, onclick: () => openedFrom(x) },
      h("div", { class: "poster", style: x.poster ? `background-image:${image(x.id, "poster")}` : "" },
        !x.poster && x.title, watched && h("span", { class: "seen", title: "Watched" }, "✓"), !isShow(x) && progressBar(x),
        x.planned && h("span", { class: "ribbon", title: "On your watchlist" }, icon("mine"))),
      h("div", { class: "title" }, x.title),
      h("div", { class: "sub" }, [!sameText(x.localTitle, x.title) && x.localTitle, x.year || null, isShow(x) && "series",
        x.myRating && "★ " + starText(x.myRating)].filter(Boolean).join(" · ")));
    card.dataset.text = `${x.title} ${x.localTitle || ""} ${x.originalTitle || ""} ${x.year || ""} ${x.collection || ""}`.toLowerCase();
    card.dataset.id = x.id;
    return card;
  }));
}

// Coming back from a title to the list it was opened from — with its "←"
// button or the browser's Back — the list is scrolled to that title again.
// A section chosen in the header starts at the top as usual.
let cameFrom = null; // {hash, id}: the list a title was opened from
let freshVisit = false; // a section was chosen in the header

function openedFrom(x) {
  cameFrom = { hash: location.hash || "#movies", id: x.id };
}

function backAtTitle() {
  const fresh = freshVisit;
  freshVisit = false;
  if (fresh || !cameFrom || cameFrom.hash !== (location.hash || "#movies")) return;
  const card = document.querySelector(`.card[data-id="${cameFrom.id}"]`);
  if (!card) return;
  card.scrollIntoView({ block: "center" });
  card.classList.add("came-from");
  setTimeout(() => card.classList.remove("came-from"), 1600);
}

// backLink leads from a title's page back to the list it was opened from,
// or to the whole section when it was opened some other way.
function backLink(x) {
  const hash = cameFrom && cameFrom.id === x.id ? cameFrom.hash : isShow(x) ? "#shows" : "#movies";
  if (!cameFrom || cameFrom.id !== x.id) cameFrom = { hash, id: x.id };
  const label = hash.startsWith("#shows") ? "Shows" : hash.startsWith("#movies") ? "Movies" : "Back";
  return h("a", { class: "button small back", href: hash }, "← " + label);
}

// renderList is the Movies or Shows page: every title, narrowed by the
// groups chosen above the grid (genre, year, actor...), which are kept in
// the address: #movies?genre=Action&year=2008.
function renderList(page, items, query) {
  const params = new URLSearchParams(query || "");
  const chosen = Object.keys(facets).filter(f => params.get(f));
  const shown = items.filter(x => chosen.every(f => (facets[f].values(x) || []).some(v => sameText(v, params.get(f)))));
  const order = params.get("sort") || "title";
  sortTitles(shown, order);

  const go = (key, value) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value); else next.delete(key);
    location.hash = "#" + page + (next.toString() ? "?" + next : "");
  };
  // Every group that has values, most common first (years newest first),
  // with how many titles have each.
  const selects = Object.entries(facets).map(([key, f]) => {
    const counts = new Map();
    for (const x of items) for (const v of new Set(f.values(x) || [])) counts.set(v, (counts.get(v) || 0) + 1);
    if (!counts.size) return null;
    const values = [...counts.keys()].sort(key === "year" ? (a, b) => b - a : (a, b) => counts.get(b) - counts.get(a) || a.localeCompare(b));
    const select = h("select", { "aria-label": f.label, class: params.get(key) ? "on" : "", onchange: () => go(key, select.value) },
      h("option", { value: "" }, f.plural),
      values.map(v => h("option", { value: v, selected: sameText(v, params.get(key)) }, `${v} (${counts.get(v)})`)));
    return select;
  });
  const sort = sortSelect(order, value => go("sort", value === "title" ? "" : value));
  shell(page,
    items.length > 0 && filterBar(selects, sort, chosen.length, "#" + page),
    grid(shown, chosen.length ? "No titles match all of these." : undefined));
}

// The orders of a list. Release dates are compared in full, so that two
// films of one year still come in the order they came out — a film series
// watched from the start.
const releaseKey = x => x.date || (x.year ? `${x.year}-99` : "");
const byTitle = (a, b) => a.title.localeCompare(b.title);
const sorters = {
  title: byTitle,
  oldest: (a, b) => (releaseKey(a) || "9999").localeCompare(releaseKey(b) || "9999") || byTitle(a, b),
  year: (a, b) => releaseKey(b).localeCompare(releaseKey(a)) || byTitle(a, b),
  added: (a, b) => (b.added || 0) - (a.added || 0),
  rating: (a, b) => (b.rating || 0) - (a.rating || 0) || byTitle(a, b),
  mine: (a, b) => (b.myRating || 0) - (a.myRating || 0) || byTitle(a, b),
};
const sortTitles = (items, order) => items.sort(sorters[order] || sorters.title);
function sortSelect(order, change) {
  const select = h("select", { "aria-label": "Order", class: "sort", onchange: () => change(select.value) },
    [["title", "By title"], ["oldest", "Oldest first"], ["year", "Newest first"], ["added", "Recently added"],
      ["rating", "Best rated"], !me.guest && ["mine", "My rating"]].filter(Boolean)
      .map(([v, label]) => h("option", { value: v, selected: v === order }, label)));
  return select;
}

// filterBar lays out the filters. On a wide screen they stand in one row;
// on a narrow one they fold behind a "Filters" button and open as a grid.
let filtersOpen = false;
function filterBar(selects, sort, active, clearHref) {
  const bar = h("div", { class: "filters" + (filtersOpen ? " open" : "") },
    h("button", {
      class: "filters-toggle", "aria-expanded": String(filtersOpen),
      onclick: () => { filtersOpen = bar.classList.toggle("open"); toggle.setAttribute("aria-expanded", String(filtersOpen)); },
    }, "Filters", active > 0 && h("span", { class: "count" }, active)),
    h("div", { class: "filter-list" }, selects),
    sort,
    active > 0 && h("a", { class: "clear", href: clearHref }, "Clear"));
  const toggle = bar.firstChild;
  return bar;
}

// The groups a title belongs to. Each value links to everything else in
// the library that shares it.
const facets = {
  genre: { label: "Genre", plural: "All genres", values: x => x.genres },
  year: { label: "Year", plural: "All years", values: x => x.year ? [String(x.year)] : [] },
  country: { label: "Country", plural: "All countries", values: x => x.countries },
  actor: { label: "Actor", plural: "All actors", values: x => (x.cast || []).map(p => p.name) },
  director: { label: "Director", plural: "All directors", values: x => x.directors },
  writer: { label: "Writer", plural: "All writers", values: x => x.writers },
  studio: { label: "Studio", plural: "All studios", values: x => x.studios },
  collection: { label: "Collection", plural: "All collections", values: x => x.collection ? [x.collection] : [] },
  mine: { label: "Mine", plural: "Mine: everything", values: x => mineTags(x) },
};

// mineTags are the signed-in user's own groups of a title.
function mineTags(x) {
  if (!me || me.guest) return [];
  const episodes = isShow(x) ? episodesOf(x) : null;
  const played = episodes ? episodes.length > 0 && episodes.every(e => e.played) : x.played;
  const started = episodes ? !played && episodes.some(e => e.played || e.position > 0) : !x.played && x.position > 0;
  return [x.planned && "On my watchlist", played ? "Watched" : "Not watched", started && "In progress",
    x.myRating && "Rated by me", x.favorite && "My favorites"].filter(Boolean);
}
const browseLink = (facet, value) => `#browse/${facet}/${encodeURIComponent(value)}`;
const chip = (facet, value, note) => h("a", { class: "chip", href: browseLink(facet, value) }, value, note && h("span", { class: "note" }, note));

function renderBrowse(facet, value, query) {
  const f = facets[facet];
  if (!f) return (location.hash = "#movies");
  const all = [...library.movies, ...library.shows.map(asShow)];
  const found = all.filter(x => (f.values(x) || []).some(v => sameText(v, value)));
  // A film series reads best in the order it came out; anything else by title.
  const order = new URLSearchParams(query || "").get("sort") || (facet === "collection" ? "oldest" : "title");
  sortTitles(found, order);
  const here = location.hash.split("?")[0];
  shell("browse",
    h("p", { class: "crumbs" }, h("a", { href: "#movies" }, "Library"), " › ", f.label),
    h("h1", {}, value),
    h("div", { class: "browse-bar" },
      h("p", { class: "dim" }, `${found.length} title(s) in the library`),
      found.length > 1 && sortSelect(order, v => { location.hash = here + "?sort=" + v; })),
    grid(found, "Nothing in the library matches any more."));
}

// section("Cast", content) is one titled group of the details; empty groups are left out.
function section(label, ...content) {
  content = content.flat().filter(Boolean);
  return content.length > 0 && h("section", { class: "info-group" }, h("h2", {}, label), h("div", { class: "body" }, content));
}
const chips = (facet, values) => (values || []).length > 0 && h("div", { class: "chips" }, values.map(v => chip(facet, v)));

function details(x, extraFacts) {
  const facts = [
    x.originalTitle && !sameText(x.originalTitle, x.title) && ["Original title", x.originalTitle],
    x.localTitle && !sameText(x.localTitle, x.title) && ["Russian title", x.localTitle],
    x.date && [isShow(x) ? "First aired" : "Released", x.date],
    x.status && ["Status", x.status],
    x.mpaa && ["Age rating", x.mpaa],
    x.imdb && ["IMDb", h("a", { href: `https://www.imdb.com/title/${x.imdb}/`, target: "_blank", rel: "noopener" }, x.imdb)],
    ...(extraFacts || []),
  ].filter(Boolean);
  return [
    section("Plot", x.plot && h("p", { class: "plot" }, x.plot)),
    section("Collection", x.collection && chips("collection", [x.collection])),
    section("Genres", chips("genre", x.genres)),
    section("Directed by", chips("director", x.directors)),
    section("Written by", chips("writer", x.writers)),
    section("Cast", (x.cast || []).length > 0 && h("div", { class: "chips" }, x.cast.map(p => chip("actor", p.name, p.role)))),
    section("Studio", chips("studio", x.studios)),
    section("Country", chips("country", x.countries)),
    section("Details", facts.length > 0 && h("dl", { class: "facts" }, facts.map(([k, v]) => [h("dt", {}, k), h("dd", {}, v)]))),
  ];
}

function hero(x, ...rest) {
  const meta = [
    x.year && h("a", { href: browseLink("year", x.year) }, x.year),
    !isShow(x) && minutes(x.duration),
    isShow(x) && `${x.seasons.length} season(s), ${episodesOf(x).length} episode(s)`,
    x.rating > 0 && "★ " + x.rating,
    x.mpaa,
  ].filter(Boolean);
  pendingAmbient = x.backdrop ? image(x.id, "backdrop") : x.poster ? image(x.id, "poster") : null;
  return h("div", { class: x.backdrop ? "hero" : "hero plain", style: x.backdrop ? `--backdrop:${image(x.id, "backdrop")}` : "" },
    backLink(x),
    h("div", { class: "inner" },
      x.poster && h("img", { class: "poster", src: `/api/image/${x.id}/poster`, alt: "" }),
      h("div", { class: "info glass" },
        fixButton(x),
        h("h1", {}, fullTitle(x)),
        x.tagline && h("p", { class: "tagline" }, x.tagline),
        h("div", { class: "meta" }, meta.map((m, i) => [i > 0 && " · ", m])),
        ...rest,
        mineRow(x))));
}

function playLabel(x) {
  return x.position > 0 ? `Resume from ${time(x.position)}` : "Play";
}

async function toggleWatched(item) {
  await api("progress/" + item.id, { json: { played: !item.played } });
  render();
}

// A quiet button in the corner of the title: rarely needed, only for administrators.
const fixButton = x => me.admin && h("button", { class: "edit", title: "Edit the description, the artwork, or what this really is", onclick: () => openEdit(x) }, "✎ Edit");

function renderMovie(id) {
  const m = library.movies.find(x => x.id === id);
  if (!m) return shell("movies", h("div", { class: "empty" }, "This movie is not in the library any more."));
  const file = h("dd", {}, "…");
  shell("movies",
    hero(m, h("div", { class: "actions" },
      h("button", { class: "primary", onclick: () => play(m) }, icon("play"), playLabel(m)),
      m.position > 0 && h("button", { onclick: () => play(m, 0) }, "From the beginning"),
      !me.guest && h("button", { onclick: () => toggleWatched(m) }, m.played ? "Mark as not watched" : "Mark as watched"),
      h("a", { class: "button", href: `/api/stream/${m.id}`, download: "" }, "Download the file"))),
    screenshots(m.id),
    h("div", { class: "details glass" }, details(m, [["File", file]])));
  // What is inside the file comes with a separate request.
  api("item/" + id).then(info => {
    const audio = (info.audio || []).map(a => [a.language, a.codec, a.channels && a.channels + "ch"].filter(Boolean).join(" ")).join(", ");
    file.replaceChildren([info.file, bytes(info.size), info.video, audio && "audio: " + audio].filter(Boolean).join(" · "));
  }).catch(() => file.replaceChildren("—"));
}

let openSeason = {}; // show id -> the season tab that is open

// episodeThumb is the picture of an episode in the list: its still (or a
// frame the server took from it); until there is one, the series backdrop,
// which is as wide; else the series poster whole, not cropped to a strip.
function episodeThumb(e, show, ...children) {
  const [picture, fit] =
    e.thumb ? [`url("/api/image/${e.id}/thumb?v=${e.thumb}")`, ""] :
    show.backdrop ? [image(show.id, "backdrop"), ""] :
    show.poster ? [image(show.id, "poster"), " whole"] : ["", ""];
  return h("div", { class: "thumb" + fit, style: picture && `background-image:${picture}` }, children);
}

// scrollingTabs keeps a row of tabs that is wider than the screen usable:
// the open tab is scrolled into view, and a side with more tabs behind it
// fades out.
function scrollingTabs(row) {
  const update = () => {
    row.classList.toggle("more-left", row.scrollLeft > 1);
    row.classList.toggle("more-right", row.scrollLeft + row.clientWidth < row.scrollWidth - 1);
  };
  requestAnimationFrame(() => {
    const active = row.querySelector(".active");
    if (active && row.scrollWidth > row.clientWidth) // not scrollIntoView: that would scroll the page too
      row.scrollLeft = active.offsetLeft - (row.clientWidth - active.offsetWidth) / 2;
    update();
  });
  row.addEventListener("scroll", update, { passive: true });
  // A mouse wheel scrolls the row sideways, until it reaches an end.
  row.addEventListener("wheel", e => {
    if (Math.abs(e.deltaX) > Math.abs(e.deltaY)) return; // a trackpad already scrolls sideways
    const room = e.deltaY > 0 ? row.scrollWidth - row.clientWidth - row.scrollLeft : row.scrollLeft;
    if (room <= 0) return;
    e.preventDefault();
    row.scrollLeft += e.deltaY;
  }, { passive: false });
  new ResizeObserver(update).observe(row);
  return row;
}

function renderShow(id) {
  const show = library.shows.map(asShow).find(x => x.id === id);
  if (!show) return shell("shows", h("div", { class: "empty" }, "This show is not in the library any more."));
  const episodes = episodesOf(show);
  // Continue where the viewer is: an episode in progress, else the first not watched.
  const next = episodes.find(e => e.position > 0) || episodes.find(e => !e.played) || episodes[0];
  const current = show.seasons.find(s => s.number === openSeason[id]) || show.seasons.find(s => s.episodes.includes(next)) || show.seasons[0];
  const label = e => `S${e.season}E${e.episode}${e.episodeEnd > e.episode ? "-" + e.episodeEnd : ""}`;
  shell("shows",
    hero(show, h("div", { class: "actions" },
      next && h("button", { class: "primary", onclick: () => play(next, undefined, episodes) }, icon("play"), `${playLabel(next)} · ${label(next)}`),
    )),
    scrollingTabs(h("div", { class: "tabs" }, show.seasons.map(s => h("button", {
      class: s === current ? "active" : "",
      onclick: () => { openSeason[id] = s.number; renderShow(id); },
    }, s.number ? `Season ${s.number}` : "Specials")))),
    current.episodes.map(e => h("div", { class: "episode", onclick: () => play(e, undefined, episodes) },
      episodeThumb(e, show,
        e.played && h("span", { class: "seen", title: "Watched" }, "✓"), progressBar(e)),
      h("div", { class: "body" },
        h("div", { class: "name" }, `${e.episode}${e.episodeEnd > e.episode ? "–" + e.episodeEnd : ""}. ${e.title}`),
        h("div", { class: "dim" }, [e.date, minutes(e.duration)].filter(Boolean).join(" · ")),
        h("div", { class: "plot" }, e.plot)),
      !me.guest && h("button", { class: "small", onclick: ev => { ev.stopPropagation(); toggleWatched(e); } }, e.played ? "Unwatch" : "Watched"))),
    h("div", { class: "details glass" }, details(show)));
}

// ------------------------------------------------------------------ mine

// A rating is kept from 1 to 10 and shown as five stars with halves.
const starText = r => (r / 2).toString().replace(".5", "½");

// stars is the user's rating of a title: hover to see, click to set. Most
// of a star gives the whole star; only its left edge gives a half, so that
// a click in the middle of a star is not taken for half a point less.
function stars(value, onChange) {
  const box = h("div", { class: "stars", role: "slider", tabIndex: 0, "aria-label": "Your rating", "aria-valuemin": 0, "aria-valuemax": 10 });
  const draw = v => {
    box.setAttribute("aria-valuenow", v);
    box.replaceChildren(...[1, 2, 3, 4, 5].map(i => h("span", { class: "star " + ["", "half", "full"][Math.max(0, Math.min(2, v - (i - 1) * 2))] }, "★")));
  };
  const at = e => { // the rating under the pointer
    const r = box.getBoundingClientRect();
    const x = Math.max(0, Math.min(4.999, (e.clientX - r.left) / r.width * 5)); // 0..5 across the stars
    const star = Math.floor(x) + 1, within = x - Math.floor(x);
    return within < 0.4 ? star * 2 - 1 : star * 2;
  };
  box.addEventListener("mousemove", e => draw(at(e)));
  box.addEventListener("mouseleave", () => draw(value));
  box.addEventListener("click", e => {
    const v = at(e);
    if (v !== value) onChange(v);
  });
  box.addEventListener("keydown", e => {
    if (e.key === "ArrowRight" || e.key === "ArrowUp") { e.preventDefault(); onChange(Math.min(10, value + 1)); }
    if (e.key === "ArrowLeft" || e.key === "ArrowDown") { e.preventDefault(); onChange(Math.max(0, value - 1)); }
  });
  draw(value);
  return box;
}

// mineRow is the signed-in user's own marks of a title on its page: the
// rating, the watchlist, favourite and a note.
function mineRow(x) {
  if (!me || me.guest) return null;
  const row = h("div", { class: "mine" });
  let editing = false, sent = 0;
  // A change shows at once; the server's answer only counts if it is the
  // answer to the latest change (quick clicks may be answered out of order).
  const set = async (body, shown) => {
    const before = { myRating: x.myRating, planned: x.planned, favorite: x.favorite, note: x.note };
    const mine = ++sent;
    if (shown) { Object.assign(x, shown); draw(); }
    try {
      const r = await api("mine/" + x.id, { json: body });
      if (mine !== sent) return;
      Object.assign(x, { myRating: r.myRating || 0, planned: r.planned || 0, favorite: !!r.favorite, note: r.note || "" });
      editing = false;
      draw();
    } catch (err) {
      if (mine === sent) { Object.assign(x, before); draw(); }
      alert(err.message);
    }
  };
  function draw() {
    const note = h("textarea", { rows: 3, value: x.note || "", placeholder: "Only you see this note", "aria-label": "Your note" });
    fill(row,
      h("div", { class: "mine-bar" },
        h("span", { class: "dim" }, "Your rating"),
        stars(x.myRating || 0, v => set({ rating: v }, { myRating: v })),
        x.myRating ? [h("span", { class: "dim" }, starText(x.myRating)),
          h("button", { class: "clear-rating", title: "Remove your rating", "aria-label": "Remove your rating", onclick: () => set({ rating: 0 }, { myRating: 0 }) }, "×")] : null,
        h("span", { class: "grow" }),
        h("button", { class: "small" + (x.planned ? " on" : ""), onclick: () => set({ planned: !x.planned }, { planned: x.planned ? 0 : Math.floor(Date.now() / 1000) }) },
          icon("mine"), x.planned ? "On your watchlist" : "Watch later"),
        h("button", { class: "small" + (x.favorite ? " on" : ""), onclick: () => set({ favorite: !x.favorite }, { favorite: !x.favorite }) }, x.favorite ? "♥ Favorite" : "♡ Favorite"),
        !editing && h("button", { class: "small", onclick: () => { editing = true; draw(); note.focus(); } }, x.note ? "✎ Note" : "✎ Add a note")),
      editing ? h("div", { class: "note-edit" }, note,
        h("div", { class: "row" },
          h("button", { class: "small primary", onclick: () => set({ note: note.value }) }, "Save"),
          h("button", { class: "small", onclick: () => { editing = false; draw(); } }, "Cancel")))
        : x.note && h("p", { class: "my-note" }, x.note));
  }
  draw();
  return row;
}

// renderMine is the user's own page: what they are watching, what they
// want to watch, what they watched, rated and like.
async function renderMine(tab) {
  const titles = [...library.movies, ...library.shows.map(asShow)];
  const showLast = s => Math.max(0, ...episodesOf(s).map(e => e.lastPlayed || 0));
  const tabs = [["continue", "Continue"], ["watchlist", "Watchlist"], ["history", "History"], ["rated", "Rated"], ["favorites", "Favorites"]];
  const bar = h("div", { class: "tabs" }, tabs.map(([id, label]) => h("button", { class: id === tab ? "active" : "", onclick: () => { location.hash = "#mine/" + id; } }, label)));
  let content;
  switch (tab) {
    case "watchlist":
      content = grid(titles.filter(x => x.planned).sort((a, b) => b.planned - a.planned),
        "Nothing on your watchlist. “Watch later” on a title's page puts it here.");
      break;
    case "rated":
      content = grid(titles.filter(x => x.myRating).sort((a, b) => b.myRating - a.myRating || a.title.localeCompare(b.title)),
        "You have not rated anything yet: the stars on a title's page.");
      break;
    case "favorites":
      content = grid(titles.filter(x => x.favorite).sort((a, b) => a.title.localeCompare(b.title)), "No favorites yet.");
      break;
    case "history":
      content = historyList();
      break;
    default: {
      const going = titles.filter(x => mineTags(x).includes("In progress"))
        .sort((a, b) => (isShow(b) ? showLast(b) : b.lastPlayed || 0) - (isShow(a) ? showLast(a) : a.lastPlayed || 0));
      content = grid(going, "Nothing started. What you begin to watch shows up here.");
    }
  }
  shell("mine", h("h1", { class: "page-title" }, "My"), scrollingTabs(bar), content);
}

// historyList is the history of viewings, by day, newest first.
function historyList() {
  const box = h("div", { class: "history" });
  const list = h("div", {});
  const more = h("button", { class: "hidden", onclick: () => load() }, "Show earlier");
  let next = "", lastDay = "";
  const day = t => {
    const d = new Date(t * 1000), today = new Date();
    const yesterday = new Date(today); yesterday.setDate(today.getDate() - 1);
    if (d.toDateString() === today.toDateString()) return "Today";
    if (d.toDateString() === yesterday.toDateString()) return "Yesterday";
    return d.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long", year: d.getFullYear() === today.getFullYear() ? undefined : "numeric" });
  };
  const clock = t => new Date(t * 1000).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  function entry(e) {
    const name = e.kind === "episode" ? e.showTitle : e.title;
    const sub = e.kind === "episode" ? `S${e.season}E${e.episode} · ${e.title}` : e.year || "";
    const state = e.finished ? "watched to the end" : e.duration ? `stopped at ${Math.round(e.position / e.duration * 100)}%` : "";
    const row = h("div", { class: "history-entry" },
      h(e.link ? "a" : "div", { class: "pic" + (e.wide ? " wide" : ""), href: e.link, style: e.image ? `background-image:url("${e.image}")` : "" }),
      h("div", { class: "grow" },
        e.link ? h("a", { class: "name", href: e.link }, name) : h("span", { class: "name" }, name, h("span", { class: "dim" }, " · no longer in the library")),
        sub && h("div", { class: "dim" }, sub),
        h("div", { class: "dim" }, [`${clock(e.started)}–${clock(e.ended)}`, minutes(e.watched) || "under a minute", state].filter(Boolean).join(" · "))),
      h("button", { class: "small", title: "Remove from the history", "aria-label": "Remove from the history", onclick: async () => {
        try { await api("history/" + e.id, { method: "DELETE" }); row.remove(); } catch (err) { alert(err.message); }
      } }, "×"));
    return row;
  }
  async function load() {
    let res;
    try { res = await api("history" + (next ? "?after=" + next : "")); } catch (err) { return list.append(h("p", { class: "error" }, err.message)); }
    if (res.stats) {
      const st = res.stats;
      box.prepend(h("div", { class: "stats" },
        [["This month", minutes(st.since) || "—"], ["All time", minutes(st.total) || "—"], ["Titles", st.titles], ["Watched to the end", st.finished]]
          .map(([k, v]) => h("div", { class: "stat glass" }, h("b", {}, v), h("span", { class: "dim" }, k))),
        st.total > 0 && h("button", { class: "small danger", onclick: async () => {
          if (!confirm("Forget your whole history? Ratings, the watchlist and watched marks stay.")) return;
          await api("history", { method: "DELETE" }); render();
        } }, "Clear history")));
      if (!res.entries.length) list.append(h("div", { class: "empty" }, "Nothing watched yet."));
    }
    for (const e of res.entries) {
      const d = day(e.started);
      if (d !== lastDay) list.append(h("h2", { class: "day" }, lastDay = d));
      list.append(entry(e));
    }
    next = res.next || "";
    more.classList.toggle("hidden", !next);
  }
  load();
  box.append(list, more);
  return box;
}

// --------------------------------------------------- choosing what it is

// identifyForm lets an administrator say what a movie or a series is:
// candidates from all sources, a search by another title, or an ID. It is
// used for downloads the program could not identify and for titles of the
// library it identified wrongly.
//   unit:    {kind, title, year, files}
//   search:  query -> Promise of candidates
//   resolve: request body -> Promise
//   st:      the form's state, kept by the caller across redraws
//   fill:    the choice fills in a form instead of filing the files: a
//            series is then simply taken as a movie, nothing is asked, and
//            a click on a candidate is the choice, without a confirmation
//   ask:     with fill, the button for an ID says "Set by ID" (a choice
//            made in advance, not a form)
// resolve gets the request body and the candidate picked (none for an ID).
function identifyForm({ unit, search, resolve, st, extra, fill, ask, auto = true }) {
  Object.assign(st, { query: "", picked: null, candidates: null, asMovie: true, season: 1, episode: 1, ref: "", ...st });
  const holder = h("div", {});
  const error = h("p", { class: "error" });
  const pickedSeries = () => !fill && st.picked && st.picked.kind === "tv" && unit.kind === "movie";
  const working = h("p", { class: "dim hidden" }, "Loading the chosen title…");
  const submit = async (body, candidate) => {
    error.textContent = st.failed = "";
    st.submitting = true;
    holder.classList.add("busy");
    working.classList.remove("hidden");
    try { await resolve(body, candidate); } catch (err) { error.textContent = st.failed = err.message; }
    st.submitting = false;
    holder.classList.remove("busy");
    working.classList.add("hidden");
  };

  // The search belongs to the form's state, not to this drawing of it: the
  // page is drawn anew every few seconds, and a search slower than that
  // (a source timing out) must neither start again nor be lost.
  async function find() {
    holder.replaceChildren(h("p", { class: "dim" }, "Searching…"));
    const token = st.token = (st.token || 0) + 1; // only the latest search counts
    const mine = st.searching = (async () => {
      let found = [], failed = "";
      try { found = await search(st.query); } catch (err) { failed = err.message; }
      if (token === st.token) { st.candidates = found; st.error = failed; }
    })();
    await mine;
    if (st.searching === mine) st.searching = null;
    error.textContent = st.error;
    show();
  }
  function show() {
    const groups = [];
    let last = "";
    for (const c of st.candidates || []) {
      if (c.sourceName !== last) groups.push(h("div", { class: "group" }, last = c.sourceName));
      groups.push(h("button", {
        type: "button", class: st.picked === c ? "picked" : "",
        onclick: () => {
          st.picked = c;
          show();
          if (fill) submit({ source: c.source, id: c.id, kind: c.kind }, c);
        },
      }, c.title, c.year ? ` (${c.year})` : "", c.originalTitle && c.originalTitle !== c.title ? ` — ${c.originalTitle}` : "",
        c.kind === "tv" && h("span", { class: "tag" }, "series")));
    }
    const season = h("input", { type: "text", size: 3, value: st.season, "aria-label": "Season", oninput: () => st.season = +season.value });
    const episode = h("input", { type: "text", size: 3, value: st.episode, "aria-label": "Episode", oninput: () => st.episode = +episode.value });
    const asMovie = h("select", { "aria-label": "What the file is", onchange: () => { st.asMovie = asMovie.value === "movie"; show(); } },
      h("option", { value: "movie", selected: st.asMovie }, "The file is the whole series — keep it as a movie"),
      h("option", { value: "episode", selected: !st.asMovie }, "The file is one episode"));
    holder.replaceChildren(...[
      h("div", { class: "candidates" }, groups.length ? groups : h("p", { class: "dim" }, "Nothing found. Try another title or paste an ID.")),
      pickedSeries() && h("div", { class: "row" }, asMovie, !st.asMovie && ["Season", season, "Episode", episode]),
      !fill && h("div", { class: "row", style: "margin-top:10px" },
        h("button", {
          type: "button", class: "primary", disabled: !st.picked,
          onclick: () => submit({ source: st.picked.source, id: st.picked.id, kind: st.picked.kind,
            asMovie: pickedSeries() && st.asMovie, season: st.season, episode: pickedSeries() && !st.asMovie ? st.episode : 0 }),
        }, "This is it")),
    ].filter(Boolean));
  }

  const query = h("input", { type: "text", class: "grow", placeholder: "Another title; a year helps: Форсаж 2026", value: st.query, "aria-label": "Title",
    oninput: () => st.query = query.value, onkeydown: e => { if (e.key === "Enter") { e.preventDefault(); find(); } } });
  const ref = h("input", { type: "text", class: "grow", placeholder: "tt0371746, tmdb:1726, kp:61237 or a link to IMDb, TMDB, Kinopoisk, Letterboxd, TVMaze", value: st.ref,
    "aria-label": "ID or link", oninput: () => st.ref = ref.value });
  // Many titles waiting at once are not all searched at once: the first few
  // are, the others when asked.
  if (st.failed) error.textContent = st.failed; // the page was drawn anew since
  if (st.submitting) { holder.classList.add("busy"); working.classList.remove("hidden"); }
  if (st.searching) {
    holder.replaceChildren(h("p", { class: "dim" }, "Searching…"));
    st.searching.then(() => { error.textContent = st.error || ""; show(); });
  } else if (st.candidates) show();
  else if (auto) find();
  else holder.replaceChildren(h("p", { class: "dim" }, "Search to see what it could be."));
  const files = unit.files || [];
  return h("div", { class: "unit" },
    h("div", {}, h("b", {}, { tv: "Series: ", movie: "Movie: " }[unit.kind] || ""), unit.title || "?", unit.year ? ` (${unit.year})` : ""),
    files.length > 0 && h("div", { class: "files", title: files.join("\n") },
      files.length > 3 ? `${files.length} files: ${files.slice(0, 2).join(", ")}, …` : files.join(", ")),
    h("div", { class: "row", style: "margin-top:10px" }, query, h("button", { type: "button", onclick: find }, "Search")),
    holder,
    h("div", { class: "row", style: "margin-top:10px" }, ref,
      h("button", { type: "button", onclick: () => st.ref.trim() && submit({ ref: st.ref, asMovie: unit.kind === "movie" }) }, fill && !ask ? "Fill in by ID" : "Set by ID"),
      extra),
    working, error);
}

// screenshots is the strip of frames from the film. The server takes them
// in the background; while it works the strip shows placeholders and asks
// again every few seconds.
function screenshots(id) {
  const strip = h("div", { class: "shots" });
  const box = h("section", { class: "screens hidden" }, h("h2", {}, "Screenshots"), strip);
  let tries = 0;
  async function load() {
    if (!box.isConnected && tries > 0) return; // the page has changed
    let res;
    try { res = await api(`screens/${id}`); } catch { return; }
    if (res.state === "unavailable" || (res.state === "failed" && !res.shots.length)) return;
    box.classList.remove("hidden");
    if (res.state === "pending") {
      strip.replaceChildren(...Array.from({ length: 8 }, () => h("div", { class: "shot placeholder" })),
        h("p", { class: "dim note" }, "Taking screenshots…"));
      if (tries++ < 60) setTimeout(load, 3000);
      return;
    }
    strip.replaceChildren(...res.shots.map((url, i) => h("button", {
      class: "shot", style: `background-image:url("${url}")`, "aria-label": `Screenshot ${i + 1}`,
      onclick: () => viewShots(res.shots, i),
    })));
  }
  load();
  return box;
}

// viewShots shows the screenshots one by one over the page.
function viewShots(urls, index) {
  const img = h("img", { alt: "" });
  const counter = h("span", { class: "dim" });
  const show = i => { index = (i + urls.length) % urls.length; img.src = urls[index]; counter.textContent = `${index + 1} / ${urls.length}`; };
  const close = () => { box.remove(); document.removeEventListener("keydown", onKey); };
  const onKey = e => {
    if (e.key === "Escape") close();
    else if (e.key === "ArrowRight") show(index + 1);
    else if (e.key === "ArrowLeft") show(index - 1);
  };
  const box = h("div", { class: "modal lightbox", onclick: e => { if (e.target === box) close(); } },
    h("div", { class: "frame" },
      img,
      h("div", { class: "bar glass" },
        h("button", { class: "small", onclick: () => show(index - 1), "aria-label": "Previous" }, "‹"),
        counter,
        h("button", { class: "small", onclick: () => show(index + 1), "aria-label": "Next" }, "›"),
        h("button", { class: "small", onclick: close }, "Close"))));
  // A swipe on a phone or a tablet.
  let startX = null;
  img.addEventListener("touchstart", e => { startX = e.touches[0].clientX; }, { passive: true });
  img.addEventListener("touchend", e => {
    const dx = e.changedTouches[0].clientX - startX;
    if (Math.abs(dx) > 40) show(index + (dx < 0 ? 1 : -1));
  });
  document.addEventListener("keydown", onKey);
  document.body.append(box);
  show(index);
}

// toast shows a short message at the bottom of the screen.
function toast(text) {
  const el = h("div", { class: "toast glass" }, text);
  document.body.append(el);
  setTimeout(() => el.classList.add("gone"), 3500);
  setTimeout(() => el.remove(), 4200);
}

const listText = values => (values || []).join(", ");
const textList = text => text.split(",").map(v => v.trim()).filter(Boolean);
const castText = cast => (cast || []).map(p => p.role ? `${p.name} — ${p.role}` : p.name).join("\n");
const textCast = text => text.split("\n").map(line => line.trim()).filter(Boolean).map(line => {
  const [name, ...role] = line.split(/\s+[—–-]\s+/);
  return { name: name.trim(), role: role.join(" — ").trim() };
});

// openEdit is the administrator's sheet for a title: its description, which
// can be filled in from a catalogue, its artwork, and the screenshots of a
// movie or the episodes of a series.
//   opts.find: open with the search of the catalogues showing
//   opts.stay: stay on the page it was opened from after saving
function openEdit(x, opts = {}) {
  const movie = !isShow(x);
  let dirty = false; // something was saved: the page is drawn anew on closing
  const saved = () => { dirty = true; library = null; };
  const close = () => {
    box.remove();
    document.removeEventListener("keydown", onKey);
    if (dirty) render();
  };
  const onKey = e => { if (e.key === "Escape" && !e.target.closest("input, textarea, select")) close(); };
  const tabs = [["details", "Description"], ["images", "Images"], !movie && ["episodes", "Episodes"]].filter(Boolean);
  let current = tabs[0][0];
  const body = h("div", {});
  const tabBar = h("div", { class: "tabs" });
  const choose = id => {
    current = id;
    tabBar.replaceChildren(...tabs.map(([tab, label]) => h("button", { class: tab === current ? "active" : "", onclick: () => choose(tab) }, label)));
    body.replaceChildren({ details: detailsTab, images: imagesTab, episodes: episodesTab }[id]());
  };
  const text = (value, attrs) => h("input", { type: "text", value: value || "", ...attrs });
  // upload sends a picture chosen in a file field, then shows it in place.
  const upload = async (input, part, preview, src) => {
    if (!input.files.length) return;
    const form = new FormData();
    form.append("image", input.files[0]);
    try {
      const res = await api(`meta/${part}`, { method: "POST", body: form });
      preview.style.backgroundImage = `url("${src}?t=${Date.now()}")`;
      saved();
      toast(res.tags ? "Saved. The poster is being embedded in the file." : "Saved.");
    } catch (err) { alert(err.message); }
    input.value = "";
  };

  function detailsTab() {
    const holder = h("div", {}, h("p", { class: "dim" }, "Loading…"));
    api(`meta/${x.id}`).then(m => {
      const f = {
        title: text(m.title), originalTitle: text(m.originalTitle), localTitle: text(m.localTitle),
        year: text(m.year || "", { inputMode: "numeric" }), released: text(m.released, { placeholder: "2008-04-30" }),
        status: text(m.status, { placeholder: "Continuing, Ended" }),
        mpaa: text(m.mpaa, { placeholder: "PG-13" }), rating: text(m.rating || "", { inputMode: "decimal", placeholder: "0–10" }),
        tagline: text(m.tagline), plot: h("textarea", { rows: 5, value: m.plot || "" }),
        collection: text(m.collection, { placeholder: "Pirates of the Caribbean Collection" }),
        genres: text(listText(m.genres)), directors: text(listText(m.directors)), writers: text(listText(m.writers)),
        studios: text(listText(m.studios)), countries: text(listText(m.countries)),
        cast: h("textarea", { rows: 6, value: castText(m.cast), placeholder: "Robert Downey Jr. — Tony Stark" }),
      };
      // Set when the fields are filled in from a catalogue.
      let source = null, photos = {};
      const takeArt = h("input", { type: "checkbox", id: "take-art", checked: true });
      const artRow = h("label", { class: "check hidden", for: "take-art" }, takeArt, h("span", {}));
      // A movie can be renamed after its fields at any time; a series only
      // after a catalogue entry, which also names its episodes.
      const rename = h("input", { type: "checkbox", id: "rename" });
      const renameRow = h("label", { class: movie ? "check" : "check hidden", for: "rename" }, rename,
        h("span", {}, "Rename the files after the title and year"));
      const filledNote = h("p", { class: "filled hidden" });

      const fillIn = (r, picked) => {
        const m = r.meta;
        f.title.value = m.title || ""; f.originalTitle.value = m.originalTitle || ""; f.localTitle.value = m.localTitle || "";
        f.year.value = m.year || ""; f.released.value = m.released || ""; f.status.value = m.status || "";
        f.mpaa.value = m.mpaa || ""; f.rating.value = m.rating || "";
        f.tagline.value = m.tagline || ""; f.plot.value = m.plot || ""; f.collection.value = m.collection || "";
        f.genres.value = listText(m.genres); f.directors.value = listText(m.directors); f.writers.value = listText(m.writers);
        f.studios.value = listText(m.studios); f.countries.value = listText(m.countries); f.cast.value = castText(m.cast);
        photos = Object.fromEntries((m.cast || []).filter(p => p.thumb).map(p => [p.name, p.thumb]));
        source = r;
        const art = [r.posterUrl && "poster", r.backdropUrl && "backdrop"].filter(Boolean);
        artRow.classList.toggle("hidden", !art.length);
        artRow.lastChild.textContent = `Also take the ${art.join(" and ")} from ${r.sourceName}`;
        if (!movie) renameRow.lastChild.textContent = `Rename the files, and take the titles, descriptions and stills of the episodes from ${r.sourceName}`;
        renameRow.classList.remove("hidden");
        rename.checked = true;
        // IMDb, Wikidata only find titles: the details come from another catalogue.
        filledNote.textContent = (picked && picked.sourceName !== r.sourceName
          ? `Found on ${picked.sourceName}, filled in from ${r.sourceName}.` : `Filled in from ${r.sourceName}.`) + " Check the fields, then save.";
        filledNote.classList.remove("hidden");
        finder.classList.add("hidden");
        f.title.focus();
      };
      const finder = h("div", { class: opts.find ? "finder" : "finder hidden" },
        identifyForm({
          unit: { kind: movie ? "movie" : "tv", title: m.title, year: m.year }, st: {}, fill: true,
          search: q => api(`fix/${x.id}/search?q=${encodeURIComponent(q)}`),
          resolve: async (body, picked) => fillIn(await api(`meta/${x.id}/lookup`, { json: body }), picked),
        }));
      const error = h("p", { class: "error" });
      const save = h("button", { class: "primary", type: "submit" }, "Save");
      holder.replaceChildren(
        h("div", { class: "row" },
          h("button", { type: "button", onclick: () => finder.classList.toggle("hidden") }, "Fill in from a catalogue…"),
          h("span", { class: "dim" }, "Search all sources and pick the right one: the fields are filled in for you to check.")),
        m.noFolder ? h("p", { class: "filled warn" }, "The episodes are not in a folder of their own yet. Fill the fields in from a catalogue and let the files be renamed: that files them into one.") : "",
        finder, filledNote,
        h("form", {
          class: "meta-form",
          onsubmit: async e => {
            e.preventDefault();
            error.textContent = "";
            save.disabled = true;
            const cast = textCast(f.cast.value).map(p => ({ ...p, thumb: photos[p.name] || "" }));
            try {
              const res = await api(`meta/${x.id}`, { json: {
                title: f.title.value, originalTitle: f.originalTitle.value, localTitle: f.localTitle.value,
                year: +f.year.value || 0, released: f.released.value.trim(), mpaa: f.mpaa.value, rating: +String(f.rating.value).replace(",", ".") || 0,
                plot: f.plot.value, genres: textList(f.genres.value), studios: textList(f.studios.value), cast,
                ...(movie ? {
                  tagline: f.tagline.value, directors: textList(f.directors.value), writers: textList(f.writers.value),
                  countries: textList(f.countries.value), collection: f.collection.value.trim(),
                } : { status: f.status.value }),
                rename: rename.checked && (movie || !!source),
                ...(source ? {
                  source: source.source, sourceId: source.sourceId, sourceKind: source.sourceKind, ids: source.ids,
                  posterUrl: takeArt.checked ? source.posterUrl : "", backdropUrl: takeArt.checked ? source.backdropUrl : "",
                } : {}),
              } });
              saved();
              // A renamed title has a new address: a movie goes back to the
              // list, a series to its new page.
              if (opts.stay);
              else if (movie && rename.checked) location.hash = "#movies";
              else if (res.id && res.id !== x.id) location.hash = "#show/" + res.id;
              close();
              if (res.problems && res.problems.length) alert("Saved, but: " + res.problems.join("; "));
              else toast(res.tags ? "Saved. The tags inside the files are being updated." : "Saved.");
            } catch (err) { error.textContent = err.message; save.disabled = false; }
          },
        },
          h("div", { class: "fields" },
            field("Title", f.title), field("Original title", f.originalTitle), field("Russian title", f.localTitle),
            field("Year", f.year), field(movie ? "Released" : "First aired", f.released), !movie && field("Status", f.status),
            field("Age rating", f.mpaa), field("Rating", f.rating)),
          movie && field("Tagline", f.tagline), field("Plot", f.plot),
          movie && field("Collection — the film series, to find and watch its parts in order", f.collection),
          h("div", { class: "fields" },
            field("Genres", f.genres), movie && field("Directed by", f.directors), movie && field("Written by", f.writers),
            field("Studios", f.studios), movie && field("Countries", f.countries)),
          h("p", { class: "dim hint" }, "Lists are separated by commas. Cast: one actor per line, the role after a dash."),
          field("Cast", f.cast),
          artRow, renameRow,
          h("div", { class: "row" }, save, h("span", { class: "dim" },
            movie ? "Written into the .nfo and the tags of the file." : "Written into tvshow.nfo, and the title and genres into the tags of the episodes.")),
          error));
    }).catch(err => holder.replaceChildren(h("p", { class: "error" }, err.message)));
    return holder;
  }

  let artNote = "From the catalogue entry the description comes from" + (movie ? "." : "; episode stills are only added where there are none.");
  function imagesTab() {
    // card is one picture: what it is now, and a field to replace it.
    // The browser's own file field does not fit a narrow card: a button
    // (or a click on the picture) opens it instead.
    const card = (part, cls, label, note, src, has) => {
      const input = h("input", { type: "file", accept: "image/jpeg,image/png", class: "hidden", "aria-label": label, onchange: () => upload(input, `${x.id}/${part}`, preview, src) });
      const pick = () => input.click();
      const preview = h("div", { class: "art pickable " + cls, title: "Choose a picture", onclick: pick, style: has ? `background-image:url("${src}?t=${Date.now()}")` : "" });
      return h("div", { class: "art-card" }, preview, h("b", {}, label), note && h("span", { class: "dim" }, note),
        h("button", { type: "button", class: "small", onclick: pick }, "Upload…"), input);
    };
    const main = h("div", { class: "art-cards" },
      card("poster", "poster", "Poster", "A tall picture, JPEG or PNG.", `/api/image/${x.id}/poster`, x.poster),
      card("backdrop", "backdrop", "Backdrop", "A wide picture behind the title.", `/api/image/${x.id}/backdrop`, x.backdrop));
    // The artwork of the catalogue entry the description comes from: when
    // it could not be downloaded at the time, or to replace it.
    const note = h("span", { class: "dim" }, artNote);
    // It runs on the server in the background (a series has a still for
    // every episode); the sheet follows it.
    const fetchArt = async (e, replace) => {
      const buttons = e.target.parentNode.querySelectorAll("button");
      buttons.forEach(b => { b.disabled = true; });
      note.textContent = "Starting…";
      try {
        let job = await api(`meta/${x.id}/artwork`, { json: { replace } });
        while (job.running) {
          note.textContent = `Downloading… ${job.fetched} picture(s) so far`;
          await new Promise(r => setTimeout(r, 1000));
          if (!document.body.contains(note)) return; // the sheet was closed: it goes on without it
          job = await api("artwork");
        }
        saved();
        const res = job.result || { fetched: job.fetched, problems: [], source: "" };
        if (job.error) throw new Error(job.error);
        artNote = (res.fetched ? `${res.fetched} picture(s) downloaded from ${res.source}.` : `Nothing downloaded: ${res.source} has no other pictures for it.`) +
          (res.problems.length ? ` Not downloaded: ${res.problems.slice(0, 3).join("; ")}${res.problems.length > 3 ? `, and ${res.problems.length - 3} more` : ""}.` : "");
        if (res.fetched) {
          x.poster = x.backdrop = true;
          (x.seasons || []).forEach(s => { s.poster = true; });
          return body.replaceChildren(imagesTab());
        }
        note.textContent = artNote;
      } catch (err) { note.textContent = err.message; }
      buttons.forEach(b => { b.disabled = false; });
    };
    const fromCatalogue = h("div", { class: "row tools" },
      h("button", { onclick: e => fetchArt(e, false) }, "Download missing artwork"),
      h("button", { onclick: e => fetchArt(e, true) }, "Replace with the catalogue's"),
      note);
    if (!movie) return h("div", {}, main, fromCatalogue,
      h("h3", { class: "sub" }, "Season posters"),
      h("div", { class: "art-cards seasons" }, x.seasons.map(s => card(`season${String(s.number).padStart(2, "0")}`, "poster",
        s.number ? `Season ${s.number}` : "Specials", "", `/api/image/${s.id}/poster`, s.poster))));
    return h("div", {}, main, fromCatalogue,
      h("div", { class: "row tools" },
        h("button", {
          onclick: async e => {
            e.target.disabled = true;
            try { await api(`screens/${x.id}/regenerate`, { method: "POST" }); dirty = true; close(); }
            catch (err) { e.target.disabled = false; alert(err.message); }
          },
        }, "Regenerate screenshots"),
        h("span", { class: "dim" }, "New frames from other moments of the film.")));
  }

  // episodesTab lists the episodes of one season; each is saved on its own.
  function episodesTab() {
    const holder = h("div", {}, h("p", { class: "dim" }, "Loading…"));
    api(`meta/${x.id}/episodes`).then(list => {
      const seasons = [...new Set(list.map(e => e.season))];
      let season = seasons.includes(openSeason[x.id]) ? openSeason[x.id] : seasons[0];
      const listBox = h("div", { class: "ep-list" });
      const draw = () => listBox.replaceChildren(...list.filter(e => e.season === season).map(episodeForm));
      const picker = h("select", { "aria-label": "Season", onchange: () => { season = +picker.value; draw(); } },
        seasons.map(n => h("option", { value: n, selected: n === season }, n ? `Season ${n}` : "Specials")));
      holder.replaceChildren(
        h("div", { class: "row" }, picker, h("span", { class: "dim" }, "Each episode is saved on its own, into its .nfo and the tags of its file.")),
        listBox);
      draw();
    }).catch(err => holder.replaceChildren(h("p", { class: "error" }, err.message)));
    return holder;
  }

  function episodeForm(e) {
    const pad = n => String(n).padStart(2, "0");
    const number = `S${pad(e.season)}E${pad(e.episode)}${e.episodeEnd > e.episode ? "-E" + pad(e.episodeEnd) : ""}`;
    const changed = () => { save.disabled = false; };
    const title = text(e.title, { placeholder: `Episode ${e.episode}`, oninput: changed });
    const aired = text(e.aired, { placeholder: "2001-09-26", oninput: changed });
    const plot = h("textarea", { rows: 3, value: e.plot || "", oninput: changed });
    const save = h("button", { class: "primary small", type: "submit", disabled: true }, "Save");
    const error = h("span", { class: "error" });
    const src = `/api/image/${e.id}/thumb`;
    const still = h("div", { class: "art still", style: e.thumb ? `background-image:url("${src}?t=${Date.now()}")` : "" });
    const file = h("input", { type: "file", accept: "image/jpeg,image/png", class: "hidden", onchange: () => upload(file, `${e.id}/thumb`, still, src) });
    return h("form", {
      class: "ep-edit",
      onsubmit: async ev => {
        ev.preventDefault();
        error.textContent = "";
        save.disabled = true;
        const values = { title: title.value.trim(), aired: aired.value.trim(), plot: plot.value };
        try {
          const res = await api(`meta/${e.id}`, { json: values });
          Object.assign(e, values);
          saved();
          toast(res.tags ? "Saved. The tags inside the file are being updated." : "Saved.");
        } catch (err) { error.textContent = err.message; save.disabled = false; }
      },
    },
      h("label", { class: "still-pick", title: "Choose another still" }, still, h("span", { class: "dim" }, "Change the still"), file),
      h("div", { class: "grow" },
        h("div", { class: "ep-head" }, h("b", {}, number), h("span", { class: "dim" }, e.file)),
        h("div", { class: "fields" }, field("Title", title), field("Aired", aired)),
        field("Plot", plot),
        h("div", { class: "row" }, save, error)));
  }

  const box = h("div", { class: "modal", onclick: e => { if (e.target === box) close(); } },
    h("div", { class: "panel glass sheet" },
      h("div", { class: "row" }, h("h2", { class: "grow", style: "margin:0" }, `Edit · ${x.title}`), h("button", { class: "small", onclick: close }, "Close")),
      tabBar, body));
  document.body.append(box);
  document.addEventListener("keydown", onKey);
  choose(current);
}

// ----------------------------------------------------------------- player

// play opens the player. Files the browser understands are played as they
// are; others are converted by the server on the fly. A converted stream
// has no fixed length, so seeking asks the server for a new one from that
// moment, and the position is the start of the stream plus its own clock.
// Safari takes the converted video as HLS, the only streamed form it plays
// without downloading everything first; other browsers take a plain stream.
async function play(item, startAt, queue) {
  let info;
  try { info = await api("item/" + item.id); } catch (err) { return alert(err.message); }
  const duration = info.duration;
  let converted = false, offset = 0, audio = 0, closed = false, session = null;
  // Subtitles: the key of the track shown ("" for none), and the picture
  // track burned into the converted stream (-1 for none).
  const subTracks = info.subtitleTracks || [];
  let subKey = "", burn = -1;

  const video = h("video", { controls: true, autoplay: true, playsInline: true });
  const nativeHLS = !!video.canPlayType("application/vnd.apple.mpegurl");
  const note = h("div", { class: "note hidden" });
  const slider = h("input", { type: "range", min: 0, max: Math.max(1, Math.floor(duration)), step: 1, "aria-label": "Seek" });
  const clock = h("span", {});
  // A converted stream is made while it plays: the browser does not know
  // its length, and its own controls offer no seeking — Safari's full
  // screen even calls it a live broadcast. So a converted video gets these
  // controls instead of the browser's, and goes full screen with them.
  const fsElement = () => document.fullscreenElement || document.webkitFullscreenElement;
  const canFullscreen = !!(document.documentElement.requestFullscreen || document.documentElement.webkitRequestFullscreen);
  let leftFullscreen = 0;
  function toggleFullscreen() {
    if (fsElement()) (document.exitFullscreen || document.webkitExitFullscreen).call(document);
    else if (canFullscreen) (box.requestFullscreen || box.webkitRequestFullscreen).call(box);
    else if (video.webkitEnterFullscreen) video.webkitEnterFullscreen(); // an iPhone: only the bare video can
  }
  const togglePlay = () => video.paused ? video.play().catch(() => {}) : video.pause();
  const control = (name, label, action) => h("button", { class: "small", title: label, "aria-label": label, onclick: action }, icon(name));
  const playButton = control("play", "Play (Space)", togglePlay);
  const muteButton = control("sound", "Sound", () => { video.muted = !video.muted; });
  const volume = h("input", { type: "range", class: "volume", min: 0, max: 1, step: 0.05, value: 1, "aria-label": "Volume",
    oninput: () => { video.volume = +volume.value; video.muted = video.volume === 0; } });
  const pipButton = (document.pictureInPictureEnabled || video.webkitSupportsPresentationMode) && control("pip", "Picture in picture", () => {
    if (video.webkitSetPresentationMode) video.webkitSetPresentationMode(video.webkitPresentationMode === "picture-in-picture" ? "inline" : "picture-in-picture");
    else if (document.pictureInPictureElement) document.exitPictureInPicture();
    else video.requestPictureInPicture().catch(() => {});
  });
  const fullscreen = (canFullscreen || video.webkitEnterFullscreen) && control("fullscreen", "Full screen (F)", toggleFullscreen);
  const seek = h("div", { class: "seek glass hidden" }, playButton, slider, clock, muteButton, volume, pipButton, fullscreen);
  const showState = () => {
    playButton.replaceChildren(icon(video.paused ? "play" : "pause"));
    muteButton.replaceChildren(icon(video.muted || video.volume === 0 ? "muted" : "sound"));
    if (!volume.matches(":active")) volume.value = video.muted ? 0 : video.volume;
  };
  for (const ev of ["play", "pause", "volumechange"]) video.addEventListener(ev, showState);
  showState();
  const audioSelect = (info.audio || []).length > 1 && h("select", {
    "aria-label": "Audio track",
    onchange: () => { audio = +audioSelect.value; convert(position()); },
  }, info.audio.map((a, i) => h("option", { value: i }, `Audio ${i + 1}: ${[a.title, a.language, a.codec].filter(Boolean).join(", ")}`)));
  // Text subtitles are shown by the browser over any stream; picture ones
  // (Blu-ray, DVD) only burned into a converted one, which they switch to.
  const subSelect = subTracks.length > 0 && h("select", {
    "aria-label": "Subtitles",
    onchange: () => chooseSubtitles(subSelect.value, true),
  }, h("option", { value: "" }, "Subtitles: off"),
    subTracks.map(t => h("option", { value: t.key }, t.image ? `${t.label} (burned in)` : t.label)));
  function chooseSubtitles(key, byHand) {
    const track = subTracks.find(t => t.key === key);
    subKey = track ? key : "";
    if (subSelect) subSelect.value = subKey;
    if (byHand) { // remembered for the next video: the language, or none
      try { localStorage.setItem("mk_subtitles", track && !track.image ? track.lang || track.label : track ? "" : "off"); } catch { /* private mode */ }
    }
    const wanted = track && track.image ? track.ordinal : -1;
    if (wanted !== burn) { // the picture has to change: a new stream
      burn = wanted;
      if (burn >= 0 || converted) return convert(position());
    }
    showSubtitles();
  }
  function showSubtitles() {
    for (const t of video.textTracks) t.mode = t.id === "sub-" + subKey ? "showing" : "disabled";
  }
  const mode = h("button", { onclick: () => converted ? direct(position()) : convert(position()) });
  const box = h("div", { class: "player" },
    h("div", { class: "top glass" },
      h("button", { onclick: close }, "← Back"),
      h("span", { class: "name" }, item.show ? `${item.show} · S${item.season}E${item.episode} · ${item.title}` : fullTitle(item)),
      audioSelect, subSelect, info.canTranscode && mode),
    note, video, seek);

  const position = () => (converted ? offset : 0) + (video.currentTime || 0);
  const say = text => { note.textContent = text || ""; note.classList.toggle("hidden", !text); };
  const endSession = () => {
    if (session) api("hls/s/" + session, { method: "DELETE" }).catch(() => {});
    session = null;
  };

  // subtitles puts the text tracks on the video, their times moved back by
  // shift (a converted stream starts at zero), and shows the chosen one. A
  // track is only fetched once it is shown.
  function subtitles(shift) {
    video.querySelectorAll("track").forEach(t => t.remove());
    for (const t of subTracks.filter(t => !t.image))
      video.append(h("track", { kind: "subtitles", id: "sub-" + t.key, label: t.label, srclang: t.lang || "", src: `/api/subs/${info.id}/${t.key}.vtt?offset=${shift}` }));
    showSubtitles();
    // The browser sets up the tracks a moment later.
    setTimeout(showSubtitles, 0);
  }
  function direct(at) {
    endSession();
    converted = false; offset = 0; say("");
    if (burn >= 0) { burn = -1; subKey = ""; if (subSelect) subSelect.value = ""; } // the original has no burned-in subtitles
    box.classList.remove("custom", "idle");
    video.controls = true; // the browser's own: the file has a length, they can seek
    mode.textContent = "Does not play? Convert";
    seek.classList.add("hidden");
    video.src = `/api/stream/${info.id}`;
    video.addEventListener("loadedmetadata", () => { if (at > 0) video.currentTime = at; }, { once: true });
    subtitles(0);
  }
  // convert plays a stream the server converts from a moment of the video.
  // The new position counts only once the server has started it: if it
  // cannot, the seek bar goes back to where the video really is.
  async function convert(at) {
    if (!info.canTranscode) return say("The browser cannot play this file, and the server has no ffmpeg to convert it. Download the file and open it in a player.");
    const from = Math.max(0, Math.floor(at || 0));
    mode.textContent = "Play the original";
    seek.classList.remove("hidden");
    video.controls = false; // ours instead, see above
    box.classList.add("custom"); // over the picture, hidden while it plays
    wake();
    if (nativeHLS) {
      say("");
      try {
        // The server ends this viewer's previous stream itself.
        const started = await api(`hls/start/${info.id}`, { json: { start: from, audio, ...(burn >= 0 ? { burn } : {}) } });
        if (closed) return api("hls/s/" + started.id, { method: "DELETE" }).catch(() => {});
        session = started.id;
        converted = true; offset = from;
        video.src = started.url;
      } catch (err) {
        slider.value = position();
        return say(/busy/.test(err.message) ? "The server is busy converting other videos: try again in a moment." : err.message);
      }
    } else {
      converted = true; offset = from; say("");
      video.src = `/api/transcode/${info.id}?start=${offset}&audio=${audio}` + (burn >= 0 ? `&burn=${burn}` : "");
    }
    slider.value = offset;
    subtitles(offset);
    video.play().catch(() => {});
  }
  video.addEventListener("error", () => {
    if (closed || !video.error) return;
    if (!converted) convert(position() || startPosition);
    else recover();
  });

  // A converted stream that stops moving while it should play (a network
  // hiccup, a session the server ended) is started again from where it
  // stopped. Several failures in a row are reported instead.
  let restarts = 0, lastTime = -1, stuckSince = 0;
  function recover() {
    if (restarts++ >= 3) return say("Playback keeps stopping: the server may be busy. Try again later, or download the file.");
    convert(position());
  }
  const watchdog = setInterval(() => {
    if (!converted || video.paused || video.ended || closed) { stuckSince = 0; return; }
    if (video.currentTime !== lastTime) {
      if (video.currentTime > lastTime + 30) restarts = 0; // playing well again
      lastTime = video.currentTime;
      stuckSince = 0;
      return;
    }
    if (!stuckSince) stuckSince = Date.now();
    else if (Date.now() - stuckSince > 20000) { stuckSince = 0; lastTime = -1; recover(); }
  }, 2000);
  video.addEventListener("timeupdate", () => {
    if (!slider.matches(":active")) slider.value = position();
    clock.textContent = `${time(position())} / ${time(duration)}`;
  });
  slider.addEventListener("change", () => convert(+slider.value));
  video.addEventListener("ended", async () => {
    await report(duration);
    const i = queue ? queue.findIndex(e => e.id === item.id) : -1;
    close();
    if (i >= 0 && queue[i + 1]) play(queue[i + 1], 0, queue);
  });

  let lastReport = 0;
  async function report(at, stopped) {
    lastReport = Date.now();
    if (me.guest) return; // nothing is remembered without an account
    try { await api("progress/" + info.id + (stopped ? "?stopped=1" : ""), { json: { position: at } }); } catch { /* next time */ }
  }
  const ticker = setInterval(() => { if (!video.paused && Date.now() - lastReport > 9000) report(position()); }, 2000);
  video.addEventListener("pause", () => report(position()));

  function close() {
    if (closed) return;
    closed = true;
    clearInterval(ticker);
    clearInterval(watchdog);
    clearTimeout(idleTimer);
    const at = position();
    video.pause(); video.removeAttribute("src"); video.load(); // stops the download and the conversion
    endSession();
    if (fsElement()) (document.exitFullscreen || document.webkitExitFullscreen).call(document);
    box.remove();
    document.removeEventListener("keydown", onKey);
    document.removeEventListener("fullscreenchange", onFullscreen);
    document.removeEventListener("webkitfullscreenchange", onFullscreen);
    (at > 0 ? report(at, true) : Promise.resolve()).then(render);
  }
  const onKey = e => {
    wake();
    // Escape first leaves full screen; only the next one closes the player.
    if (e.key === "Escape" && !fsElement() && Date.now() - leftFullscreen > 500) close();
    else if (!converted || e.target.closest("input, select, button")) return;
    else if ((e.key === "f" || e.key === "F") && fullscreen) toggleFullscreen();
    else if (e.key === " " || e.key === "k") { e.preventDefault(); togglePlay(); }
  };
  document.addEventListener("keydown", onKey);
  const onFullscreen = () => { if (!fsElement()) leftFullscreen = Date.now(); };
  document.addEventListener("fullscreenchange", onFullscreen);
  document.addEventListener("webkitfullscreenchange", onFullscreen);
  // Our controls lie over the picture and fade away while the video plays
  // and nobody touches anything; a move of the mouse, a tap or a key brings
  // them back. (They lie over it only without the browser's controls:
  // Safari puts its own buttons in the corners of the video.)
  let idleTimer = null, wokenByTap = false;
  function wake() {
    box.classList.remove("idle");
    clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      const busy = video.paused || slider.matches(":active") || box.querySelector(".top:hover, .seek:hover, select:focus");
      if (converted && !busy && !closed) box.classList.add("idle");
    }, 3000);
  }
  box.addEventListener("mousemove", wake);
  box.addEventListener("keydown", wake);
  video.addEventListener("pointerdown", () => { wokenByTap = box.classList.contains("idle"); wake(); });
  for (const ev of ["play", "pause"]) video.addEventListener(ev, wake);
  // Without the browser's controls a click on the picture pauses, as with
  // them; a tap that only brings the controls back does not.
  let clickTimer = null;
  video.addEventListener("click", () => {
    if (!converted) return;
    if (wokenByTap) { wokenByTap = false; return; }
    clearTimeout(clickTimer);
    clickTimer = setTimeout(togglePlay, 250); // not when it is the first half of a double click
  });
  video.addEventListener("dblclick", () => { if (converted && fullscreen) { clearTimeout(clickTimer); toggleFullscreen(); } });

  const startPosition = startAt !== undefined ? startAt : (info.position || 0);
  // The subtitles chosen last time: the same language again, if the video
  // has it as text (picture ones would force a conversion).
  let remembered = "";
  try { remembered = localStorage.getItem("mk_subtitles") || ""; } catch { /* private mode */ }
  const again = remembered && remembered !== "off" && subTracks.find(t => !t.image && (t.lang || t.label) === remembered);
  if (again) { subKey = again.key; if (subSelect) subSelect.value = subKey; }
  document.body.append(box);
  box.tabIndex = -1;
  box.focus(); // not the page's Play button beneath: Space is for the player now
  // Start converted right away when the server knows the browser cannot play the file.
  if (info.direct === false) convert(startPosition); else direct(startPosition);
}

// -------------------------------------------------------------- downloads

const stateNames = { downloading: "Downloading", organizing: "Organizing", attention: "Needs you", done: "In the library", error: "Failed" };

async function renderDownloads() {
  const source = h("input", { type: "text", class: "grow", placeholder: "magnet:?xt=…  or  https://…/file.mkv  or  https://…/file.torrent", "aria-label": "Link" });
  const file = h("input", { type: "file", accept: ".torrent,application/x-bittorrent", "aria-label": "Torrent file" });
  const error = h("p", { class: "error" });
  const list = h("div", {});
  // Where downloads are filed: by kind, or a library folder chosen (the
  // last choice is offered again).
  let roots = [];
  try { roots = await api("downloads/libraries"); } catch { /* by kind, then */ }
  const kindNote = { movies: "movies", shows: "series", "": "movies and series" };
  const rootName = r => `${r.path} — ${kindNote[r.kind || ""]}${r.free >= 0 ? ` · ${bytes(r.free)} free` : ""}`;
  const rootOptions = chosen => [h("option", { value: "", selected: !chosen }, "Automatically: each title to a folder of its kind"),
    roots.map(r => h("option", { value: r.path, selected: r.path === chosen }, rootName(r)))];
  let lastRoot = "";
  try { lastRoot = localStorage.getItem("mk_download_root") || ""; } catch { /* private mode */ }
  if (!roots.some(r => r.path === lastRoot)) lastRoot = "";
  const target = h("select", { "aria-label": "Library folder", class: "grow", onchange: () => {
    try { localStorage.setItem("mk_download_root", target.value); } catch { /* private mode */ }
  } }, rootOptions(lastRoot));
  const open = {}; // unit key -> the state of its form, kept across refreshes
  const presetOpen = {}; // download id -> the "what is it" form is open
  let last = []; // the list as last drawn

  // fixFiled opens the edit sheet of a title a download became, searching.
  async function fixFiled(t) {
    // Fresh: the list in memory may be older than the download.
    try { library = await api("library"); } catch (err) { return alert(err.message); }
    const x = t.kind === "show" ? library.shows.map(asShow).find(s => s.id === t.id) : library.movies.find(m => m.id === t.id);
    if (!x) return alert("This title is not in the library any more.");
    openEdit(x, { find: true, stay: true });
  }

  async function add(e) {
    e.preventDefault();
    error.textContent = "";
    try {
      let result;
      if (file.files.length) {
        const form = new FormData();
        form.append("torrent", file.files[0]);
        form.append("root", target.value);
        result = await api("downloads", { method: "POST", body: form });
      } else if (source.value.trim()) {
        result = await api("downloads", { json: { source: source.value, root: target.value } });
      } else return;
      source.value = ""; file.value = "";
      draw(result, true);
    } catch (err) { error.textContent = err.message; }
  }

  // The periodic refresh does not redraw while the administrator is typing
  // in one of the forms; the answer to an action of theirs always does.
  function draw(downloads, force) {
    last = downloads;
    refreshBadge(downloads);
    if (!force && list.contains(document.activeElement) && ["INPUT", "SELECT"].includes(document.activeElement.tagName)) return;
    list.replaceChildren(...(downloads.length ? downloads.map(card) : [h("div", { class: "empty" }, "Nothing has been downloaded yet.")]));
  }

  function card(d) {
    const act = async (path, options) => { draw(await api(`downloads/${d.id}${path}`, options), true); library = null; };
    const alerting = (path, options) => act(path, options).catch(err => alert(err.message));
    // While it downloads, what it is can be said in advance.
    const presetKey = d.id + "/preset";
    const presetBox = d.state === "downloading" && h("div", { class: "preset" },
      d.preset ? h("div", { class: "row" },
        h("span", { class: "grow" }, "Will be filed as ", h("b", {}, d.preset)),
        h("button", { class: "small", onclick: () => { presetOpen[d.id] = true; draw(last, true); } }, "Change"),
        h("button", { class: "small", onclick: () => alerting("/preset", { method: "DELETE" }) }, "Let it be recognized"))
      : !presetOpen[d.id] && h("div", { class: "row" },
        h("button", { class: "small", onclick: () => { presetOpen[d.id] = true; draw(last, true); } }, "Say what it is…"),
        h("span", { class: "dim" }, "Pick it now, and it is filed as that when the download finishes.")),
      presetOpen[d.id] && identifyForm({
        unit: { title: d.name }, st: open[presetKey] = open[presetKey] || {}, fill: true, ask: true,
        search: q => api(`downloads/${d.id}/search?q=${encodeURIComponent(q)}`),
        resolve: async body => { await act("/preset", { json: body }); presetOpen[d.id] = false; delete open[presetKey]; draw(last, true); },
        extra: h("button", { type: "button", onclick: () => { presetOpen[d.id] = false; draw(last, true); } }, "Cancel"),
      }));
    // What it became: links to the titles, and a quick way to correct them.
    const titles = (d.titles || []).length > 0 && h("div", { class: "filed" },
      h("div", { class: "dim" }, "In the library:"),
      d.titles.map(t => h("div", { class: "filed-title" },
        h("a", { class: "thumb", href: `#${t.kind}/${t.id}`, style: t.poster ? `background-image:${image(t.id, "poster")}` : "" }),
        h("a", { class: "grow", href: `#${t.kind}/${t.id}` }, fullTitle(t), t.year ? ` (${t.year})` : ""),
        h("button", { class: "small", onclick: () => fixFiled(t) }, "Wrong? Fix…"))));
    // Where it goes, changeable until it is filed.
    const where = roots.length > 1 && ["downloading", "attention"].includes(d.state) && h("div", { class: "row download-root" },
      h("span", { class: "dim" }, "Save to"),
      h("select", { "aria-label": "Library folder", onchange: e => alerting("/root", { json: { root: e.target.value } }) }, rootOptions(d.root || "")));
    return h("div", { class: "panel glass download" },
      h("div", { class: "head" },
        h("span", { class: "name" }, d.name),
        h("span", { class: "state " + d.state }, stateNames[d.state] || d.state),
        h("button", { class: "small danger", onclick: () => confirm(d.state === "downloading" ? "Stop this download?" : "Remove this entry?") && alerting("", { method: "DELETE" }) },
          d.state === "downloading" ? "Stop" : "Remove")),
      d.state === "organizing" && [
        h("progress", d.toMove ? { value: d.moved, max: d.toMove } : {}),
        h("div", { class: "dim" }, d.toMove ? `Moving into the library: ${d.moved} of ${d.toMove} file(s) — from another disk this takes a while`
          : "Identifying and preparing the files…")],
      d.state === "downloading" && [
        h("progress", { value: d.done, max: d.total || 1 }),
        h("div", { class: "dim" }, d.wait ? `Fetching ${d.wait} · ${d.peers} peer(s)${d.peers ? "" : " — nobody found yet who shares it"}`
          : d.total ? `${bytes(d.done)} of ${bytes(d.total)} · ${bytes(d.speed)}/s · ${d.peers} peer(s), ${d.seeds} seeder(s)` : "Connecting…"),
        d.total > 0 && d.free >= 0 && d.total - d.done > d.free &&
          h("p", { class: "error" }, `Not enough room on its disk: ${bytes(d.total - d.done)} more is needed, ${bytes(d.free)} is free.`)],
      presetBox,
      where,
      d.error && h("p", { class: "error" }, d.error),
      titles,
      d.log && h("details", { class: "log-box", open: d.state !== "done" }, h("summary", { class: "dim" }, "What was done"), h("pre", { class: "log" }, d.log)),
      (d.pending || []).map((u, i) => identifyForm({
        unit: u, st: open[d.id + "/" + u.key] = open[d.id + "/" + u.key] || (d.series ? { asMovie: false } : {}), auto: i < 3,
        search: q => api(`downloads/${d.id}/search?key=${encodeURIComponent(u.key)}&q=${encodeURIComponent(q)}`),
        resolve: body => act("/resolve", { json: { key: u.key, ...body } }),
        extra: h("button", { type: "button", class: "danger", onclick: () => confirm("Delete these files?") && alerting("/discard", { json: { key: u.key } }) }, "Delete the files"),
      })));
  }

  shell("downloads",
    h("form", { class: "panel glass form", onsubmit: add },
      h("div", { class: "row" }, source, h("button", { class: "primary" }, "Download")),
      h("div", { class: "row", style: "margin-top:10px" }, h("span", { class: "dim" }, "or a .torrent file:"), file),
      roots.length > 1 && h("div", { class: "row", style: "margin-top:10px" }, h("span", { class: "dim" }, "Save to:"), target),
      h("p", { class: "dim", style: "margin-bottom:0" }, "After downloading, the file is identified, renamed and put into the library. If the program is not sure, it asks here; while it downloads, you can also say what it is."),
      error),
    list);
  const refresh = async () => { try { draw(await api("downloads")); } catch { /* shown on the next tick */ } };
  await refresh();
  pollTimer = setInterval(refresh, 2000);
}

// --------------------------------------------------------------- settings

// fill puts children in place of an element's own, as h() takes them.
const fill = (el, ...children) => { el.replaceChildren(...children.flat(3).filter(c => c != null && c !== false)); return el; };
const field = (label, input, note) => h("label", { class: "field" }, h("span", {}, label), input, note && h("small", { class: "dim" }, note));
const joinPath = (dir, name) => dir.endsWith("/") ? dir + name : dir + "/" + name;
const rootKinds = [["", "Movies and shows"], ["movies", "Movies"], ["shows", "Shows"]];

// folderPicker walks the server's folders to choose one: the server, not
// the browser, has to read the library. A new folder can be made in the one
// that is open.
function folderPicker(startAt, choose) {
  const error = h("p", { class: "error" });
  const where = h("input", { type: "text", "aria-label": "Folder", spellcheck: false, autocomplete: "off" });
  const list = h("div", { class: "folders" });
  const note = h("p", { class: "dim" });
  const name = h("input", { type: "text", placeholder: "New folder", "aria-label": "Name of the new folder", spellcheck: false, autocomplete: "off" });
  const making = h("form", { class: "row new-folder hidden", onsubmit: e => { e.preventDefault(); make(); } },
    icon("folder"), h("div", { class: "grow" }, name), h("button", { class: "small primary" }, "Make"),
    h("button", { type: "button", class: "small", onclick: () => making.classList.add("hidden") }, "Cancel"));
  const newButton = h("button", { type: "button", onclick: () => { making.classList.remove("hidden"); name.value = ""; name.focus(); } }, "+ New folder");
  let current = "";
  const close = () => box.remove();
  const show = d => {
    current = d.path;
    where.value = d.path;
    making.classList.add("hidden");
    fill(list,
      d.parent && h("button", { type: "button", class: "folder", onclick: () => open(d.parent) }, icon("up"), "Up"),
      d.folders.map(f => h("button", { type: "button", class: "folder", onclick: () => open(joinPath(d.path, f)) }, icon("folder"), f)),
      !d.folders.length && h("p", { class: "dim empty-folder" }, "No folders inside."));
    newButton.disabled = !d.writable;
    note.textContent = d.writable ? "" : "The server can read this folder but not write to it: nothing can be made or kept in it.";
  };
  const open = async path => {
    error.textContent = "";
    try { show(await api("settings/folders?path=" + encodeURIComponent(path || ""))); } catch (err) { error.textContent = err.message; }
  };
  const make = async () => {
    error.textContent = "";
    try { show(await api("settings/folders", { json: { path: current, name: name.value } })); } catch (err) { error.textContent = err.message; }
  };
  const box = h("div", { class: "modal", onclick: e => { if (e.target === box) close(); } },
    h("div", { class: "panel glass sheet picker" },
      h("div", { class: "row" }, h("h2", { class: "grow", style: "margin:0" }, "Choose a folder"), h("button", { class: "small", type: "button", onclick: close }, "Close")),
      h("form", { class: "row where", onsubmit: e => { e.preventDefault(); open(where.value); } }, where, h("button", { class: "small" }, "Go")),
      list, making, note, error,
      h("div", { class: "row picker-buttons" }, newButton, h("span", { class: "spacer" }),
        h("button", { class: "primary", type: "button", onclick: () => { close(); choose(current || where.value); } }, "Use this folder"))));
  document.body.append(box);
  open(startAt);
}

// folderEditor edits a list of library folders in place: each with what it
// holds; the first one also takes the downloads.
function folderEditor(roots, disabled) {
  const box = h("div", { class: "roots" });
  const draw = () => fill(box,
    roots.length ? roots.map((r, i) => h("div", { class: "root-row" },
      h("div", { class: "root-path" }, icon("folder"), h("div", {}, h("div", {}, r.path),
        r.missing && h("small", { class: "error" }, "not there now"),
        i === 0 && roots.length > 1 && h("small", { class: "dim" }, "downloads go here"))),
      h("div", { class: "root-controls" },
        h("select", { "aria-label": "What it holds", disabled, onchange: e => { r.kind = e.target.value; } },
          rootKinds.map(([k, label]) => h("option", { value: k, selected: (r.kind || "") === k }, label))),
        h("span", { class: "spacer" }),
        !disabled && i > 0 && h("button", { type: "button", class: "small icon-button", title: "Move up", "aria-label": "Move up", onclick: () => { roots.splice(i - 1, 0, ...roots.splice(i, 1)); draw(); } }, icon("up")),
        !disabled && h("button", { type: "button", class: "small danger", onclick: () => { roots.splice(i, 1); draw(); } }, "Remove"))))
      : h("p", { class: "dim" }, "No folders yet."),
    !disabled && h("button", { type: "button", onclick: () => folderPicker(roots.length ? roots[roots.length - 1].path.replace(/\/[^/]+\/?$/, "") || "/" : "", path => {
      if (!roots.some(r => r.path === path)) roots.push({ path, kind: "" });
      draw();
    }) }, "+ Add a folder"));
  draw();
  return box;
}

// pathInput puts a "Choose…" button next to a field for a place on the
// server: a folder, or with file a file of that name in the folder chosen.
function pathInput(input, file) {
  const choose = h("button", { type: "button", class: "small", disabled: input.disabled, onclick: () => {
    const value = input.value.trim();
    folderPicker(file ? value.replace(/\/[^/]*$/, "") : value, dir => {
      input.value = file ? joinPath(dir, file) : dir;
      input.dispatchEvent(new Event("input"));
    });
  } }, "Choose…");
  return h("div", { class: "row path-input" }, h("div", { class: "grow" }, input), choose);
}

// Notes under the database's place: how the server finds it again, and that
// in a container only mounted folders outlive it.
const dockerNote = state => (state.configDir ? ` Kept anywhere but ${state.configDir}, a small config.yaml there says where it is.` : "") +
  (state.container ? " In Docker, keep it in a mounted folder (as /config), or it is lost with the container." : "");

// keyInput is an API key field: a key that is set is not sent back to the
// browser; typing replaces it, the button removes it.
function keyInput(state, change) {
  const input = h("input", { type: "password", autocomplete: "off", spellcheck: false,
    placeholder: state.set ? `set, ends with …${state.end}` : "not set", oninput: () => change(input.value.trim() || undefined) });
  return input;
}

function lockedNote(locked, key) {
  return locked[key] && h("small", { class: "locked" }, `Set by ${locked[key]}: change it there.`);
}

async function renderSettings(tab) {
  const tabs = [["library", "Library"], ["metadata", "Descriptions"], ["server", "Server"], ["users", "Users"]];
  if (!tabs.some(([id]) => id === tab)) tab = "library";
  const bar = h("div", { class: "tabs" }, tabs.map(([id, label]) => h("button", { class: id === tab ? "active" : "", onclick: () => { location.hash = "#settings/" + id; } }, label)));
  const body = h("div", {});
  shell("settings", h("div", { class: "settings" }, h("h1", { class: "page-title" }, "Settings"), scrollingTabs(bar), body));
  if (tab === "users") return usersPanel(body);
  let view;
  try { view = await api("settings"); } catch (err) { return body.replaceChildren(h("p", { class: "error" }, err.message)); }
  const draw = { library: libraryTab, metadata: metadataTab, server: serverTab }[tab];
  body.replaceChildren(draw(view, async change => {
    const restart = await api("settings", { json: change });
    library = null;
    toast(restart.restart && restart.restart.length ? `Saved. Restart the server for ${restart.restart.join(" and ")} to change.` : "Saved.");
    return restart;
  }));
}

// saveRow is a Save button that sends what a tab gathered and shows errors.
function saveRow(gather, save, after) {
  const error = h("p", { class: "error" });
  const button = h("button", { class: "primary", type: "button", onclick: async () => {
    error.textContent = "";
    button.disabled = true;
    try { const view = await save(gather()); if (after) after(view); } catch (err) { error.textContent = err.message; }
    button.disabled = false;
  } }, "Save");
  return h("div", { class: "row save-row" }, button, error);
}

function libraryTab(view, save) {
  const roots = view.libraries.map(r => ({ ...r }));
  const locked = view.locked["libraries"];
  return h("div", {}, h("div", { class: "panel glass" },
    h("h2", { style: "margin-top:0" }, "Library folders"),
    h("p", { class: "dim" }, "The folders of films and series on the server. A folder of movies or of shows holds only those, each title in its own folder; a mixed one is sorted into Movies and Shows inside. Changes show in the library at once."),
    lockedNote(view.locked, "libraries"),
    folderEditor(roots, !!locked),
    !locked && saveRow(() => ({ libraries: roots.map(({ path, kind }) => ({ path, kind })) }), save, render)),
    artworkPanel());
}

// artworkPanel takes the posters, backdrops, season posters and episode
// stills missing in the whole library from the catalogues, in the
// background, and shows how it goes.
function artworkPanel() {
  const state = h("p", { class: "dim" });
  const button = h("button", { type: "button", onclick: async () => {
    try { show(await api("artwork", { method: "POST" })); } catch (err) { state.textContent = err.message; }
  } }, "Download missing artwork");
  let timer = null;
  const show = j => {
    button.disabled = j.running;
    if (j.running) {
      state.textContent = `Looking at ${j.done + 1} of ${j.total}${j.title ? `: ${j.title}` : ""} · ${j.fetched} picture(s) so far`;
      clearTimeout(timer);
      timer = setTimeout(() => { if (document.body.contains(button)) api("artwork").then(show).catch(() => {}); }, 1500);
    } else if (j.total) {
      state.textContent = `Done: ${j.fetched} picture(s) downloaded for ${j.done} title(s)` + (j.failed ? `; ${j.failed} could not be completed (no catalogue entry, or a source out of reach).` : ".");
      library = null;
    }
  };
  api("artwork").then(show).catch(() => {});
  return h("div", { class: "panel glass" },
    h("h2", { style: "margin-top:0" }, "Artwork"),
    h("p", { class: "dim" }, "Posters, backdrops, season posters and episode stills that could not be downloaded when the titles were described — a catalogue out of reach, say — are taken again from the catalogue entries their descriptions name. Pictures that are there stay."),
    h("div", { class: "row" }, button), state);
}

function metadataTab(view, save) {
  const change = {};
  const keys = [
    ["tmdbKey", "tmdb_api_key", "TMDB key", "themoviedb.org → Settings → API: descriptions, posters, cast in your language."],
    ["omdbKey", "omdb_api_key", "OMDb key", "omdbapi.com: IMDb ratings and descriptions in English."],
    ["kinopoiskKey", "kinopoisk_api_key", "Kinopoisk key", "kinopoiskapiunofficial.tech: Russian titles and descriptions."]];
  const keyFields = keys.map(([name, lock, label, note]) => {
    const input = keyInput(view[name], v => { if (v === undefined) delete change[name]; else change[name] = v; });
    input.disabled = !!view.locked[lock];
    const remove = view[name].set && !view.locked[lock] && h("button", { type: "button", class: "small danger", onclick: async () => {
      if (!confirm(`Remove the ${label}?`)) return;
      try { await save({ [name]: "" }); render(); } catch (err) { toast(err.message); }
    } }, "Remove");
    return field(label, h("div", { class: "row" }, h("div", { class: "grow" }, input), remove), lockedNote(view.locked, lock) || note);
  });
  const language = h("input", { type: "text", value: view.language, placeholder: "en-US", disabled: !!view.locked.language, oninput: () => { change.language = language.value; } });

  // The sources in the order they are asked, each on or off.
  const known = new Map(view.allSources.map(s => [s.key, s]));
  let order = [...view.sources, ...view.allSources.map(s => s.key).filter(k => !view.sources.includes(k))];
  const on = new Set(view.sources);
  const sourcesLocked = !!view.locked.sources;
  const sourceList = h("div", { class: "sources" });
  const drawSources = () => sourceList.replaceChildren(...order.map((k, i) => {
    const s = known.get(k);
    const key = { tmdb: "tmdbKey", omdb: "omdbKey", kinopoisk: "kinopoiskKey" }[k];
    return h("div", { class: "source-row" + (on.has(k) ? "" : " off") },
      h("label", {}, h("input", { type: "checkbox", checked: on.has(k), disabled: sourcesLocked, onchange: e => {
        e.target.checked ? on.add(k) : on.delete(k); change.sources = order.filter(x => on.has(x)); drawSources();
      } }), " ", s.name),
      key && !view[key].set && h("small", { class: "dim" }, "needs a key"),
      h("span", { class: "spacer" }),
      !sourcesLocked && i > 0 && h("button", { type: "button", class: "small", title: "Ask earlier", onclick: () => {
        order.splice(i - 1, 0, ...order.splice(i, 1)); change.sources = order.filter(x => on.has(x)); drawSources();
      } }, "↑"));
  }));
  drawSources();
  const tmdbUrl = h("input", { type: "text", value: view.tmdbUrl || "", placeholder: "https://api.themoviedb.org/3", oninput: () => { change.tmdbUrl = tmdbUrl.value; } });
  const tmdbImageUrl = h("input", { type: "text", value: view.tmdbImageUrl || "", placeholder: "https://image.tmdb.org/t/p", oninput: () => { change.tmdbImageUrl = tmdbImageUrl.value; } });
  return h("div", {},
    h("div", { class: "panel glass" },
      h("h2", { style: "margin-top:0" }, "Catalogue keys"),
      h("p", { class: "dim" }, "Descriptions, posters and ratings come from these catalogues. A key that is set is not shown again: type a new one to replace it."),
      keyFields,
      field("Language", language, lockedNote(view.locked, "language") || "Of titles and descriptions, as en-US or ru-RU.")),
    h("div", { class: "panel glass" },
      h("h2", { style: "margin-top:0" }, "Sources"),
      h("p", { class: "dim" }, "Asked in this order: the first that knows a title describes it, the others fill in what it lacks."),
      lockedNote(view.locked, "sources"), sourceList,
      h("details", { class: "advanced" }, h("summary", {}, "Where TMDB is reached (for a proxy)"),
        field("TMDB API", tmdbUrl), field("TMDB images", tmdbImageUrl))),
    saveRow(() => change, save, render));
}

function serverTab(view, save) {
  const change = {};
  const lock = key => !!view.locked[key];
  const text = (key, name, value, attrs) => {
    const input = h("input", { type: "text", value: value ?? "", disabled: lock(key), ...attrs, oninput: () => { change[name] = attrs && attrs.type === "number" ? Number(input.value) : input.value; } });
    return input;
  };
  const check = (key, name, value, label, note) => h("div", { class: "toggle" },
    h("label", {}, h("input", { type: "checkbox", checked: value, disabled: lock(key), onchange: e => { change[name] = e.target.checked; } }), " ", label),
    lockedNote(view.locked, key) || (note && h("small", { class: "dim" }, note)));
  const hw = h("select", { disabled: lock("server.hwaccel"), onchange: () => { change.hwaccel = hw.value; } },
    [["", "None: the processor"], ["auto", "Find a graphics card"], ["vaapi", "VA-API (Intel, AMD)"], ["qsv", "Quick Sync (Intel)"], ["nvenc", "NVENC (NVIDIA)"]]
      .map(([v, label]) => h("option", { value: v, selected: (view.hwaccel === "none" ? "" : view.hwaccel || "").split(":")[0] === v }, label)));
  return h("div", {},
    h("div", { class: "panel glass" },
      h("h2", { style: "margin-top:0" }, "Server"),
      field("Name", text("server.name", "name", view.name), lockedNote(view.locked, "server.name") || "Shown in the header, in Jellyfin apps and on the TV."),
      field("Port", text("server.port", "port", view.port, { type: "number", min: 1, max: 65535 }), lockedNote(view.locked, "server.port") || "Takes a restart."),
      check("server.guests", "guests", view.guests, "Watching without signing in", "Anyone who opens the address can watch; managing still takes an account."),
      check("server.dlna", "dlna", view.dlna, "DLNA for TVs", "TVs on the local network find the library by themselves; DLNA has no login. Takes a restart."),
      check("server.no_tags", "tags", view.tags, "Write tags into downloaded files", "Title, year and poster inside the file, for other players.")),
    h("div", { class: "panel glass" },
      h("h2", { style: "margin-top:0" }, "Conversion"),
      field("Hardware conversion", hw, lockedNote(view.locked, "server.hwaccel") || `In use now: ${view.hwInUse}. Without a card that works, the processor converts.`),
      field("Folder of screenshots and stills", pathInput(text("server.cache", "cache", view.cache, { placeholder: view.cacheDir, spellcheck: false })),
        lockedNote(view.locked, "server.cache") || `Now: ${view.cacheDir}. Empty: .cache in the first library folder. Moving it is harmless: the images are made again.`)),
    h("div", { class: "panel glass" },
      h("h2", { style: "margin-top:0" }, "Database"),
      field("Database file", pathInput(text("server.database", "database", view.database, { spellcheck: false }), "mediakeeper.db"),
        lockedNote(view.locked, "server.database") || "The settings, accounts, watch progress and history. Moved at once when saved; the old file is kept next to it as .old." + dockerNote(view))),
    saveRow(() => change, save, render));
}

// usersPanel lists the accounts: who watches and who manages.
async function usersPanel(body) {
  const error = h("p", { class: "error" });
  const name = h("input", { type: "text", placeholder: "Name", "aria-label": "Name", autocomplete: "off" });
  const pass = h("input", { type: "password", placeholder: "Password", "aria-label": "Password", autocomplete: "new-password" });
  const admin = h("input", { type: "checkbox", id: "admin" });
  const again = () => usersPanel(body);
  const save = async user => {
    error.textContent = "";
    try { await api("users", { json: user }); again(); } catch (err) { error.textContent = err.message; }
  };
  let users = [];
  try { users = await api("users"); } catch (err) { error.textContent = err.message; }
  body.replaceChildren(
    h("div", { class: "panel glass" },
      h("table", {},
        h("tr", {}, h("th", {}, "User"), h("th", {}, "Role"), h("th", {})),
        users.map(u => h("tr", {},
          h("td", {}, u.name, u.id === me.id && h("span", { class: "dim" }, " (you)")),
          h("td", {}, u.admin ? "Administrator: downloads and manages" : "Viewer: watches only"),
          h("td", {}, h("div", { class: "row" },
            h("button", { class: "small", onclick: () => { const p = prompt(`New password for ${u.name}`); if (p) save({ name: u.name, password: p, admin: u.admin }); } }, "Change password"),
            u.id !== me.id && h("button", { class: "small", onclick: () => save({ name: u.name, password: "", admin: !u.admin }) }, u.admin ? "Make a viewer" : "Make an administrator"),
            u.id !== me.id && h("button", {
              class: "small danger",
              onclick: async () => { if (confirm(`Delete ${u.name}?`)) { try { await api("users/" + u.id, { method: "DELETE" }); again(); } catch (err) { error.textContent = err.message; } } },
            }, "Delete"))))))),
    h("form", { class: "panel glass", onsubmit: e => { e.preventDefault(); save({ name: name.value, password: pass.value, admin: admin.checked }); } },
      h("h2", { style: "margin-top:0" }, "Add a user"),
      h("div", { class: "row" }, name, pass, h("label", { for: "admin" }, admin, " administrator"), h("button", { class: "primary" }, "Add")),
      h("p", { class: "dim", style: "margin-bottom:0" }, "The same name and password work in Jellyfin apps: add this server's address there."),
      error));
}

// ------------------------------------------------------------------ setup

// renderSetup is the first start: there is no account yet. Four steps: the
// administrator, the library folders, where the database and the images are
// kept, the name and the catalogue keys.
function renderSetup(state) {
  document.title = "MediaKeeper";
  const data = { user: "admin", password: "", repeat: "", roots: state.libraries || [],
    name: state.name || "MediaKeeper", language: state.language || "en-US", tmdbKey: "", omdbKey: "", kinopoiskKey: "",
    database: state.database || "", cache: state.cache || "" };
  const locked = state.locked || {};
  let step = 0;
  const error = h("p", { class: "error" });
  const card = h("form", { class: "setup glass", onsubmit: e => { e.preventDefault(); next(); } });
  const input = (name, attrs) => h("input", { value: data[name], ...attrs, oninput: e => { data[name] = e.target.value; } });
  const steps = [
    () => [h("h2", {}, "The administrator"),
      h("p", { class: "dim" }, "Welcome to MediaKeeper. First, the account that manages the server: downloads, descriptions, settings, other users."),
      field("Name", input("user", { type: "text", autocomplete: "username", required: true })),
      field("Password", input("password", { type: "password", autocomplete: "new-password", required: true, minLength: 4 })),
      field("The password again", input("repeat", { type: "password", autocomplete: "new-password", required: true }))],
    () => [h("h2", {}, "The library"),
      h("p", { class: "dim" }, "The folders of films and series on the server. You can add them later as well, under Settings."),
      lockedNote(locked, "libraries"),
      folderEditor(data.roots, !!locked.libraries)],
    () => [h("h2", {}, "Storage"),
      h("p", { class: "dim" }, "Where the server keeps its own files. The suggested places are fine for most; both can be changed later under Settings."),
      field("Database", pathInput(input("database", { type: "text", spellcheck: false, disabled: !!locked["server.database"] }), "mediakeeper.db"),
        lockedNote(locked, "server.database") || "The settings, accounts, watch progress and history: worth a backup." + dockerNote(state)),
      field("Screenshots and episode stills", pathInput(input("cache", { type: "text", spellcheck: false, placeholder: ".cache in the first library folder", disabled: !!locked["server.cache"] })),
        lockedNote(locked, "server.cache") || "Made from the videos, and made again if lost. Empty: .cache in the first library folder.")],
    () => [h("h2", {}, "Descriptions"),
      h("p", { class: "dim" }, "Posters, descriptions and ratings come from online catalogues. Without keys the free ones are used (TVMaze, Wikidata); keys can be added later under Settings."),
      field("Server name", input("name", { type: "text", required: true })),
      field("Language", input("language", { type: "text", placeholder: "en-US" }), "Of titles and descriptions, as en-US or ru-RU."),
      field("TMDB key", input("tmdbKey", { type: "password", autocomplete: "off" }), "themoviedb.org → Settings → API: the best source."),
      field("OMDb key", input("omdbKey", { type: "password", autocomplete: "off" }), "omdbapi.com: IMDb ratings."),
      field("Kinopoisk key", input("kinopoiskKey", { type: "password", autocomplete: "off" }), "kinopoiskapiunofficial.tech: Russian titles.")],
  ];
  const draw = () => {
    error.textContent = "";
    fill(card,
      h("div", { class: "setup-head" }, h("h1", {}, "MediaKeeper"),
        h("div", { class: "dots" }, steps.map((_, i) => h("span", { class: i === step ? "on" : i < step ? "done" : "" })))),
      steps[step](),
      error,
      h("div", { class: "row setup-buttons" },
        step > 0 && h("button", { type: "button", onclick: () => { step--; draw(); } }, "Back"),
        h("span", { class: "spacer" }),
        h("button", { class: "primary" }, step < steps.length - 1 ? "Next" : "Finish")));
    const first = card.querySelector("input");
    if (first) first.focus();
  };
  const next = async () => {
    if (step === 0 && data.password !== data.repeat) return (error.textContent = "The passwords differ.");
    if (step < steps.length - 1) { step++; return draw(); }
    const button = card.querySelector("button.primary");
    button.disabled = true;
    try {
      const body = { user: data.user.trim(), password: data.password, name: data.name, language: data.language };
      if (!locked.libraries) body.libraries = data.roots.map(({ path, kind }) => ({ path, kind }));
      if (!locked["server.database"] && data.database.trim() && data.database.trim() !== state.database) body.database = data.database.trim();
      if (!locked["server.cache"] && data.cache.trim()) body.cache = data.cache.trim();
      for (const k of ["tmdbKey", "omdbKey", "kinopoiskKey"]) if (data[k].trim()) body[k] = data[k].trim();
      me = await api("setup", { json: body });
      library = null;
      location.hash = data.roots.length ? "#movies" : "#settings/library";
      render();
    } catch (err) {
      error.textContent = err.message;
      button.disabled = false;
    }
  };
  app.replaceChildren(card);
  draw();
}
start();
