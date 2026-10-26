# nuevo-rss

A Bloomberg-terminal-style news dashboard on the web, fed by RSS.

## Why
Kareem wants a project that is a real technical challenge and could maybe make money later.
It was picked on 2026-10-24 over an AI customer-messaging tool (like Keiki), which had too much competition.
It is a separate project, not part of kmufti-hub.

## The screen
Dark and dense, split into panels, with a keyboard command bar (type `TECH <GO>` like Bloomberg).

- **Live ticker:** headlines scrolling across the top.
- **Feed panels:** World, Tech, Markets, Middle East, Sports, etc. Users can set up their own panels and feeds.
- **Breaking alerts:** a story showing up in many feeds at once gets flagged.
- **Story clustering:** many outlets covering the same event become one row, with a source count.
- **Market strip:** indexes, oil, gold, BTC and currency rates from free APIs.
- **Keyboard first:** `j`/`k` to move, `/` to search, number keys to switch panels.

## Technical pieces
1. **Feed fetcher (backend):** browsers can't read most RSS feeds directly (CORS), so a server checks ~50-200 feeds every few minutes, removes duplicates and stores the stories.
2. **Live updates:** push new stories to the page with Server-Sent Events or WebSockets, no refresh needed.
3. **Clustering:** group similar headlines, first by word overlap, later with AI embeddings.
4. **Fast UI:** lots of rows that stay smooth when scrolling.

## Later
- AI layer: one-line summaries, a "brief me on the last hour" button, topic/sentiment tags.
- Ways to make money: free version plus paid alerts ("text me when X is in the news"), AI briefings, or a niche edition (Middle East news, crypto, one industry).

## Suggested first step
Build the feed fetcher with a starter list of feeds, then a first terminal screen showing live headlines in panels.
