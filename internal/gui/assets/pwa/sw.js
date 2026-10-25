// Keep management data and credentials online-only. Only the public reconnect page is cached.
const CACHE = "everyapi-web-offline-v1";
const OFFLINE = "/pwa/offline";
self.addEventListener("install", event => {
  event.waitUntil((async () => {
    const response = await fetch(OFFLINE, { cache: "no-store", credentials: "omit" });
    if (!response.ok) throw new Error("Offline page unavailable");
    await (await caches.open(CACHE)).put(OFFLINE, response);
    await self.skipWaiting();
  })());
});
self.addEventListener("activate", event => {
  event.waitUntil((async () => {
    for (const name of await caches.keys()) {
      if (name.startsWith("everyapi-web-offline-") && name !== CACHE) await caches.delete(name);
    }
    await self.clients.claim();
  })());
});
self.addEventListener("fetch", event => {
  const request = event.request;
  const url = new URL(request.url);
  if (request.method !== "GET" || request.mode !== "navigate" || url.origin !== self.location.origin || !["/", "/index.html"].includes(url.pathname)) return;
  event.respondWith((async () => {
    try {
      return await fetch(request, { cache: "no-store" });
    } catch {
      return (await caches.match(OFFLINE)) || Response.error();
    }
  })());
});
