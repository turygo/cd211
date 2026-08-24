// downloads-live.js drives the authenticated conditional-poll live updates on
// the Downloads dashboard and detail pages. All durable state, labels,
// permissions, CSRF forms, and error text stay server-authoritative: the page
// modules fetch server-rendered fragments and reconcile the DOM by hash and
// durable row_version. Without JS the pages keep their native forms and
// navigation.
import { animateElement, motionTiming, reducedMotion } from "/static/motion.js?v=1";

const POLL_ACTIVE_MS = 2000;
const POLL_TERMINAL_MS = 10000;
const BACKOFF_STEPS = [2000, 4000, 8000, 16000, 30000];
const SEARCH_DEBOUNCE_MS = 250;
const VALID_VIEWS = ["active", "completed", "failed", "cancelled", "all"];

const listRegion = document.querySelector("[data-download-list]");
let detailRegion = document.querySelector("[data-live-detail]");
if (!listRegion && !detailRegion) {
  // Not a downloads page; nothing to enhance.
  throw new Error("downloads-live.js loaded without a download region");
}

const tbody = listRegion ? listRegion.querySelector("tbody") : null;
let tableWrap = listRegion ? listRegion.querySelector("[data-table-wrap]") : null;
let paginationNav = listRegion ? listRegion.querySelector("[data-pagination]") : null;
let emptyState = listRegion ? listRegion.querySelector("[data-empty-state]") : null;
const recordCount = listRegion ? listRegion.querySelector("[data-record-count]") : null;
const announcer = document.querySelector("[data-live-announcer]");
const filterForm = listRegion ? listRegion.querySelector("form.download-filters") : null;
const searchInput = filterForm ? filterForm.querySelector('input[name="q"]') : null;

let generation = 0;
let controller = null;
let pollTimer = null;
let lastCadence = POLL_TERMINAL_MS;
let failureStep = 0;
let inFlight = false;
let syncPending = false;
let lastETag = null;
let filterRequested = false;
let suppressEntry = false;
let searchTimer = null;
const exiting = new Set();

