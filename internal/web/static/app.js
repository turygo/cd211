"use strict";

for (const form of document.querySelectorAll("form[data-confirm]")) {
  form.addEventListener("submit", (event) => {
    const message = form.getAttribute("data-confirm");
    if (message && !window.confirm(message)) {
      event.preventDefault();
    }
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