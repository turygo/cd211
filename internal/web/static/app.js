import { animateElement, motionTiming, staggerDelay } from "/static/motion.js?v=1";
const localDateTimeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

// Server-rendered timestamps carry an ISO value for machine-readable
// semantics; the browser renders them in the operator's local timezone and
// locale. A mutation observer keeps live download fragments localized too.
function formatLocalTimes(root = document) {
  const elements = [];
  if (root instanceof Element && root.matches("[data-local-time]")) {
    elements.push(root);
  }
  if (root.querySelectorAll) {
    elements.push(...root.querySelectorAll("[data-local-time]"));
  }
  for (const element of elements) {
    const value = element.getAttribute("datetime");
    if (!value || element.dataset.localTimeValue === value) continue;
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) continue;
    element.textContent = localDateTimeFormatter.format(date);
    element.dataset.localTimeValue = value;
  }
}

formatLocalTimes();
if (document.body) {
  const localTimeObserver = new MutationObserver((mutations) => {
    for (const mutation of mutations) {
      for (const node of mutation.addedNodes) {
        if (node.nodeType === Node.ELEMENT_NODE) {
          formatLocalTimes(node);
        }
      }
    }
  });
  localTimeObserver.observe(document.body, { childList: true, subtree: true });
}

const themeStorageKey = "cd211-theme";
let savedTheme = document.documentElement.dataset.theme === "dark" ? "dark" : "light";

// Reveals a set of elements with a restrained fade and at most 4px rise. A
// tiny stagger applies only to short lists and is always capped so large
// result sets never delay their final rows. Timings come from the shared
// motion policy in motion.js.
function revealElements(elements, { duration = motionTiming.standard, distance = 4, stagger = true } = {}) {
  const targets = Array.from(elements);
  targets.forEach((element, index) => {
    const delay = stagger && targets.length > 1 ? staggerDelay(index, 30, 120) : 0;
    element.style.opacity = "0";
    element.style.transform = `translateY(${distance}px)`;
    animateElement(element, { opacity: 1, transform: "none" }, { duration, delay, clearInline: true });
  });
}

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
}

applyTheme(savedTheme);

for (const toggle of document.querySelectorAll("[data-theme-toggle]")) {
  toggle.addEventListener("click", () => {
    savedTheme = document.documentElement.dataset.theme === "light" ? "dark" : "light";
    applyTheme(savedTheme);
    try {
      window.localStorage.setItem(themeStorageKey, savedTheme);
    } catch {
      // The active page still switches theme when persistence is unavailable.
    }
  });
}

window.addEventListener("storage", (event) => {
  if (event.key === themeStorageKey) {
    savedTheme = event.newValue === "dark" ? "dark" : "light";
    applyTheme(savedTheme);
  }
});

for (const form of document.querySelectorAll("form[data-confirm]")) {
  form.addEventListener("submit", (event) => {
    const message = form.getAttribute("data-confirm");
    if (message && !window.confirm(message)) {
      event.preventDefault();
    }
  });
}

// Delete-confirmation dialog lifecycle. Native showModal() runs first so the
// top layer and focus trapping are the browser's; the entrance/exit are the
// only animation. All close paths (close button, backdrop click, Escape)
// converge on closeDeleteDialog, which is idempotent and returns the
// in-flight closing promise so a rapid reopen lands after the close
// finishes. Inline animation styles never survive a completed close.
const closingDialogs = new WeakMap();

function openDeleteDialog(dialog) {
  if (dialog.classList.contains("is-closing")) {
    const closing = closingDialogs.get(dialog) || Promise.resolve();
    closing.then(() => openDeleteDialog(dialog));
    return;
  }
  if (dialog.open) {
    return;
  }
  dialog.classList.remove("is-closing");
  dialog.showModal();
  dialog.style.opacity = "0";
  dialog.style.transform = "translateY(6px) scale(0.985)";
  animateElement(dialog, { opacity: 1, transform: "none" }, { duration: motionTiming.standard, clearInline: true });
}

function closeDeleteDialog(dialog) {
  if (!(dialog instanceof HTMLDialogElement) || !dialog.open) {
    return Promise.resolve();
  }
  if (dialog.classList.contains("is-closing")) {
    return closingDialogs.get(dialog) || Promise.resolve();
  }
  dialog.classList.add("is-closing");
  const closing = animateElement(
    dialog,
    { opacity: 0, transform: "translateY(6px) scale(0.985)" },
    { duration: motionTiming.fast }
  )
    .catch(() => {})
    .then(() => {
      dialog.classList.remove("is-closing");
      dialog.style.opacity = "";
      dialog.style.transform = "";
      closingDialogs.delete(dialog);
      if (dialog.open) {
        dialog.close();
      }
    });
  closingDialogs.set(dialog, closing);
  return closing;
}