function wait(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function parseHTML(html) {
  const host = document.createElement("template");
  host.innerHTML = html;
  return host.content;
}

function parseRow(rowHTML) {
  // Row fragments are <tr> elements; a temporary <tbody> gives them a valid
  // table parsing context.
  const host = document.createElement("tbody");
  host.innerHTML = rowHTML;
  return host.firstElementChild;
}

function cssEscape(value) {
  return window.CSS && CSS.escape ? CSS.escape(value) : value;
}

function announce(message) {
  if (announcer && message) {
    announcer.textContent = message;
  }
}

function announceTerminal(root, item) {
  const nameElement = root.querySelector(".task-title, h1") || document.querySelector("h1");
  const name = nameElement ? nameElement.textContent.trim() : item.hash;
  const badge = root.querySelector(".state-badge, [data-live-badge]");
  const label = badge ? badge.textContent.trim() : "";
  announce(`${name} ${label}`.trim());
}

// ---------- Focus and scroll preservation ----------

function controlDescriptor(element) {
  const form = element.closest("form");
  const descriptor = { tag: element.tagName };
  if (element instanceof HTMLButtonElement || element instanceof HTMLInputElement || element instanceof HTMLAnchorElement) {
    descriptor.ariaLabel = element.getAttribute("aria-label") || "";
    descriptor.name = element.getAttribute("name") || "";
    descriptor.action = form ? form.getAttribute("action") : "";
  }
  if (element instanceof HTMLAnchorElement) {
    descriptor.href = element.getAttribute("href") || "";
  }
  return descriptor;
}

function findControl(root, descriptor) {
  if (!descriptor || !root) return null;
  const tag = descriptor.tag.toLowerCase();
  if (descriptor.action && (descriptor.ariaLabel || descriptor.name)) {
    const form = root.querySelector(`form[action="${cssEscape(descriptor.action)}"]`);
    if (form) {
      const target = form.querySelector(`${tag}[aria-label="${cssEscape(descriptor.ariaLabel)}"]`) ||
        form.querySelector(`${tag}[name="${cssEscape(descriptor.name)}"]`);
      if (target) return target;
    }
  }
  if (descriptor.ariaLabel) {
    const target = root.querySelector(`${tag}[aria-label="${cssEscape(descriptor.ariaLabel)}"]`);
    if (target) return target;
  }
  if (descriptor.name) {
    const target = root.querySelector(`${tag}[name="${cssEscape(descriptor.name)}"]`);
    if (target) return target;
  }
  if (descriptor.href) {
    return root.querySelector(`${tag}[href="${cssEscape(descriptor.href)}"]`);
  }
  return null;
}

function captureFocusState() {
  const active = document.activeElement;
  if (!active || active === document.body || active === document.documentElement) return null;
  const row = active.closest("tr[data-download-hash]");
  if (row) {
    return { kind: "row", hash: row.dataset.downloadHash, descriptor: controlDescriptor(active) };
  }
  const region = document.querySelector("[data-live-detail]");
  if (region && region.contains(active)) {
    return { kind: "detail", descriptor: controlDescriptor(active) };
  }
  return null;
}

function restoreFocusState(state) {
  if (!state) return;
  let target = null;
  if (state.kind === "row" && tbody) {
    const row = tbody.querySelector(`tr[data-download-hash="${cssEscape(state.hash)}"]`);
    target = row ? findControl(row, state.descriptor) : null;
  } else if (state.kind === "detail") {
    target = findControl(document.querySelector("[data-live-detail]"), state.descriptor);
  }
  if (target && typeof target.focus === "function") {
    target.focus({ preventScroll: true });
  }
}

function captureScroll() {
  return { x: window.scrollX, y: window.scrollY, left: tableWrap ? tableWrap.scrollLeft : 0 };
}

function restoreScroll(scroll) {
  if (!scroll) return;
  window.scrollTo(scroll.x, scroll.y);
  if (tableWrap) tableWrap.scrollLeft = scroll.left;
}

// ---------- Polling ----------

function currentUpdatesURL() {
  if (listRegion) {
    return "/downloads/updates" + window.location.search;
  }
  return `/downloads/${detailRegion.dataset.hash}/updates`;
}

function knownParams() {
  const params = new URLSearchParams();
  if (!tbody) return params;
  for (const tr of tbody.querySelectorAll("tr[data-download-hash]")) {
    if (params.size >= 25) break;
    params.append("known", `${tr.dataset.downloadHash}:${tr.dataset.rowVersion}`);
  }
  return params;
}

function setRegionBusy(on) {
  if (!listRegion) return;
  if (on) listRegion.setAttribute("aria-busy", "true");
  else listRegion.removeAttribute("aria-busy");
}

function schedule(delay) {
  clearTimeout(pollTimer);
  if (document.hidden) return; // hidden tabs schedule nothing
  pollTimer = setTimeout(() => {
    pollTimer = null;
    if (document.hidden) return; // the visibility handler resumes on visible
    void sync();
  }, delay);
}

function handleSuccess() {
  failureStep = 0;
  if (syncPending) {
    syncPending = false;
    void sync();
    return;
  }
  schedule(lastCadence);
}

function handlePollError(status) {
  if (filterRequested) {
    // The URL already reflects the requested filter; the native page load
    // renders it (or the server error) without JS.
    window.location.reload();
    return;
  }
  if (status === 401 || status === 403) {
    window.location.reload();
    return;
  }
  if (detailRegion && status === 404) {
    // The download is no longer visible: authoritative disappearance.
    window.location.assign("/");
    return;
  }
  schedule(BACKOFF_STEPS[failureStep]);
  failureStep = Math.min(failureStep + 1, BACKOFF_STEPS.length - 1);
}

async function sync() {
  if (inFlight) {
    syncPending = true;
    return;
  }
  inFlight = true;
  const gen = generation;
  if (controller) controller.abort();
  controller = new AbortController();
  const url = currentUpdatesURL();
  const known = listRegion ? knownParams() : null;
  const requestURL = listRegion && known && known.size > 0
    ? `${url}${url.includes("?") ? "&" : "?"}${known.toString()}`
    : url;
  const headers = { Accept: "application/json" };
  if (lastETag) headers["If-None-Match"] = lastETag;
  let response;
  try {
    response = await fetch(requestURL, {
      credentials: "same-origin",
      headers,
      cache: "no-store",
      signal: controller.signal,
    });
  } catch (error) {
    inFlight = false;
    if (error && error.name === "AbortError") {
      if (syncPending) {
        syncPending = false;
        void sync();
      }
      return;
    }
    setRegionBusy(false);
    if (filterRequested) {
      window.location.reload();
      return;
    }
    schedule(BACKOFF_STEPS[failureStep]);
    failureStep = Math.min(failureStep + 1, BACKOFF_STEPS.length - 1);
    return;
  }
  inFlight = false;
  if (gen !== generation) {
    if (syncPending) {
      syncPending = false;
      void sync();
    }
    return;
  }
  if (response.redirected) {
    setRegionBusy(false);
    window.location.reload();
    return;
  }
  if (response.status === 304) {
    // No mutation, no animation.
    setRegionBusy(false);
    filterRequested = false;
    handleSuccess();
    return;
  }
  if (!response.ok) {
    setRegionBusy(false);
    handlePollError(response.status);
    return;
  }
  let payload;
  try {
    payload = await response.json();
  } catch {
    setRegionBusy(false);
    filterRequested = false;
    schedule(BACKOFF_STEPS[failureStep]);
    failureStep = Math.min(failureStep + 1, BACKOFF_STEPS.length - 1);
    return;
  }
  if (gen !== generation) return;
  lastETag = response.headers.get("ETag") || lastETag;
  if (listRegion) {
    applyListSnapshot(payload);
  } else {
    applyDetailSnapshot(payload);
  }
  handleSuccess();
}

document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    clearTimeout(pollTimer);
    pollTimer = null;
    return;
  }
  // Resume with an immediate sync.
  void sync();
});

