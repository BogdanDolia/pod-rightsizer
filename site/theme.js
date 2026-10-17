"use strict";

// Run before the stylesheet so a saved preference is applied before first paint.
(() => {
  const key = "pod-rightsizer-theme";
  const modes = ["light", "dark", "system"];
  const systemTheme = window.matchMedia("(prefers-color-scheme: dark)");
  const normalize = (value) => modes.includes(value) ? value : "system";
  let preference = "system";

  try {
    preference = normalize(localStorage.getItem(key));
  } catch (_) {
    // The control still works when browser storage is unavailable.
  }

  function applyTheme() {
    if (preference === "system") {
      delete document.documentElement.dataset.theme;
    } else {
      document.documentElement.dataset.theme = preference;
    }

    const dark = preference === "dark" || (preference === "system" && systemTheme.matches);
    document.querySelector('meta[name="theme-color"]').content = dark ? "#0c1714" : "#f3f6f1";
    document.querySelectorAll('input[name="theme"]').forEach((input) => {
      input.checked = input.value === preference;
    });
  }

  applyTheme();
  systemTheme.addEventListener("change", applyTheme);
  window.addEventListener("storage", (event) => {
    if (event.key === key || event.key === null) {
      preference = normalize(event.newValue);
      applyTheme();
    }
  });

  document.addEventListener("DOMContentLoaded", () => {
    const control = document.getElementById("theme-control");
    applyTheme();
    control.hidden = false;
    control.addEventListener("change", (event) => {
      if (!event.target.matches('input[name="theme"]')) return;
      preference = normalize(event.target.value);
      applyTheme();
      try {
        localStorage.setItem(key, preference);
      } catch (_) {}
    });
  });
})();
