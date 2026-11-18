// Zweig wechseln; Adresse zum Klonen bei Klick markieren.
(() => {
  const s = document.querySelector("#zweigwahl select");
  if (s) s.addEventListener("change", () => { location.href = s.dataset.ziel + encodeURIComponent(s.value) + "/"; });
  document.querySelectorAll(".klonen input, input[readonly]").forEach((i) => i.addEventListener("focus", () => i.select()));
})();