for (const opener of document.querySelectorAll("[data-dialog-open]")) {
  opener.addEventListener("click", () => {
    const id = opener.getAttribute("data-dialog-open");
    const dialog = id ? document.getElementById(id) : null;
    if (dialog instanceof HTMLDialogElement) {
      openDeleteDialog(dialog);
    }
  });
}

for (const closer of document.querySelectorAll("[data-dialog-close]")) {
  closer.addEventListener("click", () => {
    const dialog = closer.closest("dialog");
    if (dialog instanceof HTMLDialogElement) {
      closeDeleteDialog(dialog);
    }
  });
}

for (const dialog of document.querySelectorAll("dialog.delete-dialog")) {
  dialog.addEventListener("click", (event) => {
    const bounds = dialog.getBoundingClientRect();
    const outside =
      event.clientX < bounds.left ||
      event.clientX > bounds.right ||
      event.clientY < bounds.top ||
      event.clientY > bounds.bottom;
    if (outside) {
      closeDeleteDialog(dialog);
    }
  });
  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    closeDeleteDialog(dialog);
  });
}

for (const details of document.querySelectorAll("details.details-reveal")) {
  details.addEventListener("toggle", () => {
    if (!details.open) {
      return;
    }
    const summary = details.querySelector(":scope > summary");
    const targets = [];
    for (let sibling = summary ? summary.nextElementSibling : null; sibling; sibling = sibling.nextElementSibling) {
      targets.push(sibling);
    }
    revealElements(targets, { duration: motionTiming.standard });
  });
}

for (const form of document.querySelectorAll("form[data-auto-submit]")) {
  for (const select of form.querySelectorAll("select")) {
    select.addEventListener("change", () => form.requestSubmit());
  }
  const submit = form.querySelector('button[type="submit"]');
  if (submit) {
    submit.hidden = true;
  }
}

