import { animate } from "/static/vendor/motion-mini.js?v=13.1.0";

const themeStorageKey = "cd211-theme";
let savedTheme = document.documentElement.dataset.theme === "dark" ? "dark" : "light";

// Shared motion policy: one set of restrained durations/easings for every
// JS-driven transition, gated by prefers-reduced-motion. Reduced motion
// applies target values immediately but still resolves the returned promise,
// so completion-sensitive flows (dialog close) work unchanged.
const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
const motionPolicy = {
  fast: 100,
  normal: 160,
  slow: 220,
  ease: [0.25, 0.1, 0.25, 1],
};

// Drives an element to a target visual state with Motion Mini. The returned
// promise resolves when the animation finishes; under prefers-reduced-motion
// the target applies synchronously and the promise resolves immediately.
// With clearInline, the inline opacity/transform written for the animation
// are removed once the resting state is reached, so no transient style
// survives the interaction.
function transitionElement(element, target, { duration = motionPolicy.normal, delay = 0, clearInline = false } = {}) {
  const applyTarget = () => {
    if (target.opacity !== undefined) {
      element.style.opacity = String(target.opacity);
    }
    if (target.transform !== undefined) {
      element.style.transform = target.transform;
    }
  };
  const clearInlineStyles = () => {
    if (target.opacity !== undefined) {
      element.style.opacity = "";
    }
    if (target.transform !== undefined) {
      element.style.transform = "";
    }
  };
  if (reducedMotion.matches) {
    applyTarget();
    if (clearInline) {
      clearInlineStyles();
    }
    return Promise.resolve();
  }
  const keyframes = {};
  if (target.opacity !== undefined) {
    keyframes.opacity = target.opacity;
  }
  if (target.transform !== undefined) {
    keyframes.transform = target.transform;
  }
  return Promise.resolve(
    animate(element, keyframes, {
      duration: duration / 1000,
      delay: delay / 1000,
      ease: motionPolicy.ease,
    })
  ).then((result) => {
    if (clearInline) {
      clearInlineStyles();
    }
    return result;
  });
}

// Reveals a set of elements with a restrained fade and at most 4px rise. A
// tiny stagger applies only to short lists and is always capped so large
// result sets never delay their final rows.
function revealElements(elements, { duration = motionPolicy.normal, distance = 4, stagger = true } = {}) {
  const targets = Array.from(elements);
  targets.forEach((element, index) => {
    const delay = stagger && targets.length > 1 ? Math.min(index * 30, 120) : 0;
    element.style.opacity = "0";
    element.style.transform = `translateY(${distance}px)`;
    transitionElement(element, { opacity: 1, transform: "none" }, { duration, delay, clearInline: true });
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

// Opens a delete confirmation dialog with a restrained entrance: native
// showModal() first (focus trapping and top layer), then a fade-in with a
// small rise. Reopening after a completed close always starts neutral.
function openDeleteDialog(dialog) {
  if (dialog.open) {
    return;
  }
  dialog.classList.remove("is-closing");
  dialog.showModal();
  dialog.style.opacity = "0";
  dialog.style.transform = "translateY(6px) scale(0.985)";
  transitionElement(dialog, { opacity: 1, transform: "none" }, { duration: motionPolicy.normal, clearInline: true });
}

// One shared animated-close path for the close button, backdrop clicks, and
// native Escape/cancel. The reverse animation runs while the dialog stays in
// the top layer (so the CSS ::backdrop can fade out), then dialog.close() is
// called and inline start styles are cleared. A closing dialog ignores
// duplicate close requests.
function closeDeleteDialog(dialog) {
  if (!(dialog instanceof HTMLDialogElement) || !dialog.open || dialog.classList.contains("is-closing")) {
    return;
  }
  dialog.classList.add("is-closing");
  transitionElement(dialog, { opacity: 0, transform: "translateY(6px) scale(0.985)" }, { duration: 120 })
    .catch(() => {})
    .then(() => {
      dialog.classList.remove("is-closing");
      dialog.style.opacity = "";
      dialog.style.transform = "";
      if (dialog.open) {
        dialog.close();
      }
    });
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
    revealElements(targets, { duration: motionPolicy.normal });
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
    const showStatus = (message, isError = false) => {
      const status = document.createElement("p");
      status.className = "cloud-picker-status";
      status.classList.toggle("is-error", isError);
      status.textContent = message;
      list.replaceChildren(status);
      revealElements(list.children, { duration: motionPolicy.normal, stagger: false });
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

    const loadDirectories = async (target) => {
      list.setAttribute("aria-busy", "true");
      setLoading(true);
      showStatus(loadingMessage || "");
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

        currentPath = payload.path;
        pathOutput.textContent = currentPath;
        list.replaceChildren();
        if (payload.directories.length === 0) {
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
          button.addEventListener("click", () => loadDirectories(directory.path));
          list.append(button);
        }
        revealElements(list.children, { duration: motionPolicy.normal });
      } catch (error) {
        showStatus(error instanceof Error ? error.message : listErrorMessage || "", true);
      } finally {
        setLoading(false);
        list.removeAttribute("aria-busy");
      }
    };

    upButton.addEventListener("click", () => loadDirectories(parentPath(currentPath)));
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
        await loadDirectories(payload.directory.path);
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

  form.addEventListener("submit", async (event) => {
    const submitter = event.submitter;
    if (
      useNativeSubmit ||
      !(submitter instanceof HTMLButtonElement) ||
      submitter.value !== "test" ||
      !feedback
    ) {
      return;
    }

    event.preventDefault();
    submitter.disabled = true;
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
      revealElements(feedback.children, { duration: motionPolicy.normal, stagger: false });
    } catch {
      useNativeSubmit = true;
      form.requestSubmit(submitter);
    } finally {
      feedback.removeAttribute("aria-busy");
      submitter.disabled = false;
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