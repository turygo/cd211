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
