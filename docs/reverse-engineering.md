# The reverse-engineering story

*How VirginBot went from scraping a fragile website to speaking the same private mobile API the official Virgin Active app uses.*

← [Back to the README](../README.md)

> All of this was done against **my own account, on my own device, for personal automation.** No other users were involved, and no data but my own was touched.

---

## Act 1 — Scraping the website

The first version logged into `shop.virginactive.it` (Shopware) and rode a federated-login JWT (`loginbytokenglobal`) over to `www.virginactive.it` (Sitefinity) to read the class calendar and book classes. The calendar came back as **HTML fragments** from a `JFilter` endpoint, paginated 5 classes at a time; "is this bookable / booked / full" was inferred by **parsing button CSS classes** (`btn-book`, `btn-unbook`, `btb-wait`).

It worked, but it was fragile:

- Under bursts the site's WAF returned HTML where we expected JSON.
- Booking sometimes returned *"not available"* even when the window seemed open.
- And one day, the smoking gun: **a class I had booked on the phone showed up in the app, but our bot — reading the website — couldn't see it.** A *phantom booking*.

## Act 2 — The hypothesis

If the website and the app disagree about my own bookings, they're probably **not the same backend**. The website looked like a legacy system; the app had to be talking to something more modern. If we could find *that*, we'd be reading and writing the **real** source of truth.

## Act 3 — MITM'ing my own phone

The way to find out what an app talks to is to look at its traffic — an HTTPS man-in-the-middle proxy on my own network:

1. `brew install mitmproxy`, then `mitmdump --listen-port 8888 -w ~/virgin.flows` on the Mac.
2. Point the phone's Wi-Fi proxy at the Mac, install and **trust** mitmproxy's CA certificate (from `http://mitm.it`).
3. Open the Virgin Active app, browse the calendar, **book and cancel a class**, log out and back in.
4. Read the captured flows back with a small mitmproxy addon that dumps only the relevant host (with auth tokens redacted).

The iOS app did **not** use certificate pinning, so the capture worked on the first try.

## Act 4 — The modern API

The app talks to **`vapi.virginactive.com/vapi/2.1.0/`** — a clean JSON REST API:

| Action | Call |
|---|---|
| Login | `POST /sessions` `{memberId, password}` + `x-api-key` → `{token, refreshToken}` |
| Calendar | `GET /clubs/{clubId}/classes?startDate=&endDate=` |
| My bookings | `GET /member/bookings?startDate=&endDate=` |
| **Book** | `POST /clubs/{clubId}/classes/{classId}/sessions/{sessionId}?date=YYYY-MM-DD` (empty body) |
| **Cancel** | `DELETE /member/bookings/sessions/{sessionId}?date=&bookingId=&clubId=&scheduledSessionId={classId}` |

Auth is two headers: a **static app key** (`x-api-key`, the same for every install — extracted from the app) and a per-user **JWT** (`x-auth-token`) from the login, valid ~2 h. The calendar JSON even includes real availability (`bookableStatus`, `bookingCount`/`bookingCapacity`) — no more parsing buttons.

## Act 5 — The migration

The whole bot moved to `vapi`. The web-scraping packages (`session/`, `booking/`, the HTML parsing in `calendar/`) were deleted; `calendar` is now just the `Class` model. The result:

- **Same source of truth as the app** → the phantom booking is gone; what you see matches your phone.
- **Faster and more robust** → JSON instead of paginated HTML, no WAF surprises.
- **Simpler** → booking is one clean `POST`; "My bookings" is a single call.

---

## Two quirks worth knowing (learned the hard way)

- **The Solarium has many "beds".** vapi models each tanning bed as a distinct bookable class at the same time slot. The web calendar collapses them into one row (keeping the most bookable one), but the engine keeps them all and tries each in turn until one accepts — the calendar's `bookableStatus` sometimes says *Available* while the booking `POST` replies `TOO_LATE_TO_BOOK`.
- **Stale ids cause `TOO_EARLY/TOO_LATE_TO_BOOK`.** The browser caches the calendar in memory; if Virgin reschedules a session, its `sessionId` shifts. So the server **re-resolves** the ids against a fresh fetch at booking time instead of trusting the ones the frontend sends.

---

## Architecture

Pure Go, single binary, SQLite for persistence (`modernc.org/sqlite`, no CGO), web UI embedded via `go:embed`.

| Package | Responsibility |
|---|---|
| `vapi/` | Low-level client for the mobile API (login, classes, bookings, book, cancel). |
| `calendar/` | The `Class` model + club id↔name mapping. |
| `account/` | User store; Virgin passwords encrypted at rest (AES-256-GCM, key from `APP_SECRET`). |
| `automation/` | Recurring rules store + the **precise-timing engine**. |
| `i18n/` | Server-side translations (dates + email text) in it/es/en. |
| `notification/` | Email (SMTP) notifications. |
| `db/` | SQLite schema + idempotent migrations. |
| `server/` | HTTP API, embedded web UI, sessions, per-user vapi auth, bookings reconciliation, automations. |

The **automation engine** is deliberately *not* a polling loop: it sleeps until the next trigger (the class time, each day from the window opening until 24 h before) and only then touches the network — efficient and unlikely to get the IP throttled. After a successful booking it stops; the rule stays active for the following week. If the booking API errors out but the reservation actually went through, a final reconciliation step detects it and avoids a false-failure email or a double booking.

---

← [Back to the README](../README.md)