window.addEventListener("online", () => {
  if (!document.hidden) {
    void sync();
  }
});

// ---------- Row reconciliation (list) ----------


function revealError(root) {
  const error = root.querySelector(".cell-error, [data-live-error]");
  if (!error) return;
  error.style.opacity = "0";
  animateElement(error, { opacity: [0, 1] }, { duration: motionTiming.fast });
}

function resolveAccent() {
  const probe = document.createElement("div");
  probe.style.boxShadow = "inset 3px 0 0 var(--accent)";
  document.body.append(probe);
  const resolved = getComputedStyle(probe).boxShadow;
  probe.remove();
  const match = /^inset 3px 0 0 (.+)$/.exec(resolved);
  return match ? match[1].trim() : "rgb(94, 106, 210)";
}

function animateEntry(row) {
  // One combined entrance: the new row slides in from 12px above while a
  // restrained indigo left edge emphasis settles.
  animateElement(
    row,
    {
      transform: ["translateY(-12px)", "none"],
      opacity: [0, 1],
      boxShadow: [`inset 3px 0 0 ${resolveAccent()}`, "inset 3px 0 0 transparent"],
    },
    { duration: motionTiming.standard }
  );
}

function replaceRow(tr, item) {
  const oldState = tr.dataset.state || "";
  const hadError = tr.querySelector(".cell-error") !== null;
  const openDialog = tr.querySelector("dialog[open]");
  const next = parseRow(item.html);
  if (!next || !next.dataset) return tr;
  // Preserve the live top-layer dialog in the replacement fragment's exact
  // slot. Appending it would leave the server-rendered closed dialog behind,
  // duplicate its IDs, and make focus restoration target the wrong node.
  if (openDialog) {
    const replacementDialog = next.querySelector(`dialog#${cssEscape(openDialog.id)}`);
    if (replacementDialog) replacementDialog.replaceWith(openDialog);
  }
  tr.replaceWith(next);
  const hasError = next.querySelector(".cell-error") !== null;
  if (hasError && !hadError) revealError(next);
  const view = listRegion ? listRegion.dataset.view : "";
  if (item.state === "COMPLETED" && oldState !== "COMPLETED" && view !== "completed") {
    announceTerminal(next, item);
    void runCompletionVisuals(next, item);
  } else if (item.state === "FAILED" && oldState !== "FAILED") {
    announceTerminal(next, item);
  }
  // A row replaced while its delete dialog is open keeps that same live
  // dialog; polling never closes it or duplicates its IDs.
  return next;
}

