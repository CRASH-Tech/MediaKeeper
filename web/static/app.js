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
  try { me = await api("me"); } catch { me = null; }
  render();
}

async function render() {
  clearInterval(pollTimer);
  document.querySelectorAll(".modal").forEach(m => m.remove());
  const [path, query] = (location.hash.slice(1) || "movies").split("?");
  const [page, arg, ...rest] = path.split("/");
  // Without an account one may watch (if the server allows it), not manage.
  if (!me || (page === "login" && me.guest)) return renderLogin();
  try {
    if (!library || ["movies", "shows", "movie", "show", "browse"].includes(page)) library = await api("library");
  } catch (err) {
    if (!me) return;
    return shell(page, h("p", { class: "error" }, err.message));
  }
  document.title = library.name;
  switch (page) {
    case "shows": return backAtTitle(renderList("shows", library.shows.map(asShow), query));
    case "movie": return renderMovie(arg);
    case "show": return renderShow(arg);
    case "browse": return backAtTitle(renderBrowse(arg, decodeURIComponent(rest.join("/"))));
    case "downloads": return me.admin ? renderDownloads() : (location.hash = "#movies");
    case "users": return me.admin ? renderUsers() : (location.hash = "#movies");
    default: return backAtTitle(renderList("movies", library.movies, query));
  }
}

