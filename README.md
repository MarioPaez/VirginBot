# VirginBot

A small, self‑hosted bot that **books gym classes at Virgin Active Italia automatically** — the instant the booking window opens — and notifies you by email. Built in pure Go (no CGO, single static binary).

It started life scraping the Virgin Active **website**, and ended up talking to the same **private mobile API** the official app uses. The story of *how* and *why* is the interesting part — see [The reverse‑engineering story](#the-reverse-engineering-story).

## Motivation

This project has two goals. The first is to build a real, working application that delivers genuine value to the people using it — myself included, since I rely on it to book my own classes. The second is personal: I'm using it to get hands‑on experience with Go. I don't use Go professionally, but it's a language I'd like to work with one day, and this project is my way of getting comfortable with it.

---

## What it does

- **Calendar** of the classes you care about (Calisthenics in both clubs, Solarium in Corso Como), per logged‑in user.
- **One‑click booking** and cancellation.
- **Automations**: mark a weekly class as *auto* and the bot books it **precisely when the booking window opens** (e.g. 7 days before for Calisthenics, at the exact class time), retrying a few times in case the slot is enabled a few seconds late. Recurring every week.
- **"My bookings"** synced from the real source of truth.
- **Email notifications** (added/removed automation, booking succeeded/failed).
- **Multi‑user** with an email allowlist, encrypted credentials at rest, and login rate‑limiting.

---

## The reverse‑engineering story

> All of this was done against **my own account, on my own device, for personal automation.** No other users were involved, and no data but my own was touched.

### Act 1 — Scraping the website

The first version logged into `shop.virginactive.it` (Shopware) and rode a federated‑login JWT (`loginbytokenglobal`) over to `www.virginactive.it` (Sitefinity) to read the class calendar and book classes. The calendar came back as **HTML fragments** from a `JFilter` endpoint, paginated 5 classes at a time; "is this bookable / booked / full" was inferred by **parsing button CSS classes** (`btn-book`, `btn-unbook`, `btb-wait`).

It worked, but it was fragile:

- Under bursts the site's WAF returned HTML where we expected JSON.
- Booking sometimes returned *"not available"* even when the window seemed open.
- And one day, the smoking gun: **a class I had booked on the phone showed up in the app, but our bot — reading the website — couldn't see it.** A *phantom booking*.

### Act 2 — The hypothesis

If the website and the app disagree about my own bookings, they're probably **not the same backend**. The website looked like a legacy system; the app had to be talking to something more modern. If we could find *that*, we'd be reading and writing the **real** source of truth.

### Act 3 — MITM'ing my own phone

The way to find out what an app talks to is to look at its traffic — an HTTPS man‑in‑the‑middle proxy on my own network:

1. `brew install mitmproxy`, then `mitmdump --listen-port 8888 -w ~/virgin.flows` on the Mac.
2. Point the phone's Wi‑Fi proxy at the Mac, install and **trust** mitmproxy's CA certificate (from `http://mitm.it`).
3. Open the Virgin Active app, browse the calendar, **book and cancel a class**, log out and back in.
4. Read the captured flows back with a small mitmproxy addon that dumps only the relevant host (with auth tokens redacted).

The iOS app did **not** use certificate pinning, so the capture worked on the first try.

### Act 4 — The modern API

The app talks to **`vapi.virginactive.com/vapi/2.1.0/`** — a clean JSON REST API:

| Action | Call |
|---|---|
| Login | `POST /sessions` `{memberId, password}` + `x-api-key` → `{token, refreshToken}` |
| Calendar | `GET /clubs/{clubId}/classes?startDate=&endDate=` |
| My bookings | `GET /member/bookings?startDate=&endDate=` |
| **Book** | `POST /clubs/{clubId}/classes/{classId}/sessions/{sessionId}?date=YYYY-MM-DD` (empty body) |
| **Cancel** | `DELETE /member/bookings/sessions/{sessionId}?date=&bookingId=&clubId=&scheduledSessionId={classId}` |

Auth is two headers: a **static app key** (`x-api-key`, the same for every install — extracted from the app) and a per‑user **JWT** (`x-auth-token`) from the login, valid ~2 h. The calendar JSON even includes real availability (`bookableStatus`, `bookingCount`/`bookingCapacity`) — no more parsing buttons.

### Act 5 — The migration

The whole bot moved to `vapi`. The web‑scraping packages (`session/`, `booking/`, the HTML parsing in `calendar/`) were deleted; `calendar` is now just the `Class` model. The result:

- **Same source of truth as the app** → the phantom booking is gone; what you see matches your phone.
- **Faster and more robust** → JSON instead of paginated HTML, no WAF surprises.
- **Simpler** → booking is one clean `POST`; "My bookings" is a single call.

---

## Architecture

Pure Go, single binary, SQLite for persistence (`modernc.org/sqlite`, no CGO), web UI embedded via `go:embed`.

| Package | Responsibility |
|---|---|
| `vapi/` | Low‑level client for the mobile API (login, classes, bookings, book, cancel). |
| `calendar/` | The `Class` model + club id↔name mapping. |
| `account/` | User store; Virgin passwords encrypted at rest (AES‑256‑GCM, key from `APP_SECRET`). |
| `automation/` | Recurring rules store + the **precise‑timing engine**. |
| `notification/` | Email (SMTP) notifications. |
| `db/` | SQLite schema + idempotent migrations. |
| `server/` | HTTP API, embedded web UI, sessions, per‑user vapi auth, calendar cache, bookings reconciliation, automations. |

The **automation engine** is deliberately *not* a polling loop: it sleeps until the next trigger (the class time, each day from the window opening until 24 h before) and only then touches the network — efficient and unlikely to get the IP throttled. After a successful booking it stops; the rule stays active for the following week.

---

## Running locally

Requirements: Go 1.26+.

1. Create a `.env` (git‑ignored):
   ```dotenv
   APP_SECRET=<openssl rand -hex 32>          # encrypts users' passwords — required
   VA_API_KEY=<static x-api-key from the app> # required
   VA_ALLOWED_EMAILS=you@example.com          # allowlist (empty = open registration)
   # SMTP (optional, for email notifications)
   SMTP_HOST=...  SMTP_PORT=587  SMTP_USER=...  SMTP_PASS=...  SMTP_FROM=<verified sender>
   # Optional: seed the first user without the login screen
   VA_EMAIL=...  VA_PASS=...
   ```
2. Run:
   ```bash
   ./run.sh           # builds, runs, opens the browser, tees logs to ./logs/
   # or simply:
   go run .
   ```
3. Open `http://localhost:8080` and log in with your Virgin Active account.

Engine timing is tunable without recompiling: `VIRGINBOT_ATTEMPTS`, `VIRGINBOT_GAP_SEC`, `VIRGINBOT_LEAD_SEC`. `VIRGINBOT_DEBUG=1` logs every engine evaluation.

## Deploy (Fly.io)

A `Dockerfile` (multi‑stage, distroless, static binary with embedded tzdata) and `fly.toml` (single always‑on machine + a persistent volume for the SQLite DB) are included.

```bash
fly apps create <name>
fly volumes create virginbot_data --region fra --size 1 -a <name>
fly secrets set -a <name> APP_SECRET=... VA_API_KEY=... VA_ALLOWED_EMAILS=... SMTP_*=...
fly deploy -a <name>
```

The engine must run 24/7 (it can't "scale to zero"), so expect a small always‑on cost. A spare always‑on machine at home (Windows/WSL or any Linux box) behind a Cloudflare Tunnel is a free alternative.

## Security notes

- Virgin passwords are stored **encrypted** (AES‑256‑GCM); `APP_SECRET` is mandatory (no insecure default).
- Login is **rate‑limited** per IP; registration is gated by an **email allowlist**.
- `VA_API_KEY` is an app‑level static key (anyone can extract it from the app); it lives in secrets, not in the repo.
- Session cookies are `HttpOnly` + `Secure` (under HTTPS) + `SameSite=Lax`, with a 30‑day TTL.
- No secrets are committed; `.env`, the SQLite DB and capture files are git‑ignored.

---