function reconcileRows(incoming, exitedHashes) {
  if (!tbody) return false;
  let mutated = false;
  const incomingHashes = new Set(incoming.map((item) => item.hash));
  const oldRows = Array.from(tbody.querySelectorAll("tr[data-download-hash]"));
  const oldByHash = new Map(oldRows.map((tr) => [tr.dataset.downloadHash, tr]));

  // Authoritative removal pass: rows the snapshot no longer contains leave
  // the view, except rows owned by an exit sequence or terminal confirmation.
  for (const tr of oldRows) {
    const hash = tr.dataset.downloadHash;
    if (incomingHashes.has(hash) || exitedHashes.has(hash) || exiting.has(hash)) continue;
    removeRowPlain(tr);
    oldByHash.delete(hash);
    mutated = true;
  }

  // Update/insert pass in authoritative array order. Unchanged rows are
  // reused untouched; only versions that rose are replaced.
  let prevNode = null;
  let nextOld = tbody.querySelector("tr[data-download-hash]");
  for (const item of incoming) {
    let tr = oldByHash.get(item.hash);
    if (tr && Number(tr.dataset.rowVersion) >= item.row_version) {
      // Reuse the untouched node; equal or lower versions never mutate.
    } else if (tr) {
      tr = replaceRow(tr, item);
      oldByHash.set(item.hash, tr);
      mutated = true;
    } else {
      tr = parseRow(item.html);
      if (!tr || !tr.dataset) continue;
      if (!suppressEntry) animateEntry(tr);
      oldByHash.set(item.hash, tr);
      mutated = true;
    }
    if (tr === nextOld) {
      nextOld = nextOld.nextElementSibling;
    } else {
      if (prevNode) prevNode.after(tr);
      else tbody.prepend(tr);
    }
    prevNode = tr;
  }
  return mutated;
}

function removeRowPlain(tr) {
  const hash = tr.dataset.downloadHash;
  if (exiting.has(hash)) return;
  exiting.add(hash);
  const dialog = tr.querySelector("dialog[open]");
  if (dialog) dialog.close();
  void (async () => {
    if (!reducedMotion.matches) {
      tr.style.height = `${tr.offsetHeight}px`;
      tr.classList.add("is-collapsing");
      await animateElement(
        tr,
        { opacity: [1, 0], height: [tr.style.height, "0px"], transform: ["none", "translateY(-4px)"] },
        { duration: motionTiming.standard }
      );
    }
    tr.remove();
    exiting.delete(hash);
  })();
}

// ---------- Completion and terminal exits ----------

async function runCompletionVisuals(root, item) {
  const badge = root.querySelector(".state-badge") || document.querySelector("[data-live-badge]");
  if (badge) {
    const check = document.createElement("span");
    check.className = "badge-check";
    check.setAttribute("aria-hidden", "true");
    check.textContent = "✓";
    badge.append(check);
    await animateElement(
      check,
      { opacity: [0, 1], transform: ["scale(0.6)", "scale(1)"] },
      { duration: motionTiming.standard }
    );
    await wait(motionTiming.completionHold);
    check.remove();
  } else {
    await wait(motionTiming.completionHold);
  }
}

async function collapseRow(row) {
  if (reducedMotion.matches) return;
  const height = row.offsetHeight;
  row.style.height = `${height}px`;
  row.classList.add("is-collapsing");
  await animateElement(
    row,
    { opacity: [1, 0], height: [row.style.height, "0px"], transform: ["none", "translateY(-4px)"] },
    { duration: motionTiming.standard }
  );
}

