// Progressive submit feedback for the shared secondary action forms:
// settings test/save and API-token generate/revoke (settings.html),
// category create/save (categories.html), webhook enable/disable/rotate-secret
// delete/test (webhooks.html and webhook-form.html), and delivery replay
// (webhook-deliveries.html).
//
// The server remains authoritative. This module never renders a result or
// invents success: it marks the submitting button busy, blocks duplicate
// submissions, and lets the native submission carry the request so the
// server's own response — notice or redirect — is what the operator sees.
// Forms owned by the download and setup slices are intentionally not matched,
// and pages keep working unchanged without JS.

const SECONDARY_ACTION =
  /^\/(settings\/(save|api-token\/(generate|revoke)|qbt-api-key\/(generate|revoke))|categories\/save|webhooks\/\d+\/(enable|disable|rotate-secret|delete|test)|webhook-deliveries\/\d+\/replay)$/;

for (const form of document.querySelectorAll("form[action]")) {
  const action = new URL(form.getAttribute("action") || "", window.location.href).pathname;
  if (!SECONDARY_ACTION.test(action)) {
    continue;
  }
  form.addEventListener("submit", (event) => {
    // A cancelled data-confirm dialog preventDefaults in app.js; respect it.
    if (event.defaultPrevented) {
      return;
    }
    if (form.dataset.actionBusy === "true") {
      event.preventDefault();
      return;
    }
    const submitter = event.submitter;
    if (!(submitter instanceof HTMLButtonElement)) {
      return;
    }
    form.dataset.actionBusy = "true";
    submitter.disabled = true;
    submitter.setAttribute("aria-busy", "true");
    // No preventDefault: the native submission proceeds and the server's
    // response is authoritative.
  });
}

// A back/forward cache restore keeps the exact DOM, including a busy button
// and an in-flight guard from before the navigation. Clear both on every page
// show so a restored form is immediately usable again.
window.addEventListener("pageshow", () => {
  for (const button of document.querySelectorAll("button[aria-busy='true']")) {
    if (button instanceof HTMLButtonElement) {
      button.disabled = false;
      button.removeAttribute("aria-busy");
    }
  }
  for (const form of document.querySelectorAll("form[data-action-busy='true']")) {
    delete form.dataset.actionBusy;
  }
});
