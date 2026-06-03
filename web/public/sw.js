// Minimal service worker for PWA installability
const CACHE_NAME = 'xdcc-go-v1';

self.addEventListener('install', (event) => {
  console.log('[SW] Installing...');
  event.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', (event) => {
  console.log('[SW] Activating...');
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET') return;
  if (event.request.headers.get('accept') === 'text/event-stream') return;
  
  event.respondWith(
    fetch(event.request).catch(() => {
      return caches.match(event.request).then(cached => {
        if (cached) return cached;
        if (event.request.headers.get('accept').includes('text/html')) {
          return caches.match('/index.html');
        }
        return new Response('Offline', { status: 503 });
      });
    })
  );
});