for (const picker of document.querySelectorAll("[data-directory-picker]")) {
  const inputSelector = picker.getAttribute("data-input");
  const rootInput = inputSelector ? document.querySelector(inputSelector) : null;
  const pathOutput = picker.querySelector("[data-directory-path]");
  const list = picker.querySelector("[data-directory-list]");
  const upButton = picker.querySelector("[data-directory-up]");
  const selectButton = picker.querySelector("[data-directory-select]");
  const nameInput = picker.querySelector("[data-directory-name]");
  const createButton = picker.querySelector("[data-directory-create]");
  const listURL = picker.getAttribute("data-list-url");
  const createURL = picker.getAttribute("data-create-url");
  const csrf = picker.getAttribute("data-csrf");
  const loadingMessage = picker.getAttribute("data-loading");
  const emptyMessage = picker.getAttribute("data-empty");
  const listErrorMessage = picker.getAttribute("data-list-error");
  const createErrorMessage = picker.getAttribute("data-create-error");
  let currentPath = picker.getAttribute("data-start-path") || "/";

  if (
    rootInput instanceof HTMLInputElement &&
    pathOutput &&
    list &&
    upButton instanceof HTMLButtonElement &&
    selectButton instanceof HTMLButtonElement &&
    nameInput instanceof HTMLInputElement &&
    createButton instanceof HTMLButtonElement &&
    listURL &&
    createURL &&
    csrf
  ) {
    const revealStatus = (element) => {
      element.style.opacity = "0";
      element.style.transform = "translateY(4px)";
      animateElement(element, { opacity: 1, transform: "none" }, { duration: motionTiming.fast }).then(() => {
        element.style.opacity = "";
        element.style.transform = "";
      });
    };
    const showStatus = (message, isError = false) => {
      const status = document.createElement("p");
      status.className = "cloud-picker-status";
      status.classList.toggle("is-error", isError);
      status.textContent = message;
      list.replaceChildren(status);
      revealStatus(status);
    };
    const setLoading = (isLoading) => {
      upButton.disabled = isLoading || currentPath === "/";
      selectButton.disabled = isLoading;
      nameInput.disabled = isLoading;
      createButton.disabled = isLoading;
    };

    const parentPath = (value) => {
      if (value === "/") {
        return "/";
      }
      const separator = value.lastIndexOf("/");
      return separator <= 0 ? "/" : value.slice(0, separator);
    };

    let requestSequence = 0;
    const slideOut = async (direction) => {
      const offset = direction === "forward" ? -16 : 16;
      const previousHeight = list.getBoundingClientRect().height;
      list.style.height = `${previousHeight}px`;
      await animateElement(list, { opacity: 0, transform: `translateX(${offset}px)` }, { duration: motionTiming.standard });
    };
    const slideIn = async (direction) => {
      const entry = direction === "forward" ? 16 : -16;
      list.style.height = "";
      const nextHeight = list.getBoundingClientRect().height;
      list.style.height = `${nextHeight}px`;
      list.style.opacity = "0";
      list.style.transform = `translateX(${entry}px)`;
      await animateElement(list, { opacity: 1, transform: "none", height: `${nextHeight}px` }, { duration: motionTiming.standard });
      list.style.opacity = "";
      list.style.transform = "";
      list.style.height = "";
    };
    const clearSlideStyles = () => {
      list.style.opacity = "";
      list.style.transform = "";
      list.style.height = "";
    };

    const loadDirectories = async (target, direction = null) => {
      const sequence = ++requestSequence;
      list.setAttribute("aria-busy", "true");
      setLoading(true);
      // Directional navigations keep the outgoing list visible until the
      // response arrives, so the slide moves real directories; the loading
      // placeholder is initial-load only.
      if (direction === null) {
        showStatus(loadingMessage || "");
      }
      try {
        const response = await fetch(`${listURL}?path=${encodeURIComponent(target)}`, {
          credentials: "same-origin",
          headers: { Accept: "application/json" },
        });
        const payload = await response.json();
        if (!response.ok) {
          throw new Error(payload.error || listErrorMessage || "");
        }
        if (typeof payload.path !== "string" || !Array.isArray(payload.directories)) {
          throw new Error(listErrorMessage || "");
        }
        if (sequence !== requestSequence) {
          return; // A newer request superseded this one: discard stale data.
        }
        if (direction !== null) {
          await slideOut(direction);
          if (sequence !== requestSequence) {
            return; // Discard even mid-transition when superseded.
          }
        }
        // Breadcrumb and list update together.
        currentPath = payload.path;
        pathOutput.textContent = currentPath;
        list.replaceChildren();
        if (payload.directories.length === 0) {
          clearSlideStyles();
          showStatus(emptyMessage || "");
          return;
        }
        for (const directory of payload.directories) {
          if (typeof directory.name !== "string" || typeof directory.path !== "string") {
            throw new Error(listErrorMessage || "");
          }
          const button = document.createElement("button");
          const name = document.createElement("span");
          button.className = "cloud-directory-button";
          button.type = "button";
          name.className = "cloud-directory-name";
          name.textContent = directory.name;
          button.append(name);
          // Entering a child moves leftward; the list is never staggered.
          button.addEventListener("click", () => loadDirectories(directory.path, "forward"));
          list.append(button);
        }
        if (direction !== null) {
          await slideIn(direction);
        }
      } catch (error) {
        if (sequence === requestSequence) {
          clearSlideStyles();
          showStatus(error instanceof Error ? error.message : listErrorMessage || "", true);
        }
      } finally {
        if (sequence === requestSequence) {
          setLoading(false);
          list.removeAttribute("aria-busy");
        }
      }
    };

    upButton.addEventListener("click", () => loadDirectories(parentPath(currentPath), "reverse"));
    selectButton.addEventListener("click", () => {
      rootInput.value = currentPath;
      rootInput.dispatchEvent(new Event("change", { bubbles: true }));
    });
    createButton.addEventListener("click", async () => {
      const name = nameInput.value.trim();
      if (!name) {
        nameInput.focus();
        return;
      }
      createButton.disabled = true;
      try {
        const body = new URLSearchParams({ csrf_token: csrf, parent: currentPath, name });
        const response = await fetch(createURL, {
          method: "POST",
          credentials: "same-origin",
          headers: {
            Accept: "application/json",
            "Content-Type": "application/x-www-form-urlencoded",
          },
          body,
        });
        if (response.redirected) {
          window.location.assign(response.url);
          return;
        }
        const payload = await response.json();
        if (!response.ok) {
          throw new Error(payload.error || createErrorMessage || "");
        }
        if (!payload.directory || typeof payload.directory.path !== "string") {
          throw new Error(createErrorMessage || "");
        }
        nameInput.value = "";
        rootInput.value = payload.directory.path;
        await loadDirectories(payload.directory.path, "forward");
        // Only a truly created directory receives the individual insertion
        // emphasis; it is now the current breadcrumb path.
        animateElement(pathOutput, [{ opacity: 0.4, transform: "translateY(4px)" }, { opacity: 1, transform: "none" }], { duration: motionTiming.standard });
      } catch (error) {
        showStatus(error instanceof Error ? error.message : createErrorMessage || "", true);
      } finally {
        createButton.disabled = false;
      }
    });
    nameInput.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        createButton.click();
      }
    });

    loadDirectories(currentPath);
  }
}

