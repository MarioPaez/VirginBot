// Service worker: makes the installed app open instantly and survive a flaky
// connection. Network first for the app shell (the server sends no-cache, so a
// deploy shows up on the next open), the cached copy only as a fallback. The
// API is never cached: bookings and availability must always be live.
const CACHE = "virginbot-v1";
const SHELL = ["/", "/logo.svg", "/manifest.webmanifest", "/icons/apple-touch-icon.png", "/icons/icon-192.png"];

self.addEventListener("install", e => {
  e.waitUntil(caches.open(CACHE).then(c => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", e => {
  e.waitUntil(caches.keys()
    .then(keys => Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k))))
    .then(() => self.clients.claim()));
});

self.addEventListener("fetch", e => {
  const req = e.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== location.origin || url.pathname.startsWith("/api/")) return;
  e.respondWith(fetch(req)
    .then(res => {
      if (res.ok) {
        const copy = res.clone();
        caches.open(CACHE).then(c => c.put(req.mode === "navigate" ? "/" : req, copy));
      }
      return res;
    })
    .catch(() => caches.match(req.mode === "navigate" ? "/" : req)));
});