async function runExitSequence(row, item) {
  const hash = item.hash;
  if (exiting.has(hash)) return;
  exiting.add(hash);
  row.setAttribute("aria-busy", "true");
  announceTerminal(row, item);
  try {
    if (item.state === "COMPLETED") {
      await runCompletionVisuals(row, item);
    } else {
      // FAILED / CANCELLED: a short server-rendered status/error confirmation
      // before the authoritative exit. The Retry entry stays interactive only
      // in member views (All/Failed); the row is exiting here.
      const error = row.querySelector(".cell-error");
      if (error) revealError(row);
      await wait(motionTiming.standard + motionTiming.fast);
    }
    await collapseRow(row);
  } finally {
    row.remove();
    exiting.delete(hash);
  }
}

function processExited(exited) {
  if (!Array.isArray(exited) || !tbody) return false;
  let handled = false;
  for (const item of exited) {
    const tr = tbody.querySelector(`tr[data-download-hash="${cssEscape(item.hash)}"]`);
    if (!tr || exiting.has(item.hash)) continue;
    if (Number(tr.dataset.rowVersion) > item.row_version) continue; // never regress
    // The row was already terminal when the client last rendered it (for
    // example it left the view because the filter changed, not because it
    // just completed): plain authoritative exit, no confirmation replay.
    const currentState = tr.dataset.state || "";
    if (currentState === "COMPLETED" || currentState === "FAILED" || currentState === "CANCELLED") {
      removeRowPlain(tr);
      continue;
    }
    handled = true;
    const terminal = parseRow(item.html);
    const row = terminal && terminal.dataset ? terminal : tr;
    if (terminal && terminal.dataset) {
      const focus = captureFocusState();
      const scroll = captureScroll();
      tr.replaceWith(terminal);
      restoreFocusState(focus);
      restoreScroll(scroll);
    }
    void runExitSequence(row, item);
  }
  return handled;
}

// ---------- Snapshot application (list + detail) ----------

function updatePaginationAndEmpty(payload) {
  const hasRows = Array.isArray(payload.rows) && payload.rows.length > 0;
  if (tableWrap) tableWrap.hidden = !hasRows;
  if (paginationNav) paginationNav.hidden = !hasRows;
  if (emptyState) emptyState.hidden = hasRows;
  if (hasRows && payload.pagination_html) {
    const nav = parseHTML(payload.pagination_html).querySelector("nav.pagination");
    if (nav) {
      paginationNav.replaceWith(nav);
      paginationNav = nav;
      paginationNav.hidden = false;
    }
  }
  if (!hasRows && payload.empty_html) {
    const empty = parseHTML(payload.empty_html).querySelector("section.empty-state");
    if (empty) {
      emptyState.replaceWith(empty);
      emptyState = empty;
      emptyState.hidden = false;
    }
  }
  if (listRegion && payload.page_number) {
    listRegion.dataset.page = String(payload.page_number);
  }
}

function updateRecordCount(totalRows) {
  if (!recordCount) return;
  const format = recordCount.dataset.format || "%d shown";
  recordCount.textContent = format.replace("%d", String(totalRows));
}

function applyListSnapshot(payload) {
  const exited = Array.isArray(payload.rows_exited) ? payload.rows_exited : [];
  const exitedHashes = new Set(exited.map((item) => item.hash));
  const focus = captureFocusState();
  const scroll = captureScroll();
  const mutated = reconcileRows(payload.rows || [], exitedHashes);
  const handledExits = processExited(exited);
  updatePaginationAndEmpty(payload);
  updateRecordCount(payload.total_rows);
  if (focus || mutated || handledExits) {
    restoreFocusState(focus);
    restoreScroll(scroll);
  }
  setRegionBusy(false);
  filterRequested = false;
  suppressEntry = false;
  lastCadence = payload.has_active ? POLL_ACTIVE_MS : POLL_TERMINAL_MS;
}

