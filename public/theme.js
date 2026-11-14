// Applies a saved light/dark choice before the page paints, so there is no flash.
// A separate classic script because the site's CSP does not allow inline scripts.
try {
  var t = localStorage.getItem("itj-theme");
  if (t === "dark" || t === "light") document.documentElement.setAttribute("data-theme", t);
} catch (e) { /* storage unavailable: follow the system setting */ }