for (const form of document.querySelectorAll("form[data-preserve-test-fields]")) {
  const feedback = document.querySelector(".setup-feedback");
  let useNativeSubmit = false;
  let nativeSubmitting = false;
  let testing = false;

  const revealFeedback = (children) => {
    for (const child of children) {
      child.style.opacity = "0";
      child.style.transform = "translateY(4px)";
      animateElement(child, { opacity: 1, transform: "none" }, { duration: motionTiming.fast }).then(() => {
        child.style.opacity = "";
        child.style.transform = "";
      });
    }
  };

  form.addEventListener("submit", async (event) => {
    const submitter = event.submitter;
    if (!(submitter instanceof HTMLButtonElement) || !feedback) {
      return;
    }
    if (submitter.value !== "test") {
      // Continue keeps native navigation; block double submission.
      if (nativeSubmitting) {
        event.preventDefault();
        return;
      }
      nativeSubmitting = true;
      for (const button of form.querySelectorAll('button[type="submit"]')) {
        button.disabled = true;
      }
      return;
    }
    if (useNativeSubmit || nativeSubmitting) {
      return; // A fetch failed earlier: let the native submission proceed.
    }
    if (testing) {
      event.preventDefault();
      return; // A test is already in flight; never submit twice.
    }
    event.preventDefault();
    testing = true;
    for (const button of form.querySelectorAll('button[type="submit"]')) {
      button.disabled = true;
    }
    submitter.classList.add("is-busy");
    feedback.setAttribute("aria-busy", "true");

    try {
      const body = new URLSearchParams();
      for (const [name, value] of new FormData(form)) {
        if (typeof value === "string") {
          body.append(name, value);
        }
      }
      body.append(submitter.name, submitter.value);

      const response = await fetch(form.getAttribute("action"), {
        method: form.method,
        credentials: "same-origin",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body,
      });
      if (response.redirected) {
        window.location.assign(response.url);
        return;
      }

      const result = new DOMParser().parseFromString(await response.text(), "text/html");
      const nextFeedback = result.querySelector(".setup-feedback");
      if (!nextFeedback) {
        throw new Error("Setup test response is missing its feedback region");
      }
      feedback.replaceChildren(...nextFeedback.childNodes);
      revealFeedback(feedback.children);
      // Error results move focus to the alert; successes are announced by the
      // polite live region without stealing focus.
      const alert = feedback.querySelector('[role="alert"]');
      if (alert) {
        alert.setAttribute("tabindex", "-1");
        alert.focus({ preventScroll: false });
      }
    } catch {
      useNativeSubmit = true;
      nativeSubmitting = true;
      submitter.disabled = false;
      submitter.classList.remove("is-busy");
      feedback.removeAttribute("aria-busy");
      form.requestSubmit(submitter);
      return;
    } finally {
      feedback.removeAttribute("aria-busy");
      submitter.classList.remove("is-busy");
      if (!nativeSubmitting) {
        for (const button of form.querySelectorAll('button[type="submit"]')) {
          button.disabled = false;
        }
        testing = false;
      }
    }
  });
}

function joinPreviewPath(root, subpath) {
  const cleanRoot = root === "/" ? "" : root.replace(/\/+$/, "");
  const cleanSubpath = subpath.replace(/^\/+/, "");
  return `${cleanRoot}/${cleanSubpath}`;
}