function applyDetailSnapshot(payload) {
  if (!detailRegion) return;
  if (payload.row_version <= Number(detailRegion.dataset.rowVersion)) return; // never regress
  const focus = captureFocusState();
  const oldState = detailRegion.dataset.state || "";
  const holder = document.createElement("div");
  holder.innerHTML = payload.html;
  const next = holder.querySelector("[data-live-detail]");
  if (!next) return;
  detailRegion.replaceWith(next);
  detailRegion = next;
  const badge = document.querySelector("[data-live-badge]");
  if (badge && next.dataset.stateLabel) {
    badge.dataset.state = next.dataset.state;
    badge.textContent = next.dataset.stateLabel;
  }
  if (payload.state === "COMPLETED" && oldState !== "COMPLETED") {
    announceTerminal(next, payload);
    void runCompletionVisuals(next, payload);
  } else if (payload.state === "FAILED" && oldState !== "FAILED") {
    revealError(next);
    announceTerminal(next, payload);
  }
  restoreFocusState(focus);
  lastCadence = payload.terminal ? POLL_TERMINAL_MS : POLL_ACTIVE_MS;
}

// ---------- Filters, search, pagination, history ----------

function navigateToFilter(params, resetPage) {
  if (resetPage) params.delete("page");
  const query = params.toString();
  const targetURL = "/" + (query ? `?${query}` : "");
  const same = targetURL === window.location.pathname + window.location.search;
  if (!same) {
    history.pushState({ filter: query }, "", targetURL);
  }
  const view = params.get("view") || "all";
  if (listRegion && VALID_VIEWS.includes(view)) listRegion.dataset.view = view;
  if (filterForm) syncFormFromLocation(params);
  generation++;
  if (controller) controller.abort();
  filterRequested = true;
  suppressEntry = true;
  setRegionBusy(true);
  void sync();
}

function syncFormFromLocation(params) {
  if (!filterForm) return;
  const view = filterForm.querySelector('select[name="view"]');
  const category = filterForm.querySelector('select[name="category"]');
  const q = filterForm.querySelector('input[name="q"]');
  const viewValue = params.get("view") || "all";
  if (view && VALID_VIEWS.includes(viewValue)) view.value = viewValue;
  if (category) category.value = params.get("category") || "";
  if (q) q.value = params.get("q") || "";
}

if (filterForm) {
  filterForm.addEventListener("submit", (event) => {
    event.preventDefault();
    clearTimeout(searchTimer);
    navigateToFilter(new URLSearchParams(new FormData(filterForm)), true);
  });
}

if (searchInput) {
  searchInput.addEventListener("input", () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      if (!filterForm) return;
      const params = new URLSearchParams(new FormData(filterForm));
      params.delete("page");
      navigateToFilter(params, false);
    }, SEARCH_DEBOUNCE_MS);
  });
}

if (listRegion) {
  listRegion.addEventListener("click", (event) => {
    const link = event.target.closest("a.pagination-link[href]");
    if (!link) return;
    event.preventDefault();
    const url = new URL(link.href, window.location.origin);
    if (url.pathname + url.search === window.location.pathname + window.location.search) {
      void sync();
      return;
    }
    navigateToFilter(new URLSearchParams(url.search), false);
  });
}

window.addEventListener("popstate", () => {
  if (!listRegion) return;
  const params = new URLSearchParams(window.location.search);
  generation++;
  if (controller) controller.abort();
  filterRequested = true;
  suppressEntry = true;
  setRegionBusy(true);
  if (filterForm) syncFormFromLocation(params);
  const view = params.get("view") || "all";
  if (VALID_VIEWS.includes(view)) listRegion.dataset.view = view;
  void sync();
});

// ---------- Progressive action enhancement (pause/resume/retry/remove) ----------

