// Loaded only by the standalone distribution, after its normal UI scripts.
(() => {
  const actions = document.querySelector(".top .actions");
  if (!actions) return;
  // Inputs already use 16px text on phones; keep pinch zoom available in the installed console as well.
  document.querySelector('meta[name="viewport"]')?.setAttribute("content", "width=device-width, initial-scale=1, viewport-fit=cover");
  let installPrompt;
  const standalone = matchMedia("(display-mode: standalone)");
  const button = document.createElement("button");
  button.id = "pwaInstall";
  button.type = "button";
  button.className = "icon";
  button.title = button.ariaLabel = t("Install app");
  button.innerHTML = '<svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><g fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M10 2v10m-4-4 4 4 4-4M3 13v4h14v-4"/></g></svg>';
  actions.prepend(button);
  const installed = () => { button.hidden = standalone.matches || !!navigator.standalone; };
  installed();
  standalone.addEventListener("change", installed);
  window.addEventListener("appinstalled", () => { installPrompt = null; button.hidden = true; });
  window.addEventListener("beforeinstallprompt", event => { event.preventDefault(); installPrompt = event; });

  button.onclick = async () => {
    if (installPrompt) {
      const prompt = installPrompt;
      installPrompt = null;
      await prompt.prompt();
      await prompt.userChoice;
      return;
    }
    const dialog = document.createElement("dialog");
    dialog.className = "everyapi-install";
    dialog.setAttribute("aria-labelledby", "pwaInstallTitle");
    const title = document.createElement("h2");
    title.id = "pwaInstallTitle";
    title.textContent = t("Install app");
    const detail = document.createElement("p");
    const ios = /iP(hone|ad|od)/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
    detail.textContent = !window.isSecureContext
      ? t("Open the gateway over HTTPS to enable full app installation.")
      : ios ? t("In Safari, tap Share, then Add to Home Screen. Open the saved icon to use the gateway as an app.")
      : t("Open your browser menu and choose Install app or Add to Home screen. If already installed, open its icon.");
    const close = document.createElement("button");
    close.type = "button";
    close.className = "text";
    close.textContent = t("Close");
    close.onclick = () => dialog.close();
    dialog.append(title, detail, close);
    dialog.addEventListener("close", () => { dialog.remove(); button.focus({ preventScroll: true }); }, { once: true });
    document.body.append(dialog);
    dialog.showModal();
  };
  if (window.isSecureContext && "serviceWorker" in navigator) {
    navigator.serviceWorker.register("/pwa/sw.js", { scope: "/", updateViaCache: "none" }).catch(() => {
      // Browser privacy settings can deny workers; the online console still works.
    });
  }
})();
