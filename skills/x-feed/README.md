# Backlight x-feed Skill Vendor

This directory vendors the public-posting guidance used by Backlight's
structured X/Telegram incident publisher. Runtime code reads the incident
format path as a readiness check and records it in `x-feed-status.json`.

When `HELIOS_X_FEED_CARD_ENABLED=true`, Backlight also calls the vendored
`skills/exploit-flow-card/scripts/render_card.py` script to create a public-safe
SVG/PNG card from `x-feed-card-brief.md`. X media upload uses the generated PNG
when available; PNG conversion requires `cairosvg` in the configured Python
environment.