// Forms present at load already carry app.js's data-confirm listener; tag them
// so replaced fragments (which arrive later) can run their own confirmation.
if (listRegion || detailRegion) {
  const host = listRegion || detailRegion;
  for (const form of host.querySelectorAll("form[data-confirm]")) {
    form.dataset.confirmWired = "true";
  }

  // app.js binds dialog openers/closers/backdrops only to the initial DOM
  // nodes. Fragments inserted by live reconciliation need their own native
  // dialog wiring; initial dialogs keep app.js's animated close paths.
  for (const element of host.querySelectorAll("[data-dialog-open], [data-dialog-close], dialog.delete-dialog")) {
    element.dataset.dialogWired = "true";
  }

  host.addEventListener("click", (event) => {
    const opener = event.target.closest("[data-dialog-open]");
    if (opener && opener.dataset.dialogWired !== "true") {
      const id = opener.getAttribute("data-dialog-open");
      const dialog = id ? host.querySelector(`#${cssEscape(id)}`) : null;
      if (dialog instanceof HTMLDialogElement && !dialog.open) {
        dialog.showModal();
      }
      return;
    }
    const closer = event.target.closest("[data-dialog-close]");
    if (closer && closer.dataset.dialogWired !== "true") {
      const dialog = closer.closest("dialog");
      if (dialog instanceof HTMLDialogElement && dialog.open) {
        dialog.close();
      }
      return;
    }
    // Backdrop click: the native dialog element is the click target outside
    // its own box. app.js's animated backdrop close stays with wired dialogs.
    const dialog = event.target.closest("dialog.delete-dialog");
    if (dialog && dialog.dataset.dialogWired !== "true" && dialog.open) {
      const bounds = dialog.getBoundingClientRect();
      const outside =
        event.clientX < bounds.left || event.clientX > bounds.right ||
        event.clientY < bounds.top || event.clientY > bounds.bottom;
      if (outside) {
        dialog.close();
      }
    }
  });

  host.addEventListener("submit", (event) => {
    const form = event.target.closest("form[action*='/downloads/']");
    if (!form) return;
    const action = form.getAttribute("action") || "";
    if (!/^\/downloads\/[0-9a-f]{40}\/(pause|start|retry|remove)$/.test(action)) return;
    if (event.defaultPrevented) return; // a data-confirm dialog was cancelled
    if (form.dataset.nativeFallback === "true") return; // let the native submit proceed
    if (form.dataset.submitting === "true") return; // duplicate prevention
    event.preventDefault();
    if (form.hasAttribute("data-confirm") && form.dataset.confirmWired !== "true") {
      const message = form.getAttribute("data-confirm");
      if (message && !window.confirm(message)) {
        return;
      }
    }
    void submitAction(form);
  });
}

function clearBusy(form, submitter) {
  form.dataset.submitting = "false";
  if (submitter) {
    submitter.disabled = false;
    submitter.classList.remove("is-busy");
  }
}

function closeActionDialog(form) {
  const dialog = form.closest("dialog");
  if (dialog && dialog.open) dialog.close();
}

async function submitAction(form) {
  const submitter = form.querySelector('button[type="submit"]');
  if (submitter) {
    submitter.disabled = true;
    submitter.classList.add("is-busy");
  }
  form.dataset.submitting = "true";
  const returnInput = form.querySelector('input[name="return_to"]');
  if (returnInput) {
    returnInput.value = window.location.pathname + window.location.search;
  }
  try {
    const body = new URLSearchParams();
    for (const [name, value] of new FormData(form)) {
      if (typeof value === "string") body.append(name, value);
    }
    const response = await fetch(form.getAttribute("action"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
    });
    if (response.redirected || response.ok) {
      // The mutation committed (the POST endpoints redirect on success); the
      // authoritative state arrives with the next sync. Never fabricate a
      // durable success locally.
      closeActionDialog(form);
      clearBusy(form, submitter);
      generation++;
      if (controller) controller.abort();
      void sync();
      return;
    }
    // Definitive server error: resubmit natively so the server-rendered
    // error page is shown.
    clearBusy(form, submitter);
    form.dataset.nativeFallback = "true";
    form.requestSubmit(submitter || undefined);
  } catch {
    // Network-ambiguous: never blindly double-submit. Re-enable the form and
    // reconcile authoritative state instead.
    clearBusy(form, submitter);
    generation++;
    if (controller) controller.abort();
    void sync();
  }
}

// ---------- Start ----------

void sync();
