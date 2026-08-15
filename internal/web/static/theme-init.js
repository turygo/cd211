"use strict";

(() => {
  document.documentElement.classList.add("js");
  let theme = "light";
  try {
    const savedTheme = window.localStorage.getItem("cd211-theme");
    if (savedTheme === "dark" || savedTheme === "light") {
      theme = savedTheme;
    }
  } catch {
    // Light remains the default when storage is unavailable.
  }
  document.documentElement.dataset.theme = theme;
})();