for (const form of document.querySelectorAll("form[data-category-path-form]")) {
  const cloudInput = form.querySelector("[data-cloud-subpath]");
  const saveInput = form.querySelector("[data-save-subpath]");
  const cloudPreview = form.querySelector("[data-cloud-preview]");
  const savePreview = form.querySelector("[data-save-preview]");
  const nameInput = form.querySelector("[data-category-name]");
  const cloudRoot = form.getAttribute("data-cloud-root") || "/";
  const localRoot = form.getAttribute("data-local-root") || "/";

  const renderPreview = () => {
    if (cloudInput instanceof HTMLInputElement && cloudPreview && cloudInput.value) {
      cloudPreview.textContent = joinPreviewPath(cloudRoot, cloudInput.value);
    }
    if (saveInput instanceof HTMLInputElement && savePreview && saveInput.value) {
      savePreview.textContent = joinPreviewPath(localRoot, saveInput.value);
    }
  };

  for (const input of [cloudInput, saveInput]) {
    if (input instanceof HTMLInputElement) {
      if (input.hasAttribute("data-auto-subpath")) {
        input.dataset.autoSubpath = "true";
      }
      input.addEventListener("input", () => {
        input.dataset.autoSubpath = "false";
        renderPreview();
      });
    }
  }

  if (nameInput instanceof HTMLInputElement) {
    nameInput.addEventListener("input", () => {
      const suggested = nameInput.value.trim().toLowerCase();
      for (const input of [cloudInput, saveInput]) {
        if (input instanceof HTMLInputElement && input.dataset.autoSubpath === "true") {
          input.value = suggested;
        }
      }
      renderPreview();
    });
  }
  renderPreview();
}

for (const preview of document.querySelectorAll("[data-settings-remap]")) {
  const cloudRoot = preview.querySelector("[data-settings-cloud-root]");
  const localRoot = preview.querySelector("[data-settings-local-root]");
  const renderRemap = () => {
    if (cloudRoot instanceof HTMLInputElement) {
      for (const output of preview.querySelectorAll("[data-remap-cloud]")) {
        output.textContent = joinPreviewPath(cloudRoot.value, output.getAttribute("data-subpath") || "");
      }
    }
    if (localRoot instanceof HTMLInputElement) {
      for (const output of preview.querySelectorAll("[data-remap-local]")) {
        output.textContent = joinPreviewPath(localRoot.value, output.getAttribute("data-subpath") || "");
      }
    }
  };
  if (cloudRoot instanceof HTMLInputElement) {
    cloudRoot.addEventListener("input", renderRemap);
  }
  if (localRoot instanceof HTMLInputElement) {
    localRoot.addEventListener("input", renderRemap);
  }
  renderRemap();
}

async function copyText(value) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  textarea.remove();
  if (!copied) {
    throw new Error("copy command failed");
  }
}

for (const button of document.querySelectorAll("[data-copy-value]")) {
  const initialGlyph = button.textContent;
  let resetTimer;
  button.addEventListener("click", async () => {
    clearTimeout(resetTimer);
    const copyLabel = button.getAttribute("data-copy-label") || button.getAttribute("aria-label") || "";
    const copiedLabel = button.getAttribute("data-copied-label") || copyLabel;
    const failedLabel = button.getAttribute("data-copy-failed-label") || copyLabel;
    try {
      await copyText(button.getAttribute("data-copy-value") || "");
      button.classList.add("is-copied");
      button.classList.remove("is-failed");
      button.textContent = "✓";
      button.setAttribute("aria-label", copiedLabel);
      button.setAttribute("title", copiedLabel);
    } catch {
      button.classList.add("is-failed");
      button.classList.remove("is-copied");
      button.textContent = "!";
      button.setAttribute("aria-label", failedLabel);
      button.setAttribute("title", failedLabel);
    }
    resetTimer = window.setTimeout(() => {
      button.classList.remove("is-copied", "is-failed");
      button.textContent = initialGlyph;
      button.setAttribute("aria-label", copyLabel);
      button.setAttribute("title", copyLabel);
    }, 1400);
  });
}

// Sticky action columns need separation only while they overlap horizontally
// scrollable table content. Without overflow—or at the right edge—the shadow
// would render as a broken vertical gray stripe through every body row.
for (const tableWrap of document.querySelectorAll(".table-wrap")) {
  if (!tableWrap.querySelector(".actions-heading, .cell-actions")) {
    continue;
  }

  const updateStickyShadow = () => {
    const maxScrollLeft = tableWrap.scrollWidth - tableWrap.clientWidth;
    const overlapsContent = maxScrollLeft > 1 && tableWrap.scrollLeft < maxScrollLeft - 1;
    tableWrap.classList.toggle("has-sticky-overlap", overlapsContent);
  };

  tableWrap.addEventListener("scroll", updateStickyShadow, { passive: true });
  if ("ResizeObserver" in window) {
    new ResizeObserver(updateStickyShadow).observe(tableWrap);
  }
  updateStickyShadow();
}
