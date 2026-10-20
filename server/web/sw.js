// Service worker: makes the installed app open instantly and survive a flaky
// connection. Network first for the app shell (the server sends no-cache, so a
// deploy shows up on the next open), the cached copy only as a fallback. The
// API is never cached: bookings and availability must always be live.
const CACHE = "virginbot-v2";
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

// Web Push: the server sends {title, body, url} (encrypted); show it as a
// notification. Tapping it focuses the open app (or opens it) on that page.
self.addEventListener("push", e => {
  let msg = {};
  try { msg = e.data ? e.data.json() : {}; } catch { msg = { body: e.data && e.data.text() }; }
  e.waitUntil(self.registration.showNotification(msg.title || "VirginBot", {
    body: msg.body || "",
    icon: "/icons/icon-192.png",
    badge: "/icons/icon-192.png",
    data: { url: msg.url || "/" },
  }));
});

self.addEventListener("notificationclick", e => {
  e.notification.close();
  const url = new URL(e.notification.data?.url || "/", location.origin).href;
  e.waitUntil(self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(wins => {
    const win = wins.find(w => new URL(w.url).origin === location.origin);
    return win ? win.navigate(url).then(w => (w || win).focus()) : self.clients.openWindow(url);
  }));
});