// Line icons for the navigation, 24×24, drawn with the current text colour.
const icons = {
  movies: '<svg viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="14" rx="3"/><path d="M3 9h18M8 5l-1 4M13 5l-1 4M18 5l-1 4"/></svg>',
  shows: '<svg viewBox="0 0 24 24"><rect x="3" y="6" width="18" height="12" rx="3"/><path d="M8 21h8M9 2l3 4 3-4"/></svg>',
  downloads: '<svg viewBox="0 0 24 24"><path d="M12 4v11M7 10l5 5 5-5M5 19h14"/></svg>',
  users: '<svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="4"/><path d="M4 20c1.5-4 4.5-6 8-6s6.5 2 8 6"/></svg>',
  play: '<svg viewBox="0 0 24 24"><path d="M8 5.5v13l11-6.5z" fill="currentColor" stroke="none"/></svg>',
  fullscreen: '<svg viewBox="0 0 24 24"><path d="M4 9V4h5M15 4h5v5M20 15v5h-5M9 20H4v-5"/></svg>',
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

function shell(page, ...content) {
  const link = (id, label, extra) => h("a", { href: "#" + id, class: page === id ? "active" : "", onclick: () => { freshVisit = true; } },
    icon(id), h("span", { class: "label" }, label), extra);
  setAmbient(pendingAmbient);
  pendingAmbient = null;
  // The sections: in the header on a wide screen, in a floating tab bar at
  // the bottom on a phone.
  const sections = cls => h("nav", { class: cls },
    link("movies", "Movies"), link("shows", "Shows"),
    me.admin && link("downloads", "Downloads", h("span", { class: "badge hidden attention" })),
    me.admin && link("users", "Users"));
  const search = h("input", {
    type: "search", placeholder: "Search", "aria-label": "Search",
    oninput: () => {
      const term = search.value.trim().toLowerCase();
      for (const card of document.querySelectorAll(".card"))
        card.classList.toggle("hidden", !!term && !card.dataset.text.includes(term));
    },
  });
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
    return h("div", { class: "empty" }, emptyText || "Nothing here yet.",
      me.admin && !emptyText && h("p", {}, h("a", { href: "#downloads" }, "Download something")));
  }
  return h("div", { class: "grid" }, items.map(x => {
    const watched = isShow(x) ? episodesOf(x).every(e => e.played) : x.played;
    const card = h("a", { class: "card", href: `#${isShow(x) ? "show" : "movie"}/${x.id}`, onclick: () => openedFrom(x) },
      h("div", { class: "poster", style: x.poster ? `background-image:${image(x.id, "poster")}` : "" },
        !x.poster && x.title, watched && h("span", { class: "seen", title: "Watched" }, "✓"), !isShow(x) && progressBar(x)),
      h("div", { class: "title" }, x.title),
      h("div", { class: "sub" }, [!sameText(x.localTitle, x.title) && x.localTitle, x.year || null, isShow(x) && "series"].filter(Boolean).join(" · ")));
    card.dataset.text = `${x.title} ${x.localTitle || ""} ${x.originalTitle || ""} ${x.year || ""}`.toLowerCase();
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
  const sorters = {
    title: (a, b) => a.title.localeCompare(b.title),
    year: (a, b) => (b.year || 0) - (a.year || 0) || a.title.localeCompare(b.title),
    added: (a, b) => (b.added || 0) - (a.added || 0),
    rating: (a, b) => (b.rating || 0) - (a.rating || 0),
  };
  shown.sort(sorters[order] || sorters.title);

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
  const sort = h("select", { "aria-label": "Order", class: "sort", onchange: () => go("sort", sort.value === "title" ? "" : sort.value) },
    [["title", "By title"], ["year", "Newest first"], ["added", "Recently added"], ["rating", "Best rated"]]
      .map(([v, label]) => h("option", { value: v, selected: v === order }, label)));
  shell(page,
    items.length > 0 && filterBar(selects, sort, chosen.length, "#" + page),
    grid(shown, chosen.length ? "No titles match all of these." : undefined));
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
};
const browseLink = (facet, value) => `#browse/${facet}/${encodeURIComponent(value)}`;
const chip = (facet, value, note) => h("a", { class: "chip", href: browseLink(facet, value) }, value, note && h("span", { class: "note" }, note));

function renderBrowse(facet, value) {
  const f = facets[facet];
  if (!f) return (location.hash = "#movies");
  const all = [...library.movies, ...library.shows.map(asShow)];
  const found = all.filter(x => (f.values(x) || []).some(v => sameText(v, value)));
  shell("browse",
    h("p", { class: "crumbs" }, h("a", { href: "#movies" }, "Library"), " › ", f.label),
    h("h1", {}, value),
    h("p", { class: "dim" }, `${found.length} title(s) in the library`),
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
        ...rest)));
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
function identifyForm({ unit, search, resolve, st, extra, fill, ask }) {
  Object.assign(st, { query: "", picked: null, candidates: null, asMovie: true, season: 1, episode: 1, ref: "", ...st });
  const holder = h("div", {});
  const error = h("p", { class: "error" });
  const pickedSeries = () => !fill && st.picked && st.picked.kind === "tv" && unit.kind === "movie";
  const submit = async (body, candidate) => {
    error.textContent = "";
    holder.classList.add("busy");
    try { await resolve(body, candidate); } catch (err) { error.textContent = err.message; }
    holder.classList.remove("busy");
  };

  async function find() {
    holder.replaceChildren(h("p", { class: "dim" }, "Searching…"));
    try { st.candidates = await search(st.query); }
    catch (err) { st.candidates = []; error.textContent = err.message; }
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
  if (st.candidates) show(); else find();
  return h("div", { class: "unit" },
    h("div", {}, h("b", {}, { tv: "Series: ", movie: "Movie: " }[unit.kind] || ""), unit.title || "?", unit.year ? ` (${unit.year})` : ""),
    unit.files && h("div", { class: "files" }, unit.files.join(", ")),
    h("div", { class: "row", style: "margin-top:10px" }, query, h("button", { type: "button", onclick: find }, "Search")),
    holder,
    h("div", { class: "row", style: "margin-top:10px" }, ref,
      h("button", { type: "button", onclick: () => st.ref.trim() && submit({ ref: st.ref, asMovie: unit.kind === "movie" }) }, fill && !ask ? "Fill in by ID" : "Set by ID"),
      extra),
    error);
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
  const field = (label, input) => h("label", { class: "field" }, h("span", {}, label), input);
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
        f.tagline.value = m.tagline || ""; f.plot.value = m.plot || "";
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
                  countries: textList(f.countries.value),
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

  function imagesTab() {
    // card is one picture: what it is now, and a field to replace it.
    const card = (part, cls, label, note, src, has) => {
      const preview = h("div", { class: "art " + cls, style: has ? `background-image:url("${src}?t=${Date.now()}")` : "" });
      const input = h("input", { type: "file", accept: "image/jpeg,image/png", "aria-label": label, onchange: () => upload(input, `${x.id}/${part}`, preview, src) });
      return h("div", { class: "art-card" }, preview, h("b", {}, label), note && h("span", { class: "dim" }, note), input);
    };
    const main = h("div", { class: "art-cards" },
      card("poster", "poster", "Poster", "A tall picture, JPEG or PNG.", `/api/image/${x.id}/poster`, x.poster),
      card("backdrop", "backdrop", "Backdrop", "A wide picture behind the title.", `/api/image/${x.id}/backdrop`, x.backdrop));
    if (!movie) return h("div", {}, main,
      h("h3", { class: "sub" }, "Season posters"),
      h("div", { class: "art-cards seasons" }, x.seasons.map(s => card(`season${String(s.number).padStart(2, "0")}`, "poster",
        s.number ? `Season ${s.number}` : "Specials", "", `/api/image/${s.id}/poster`, s.poster))));
    return h("div", {}, main,
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
  const mode = h("button", { onclick: () => converted ? direct(position()) : convert(position()) });
  const box = h("div", { class: "player" },
    h("div", { class: "top glass" },
      h("button", { onclick: close }, "← Back"),
      h("span", { class: "name" }, item.show ? `${item.show} · S${item.season}E${item.episode} · ${item.title}` : fullTitle(item)),
      audioSelect, info.canTranscode && mode),
    note, video, seek);

  const position = () => (converted ? offset : 0) + (video.currentTime || 0);
  const say = text => { note.textContent = text || ""; note.classList.toggle("hidden", !text); };
  const endSession = () => {
    if (session) api("hls/s/" + session, { method: "DELETE" }).catch(() => {});
    session = null;
  };

  function subtitles(shift) {
    video.querySelectorAll("track").forEach(t => t.remove());
    for (let i = 0; i < info.subtitles; i++)
      video.append(h("track", { kind: "subtitles", label: `Subtitles ${i + 1}`, src: `/api/subs/${info.id}/${i}.vtt?offset=${shift}`, default: i === 0 }));
  }
  function direct(at) {
    endSession();
    converted = false; offset = 0; say("");
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
        const started = await api(`hls/start/${info.id}`, { json: { start: from, audio } });
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
      video.src = `/api/transcode/${info.id}?start=${offset}&audio=${audio}`;
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
  async function report(at) {
    lastReport = Date.now();
    if (me.guest) return; // nothing is remembered without an account
    try { await api("progress/" + info.id, { json: { position: at } }); } catch { /* next time */ }
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
    (at > 0 ? report(at) : Promise.resolve()).then(render);
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
        result = await api("downloads", { method: "POST", body: form });
      } else if (source.value.trim()) {
        result = await api("downloads", { json: { source: source.value } });
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
    return h("div", { class: "panel glass download" },
      h("div", { class: "head" },
        h("span", { class: "name" }, d.name),
        h("span", { class: "state " + d.state }, stateNames[d.state] || d.state),
        h("button", { class: "small danger", onclick: () => confirm(d.state === "downloading" ? "Stop this download?" : "Remove this entry?") && alerting("", { method: "DELETE" }) },
          d.state === "downloading" ? "Stop" : "Remove")),
      d.state === "downloading" && [
        h("progress", { value: d.done, max: d.total || 1 }),
        h("div", { class: "dim" }, d.total ? `${bytes(d.done)} of ${bytes(d.total)} · ${bytes(d.speed)}/s` : "Connecting…")],
      presetBox,
      d.error && h("p", { class: "error" }, d.error),
      titles,
      d.log && h("details", { class: "log-box", open: d.state !== "done" }, h("summary", { class: "dim" }, "What was done"), h("pre", { class: "log" }, d.log)),
      (d.pending || []).map(u => identifyForm({
        unit: u, st: open[d.id + "/" + u.key] = open[d.id + "/" + u.key] || {},
        search: q => api(`downloads/${d.id}/search?key=${encodeURIComponent(u.key)}&q=${encodeURIComponent(q)}`),
        resolve: body => act("/resolve", { json: { key: u.key, ...body } }),
        extra: h("button", { type: "button", class: "danger", onclick: () => confirm("Delete these files?") && alerting("/discard", { json: { key: u.key } }) }, "Delete the files"),
      })));
  }

  shell("downloads",
    h("form", { class: "panel glass form", onsubmit: add },
      h("div", { class: "row" }, source, h("button", { class: "primary" }, "Download")),
      h("div", { class: "row", style: "margin-top:10px" }, h("span", { class: "dim" }, "or a .torrent file:"), file),
      h("p", { class: "dim", style: "margin-bottom:0" }, "After downloading, the file is identified, renamed and put into Movies or Shows. If the program is not sure, it asks here; while it downloads, you can also say what it is."),
      error),
    list);
  const refresh = async () => { try { draw(await api("downloads")); } catch { /* shown on the next tick */ } };
  await refresh();
  pollTimer = setInterval(refresh, 2000);
}

// ------------------------------------------------------------------ users

async function renderUsers() {
  const error = h("p", { class: "error" });
  const name = h("input", { type: "text", placeholder: "Name", "aria-label": "Name", autocomplete: "off" });
  const pass = h("input", { type: "password", placeholder: "Password", "aria-label": "Password", autocomplete: "new-password" });
  const admin = h("input", { type: "checkbox", id: "admin" });
  const save = async body => {
    error.textContent = "";
    try { await api("users", { json: body }); renderUsers(); } catch (err) { error.textContent = err.message; }
  };
  let users = [];
  try { users = await api("users"); } catch (err) { error.textContent = err.message; }
  shell("users",
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
              onclick: async () => { if (confirm(`Delete ${u.name}?`)) { try { await api("users/" + u.id, { method: "DELETE" }); renderUsers(); } catch (err) { error.textContent = err.message; } } },
            }, "Delete"))))))),
    h("form", { class: "panel glass", onsubmit: e => { e.preventDefault(); save({ name: name.value, password: pass.value, admin: admin.checked }); } },
      h("h2", { style: "margin-top:0" }, "Add a user"),
      h("div", { class: "row" }, name, pass, h("label", { for: "admin" }, admin, " administrator"), h("button", { class: "primary" }, "Add")),
      h("p", { class: "dim", style: "margin-bottom:0" }, "The same name and password work in Jellyfin apps: add this server's address there."),
      error));
}

start();
