// Applies a saved light/dark choice before the first paint so the page never
// flashes the wrong theme. Loaded as an external file because the CSP forbids
// inline scripts. Keep the key in sync with src/theme.ts. Served from /assets/.
(function () {
  try {
    var v = localStorage.getItem('onefrog-theme');
    if (v === 'light' || v === 'dark') document.documentElement.setAttribute('data-theme', v);
  } catch (e) { /* storage blocked: follow the system setting */ }
})();
