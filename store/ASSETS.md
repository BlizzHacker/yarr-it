# Store assets

Everything in `assets/` is generated, not hand-made, so it can be regenerated
after any UI change without redoing work.

```bash
npm install                      # once
npx playwright install chromium  # once
node capture.mjs                 # app screenshots at device sizes
node graphics.mjs                # store banner graphics
```

## What is generated

| File | Size | Used for |
|---|---|---|
| `phone-{1..4}-*.png` | 1080×1920 | Play phone screenshots |
| `tablet7-{1..4}-*.png` | 1200×1920 | Play 7" tablet |
| `tablet10-{1..4}-*.png` | 1600×2560 | Play 10" tablet |
| `desktop-{1..4}-*.png` | 1920×1080 | Partner Center desktop |
| `winstore-{1..4}-*.png` | 1366×768 | Partner Center minimum size |
| `play-feature-graphic-1024x500.png` | 1024×500 | **Required** by Play |
| `ms-promo-2400x1200.png` | 2400×1200 | Partner Center promo |
| `ms-promo-1920x1080.png` | 1920×1080 | Partner Center promo |
| `../extension/icons/icon-{48,128}.png` | 48/128 | Chrome Web Store |
| `../web/dist/icon-{192,512}.png` | 192/512 | PWA / TWA launcher |

The four shots per device are, in order: **discover rails**, **search
results**, **filter bar**, **detail sheet with source picker** — the order a
reviewer scrolls, each showing a distinct selling point.

## Deliberate choices

**Screenshots are captured at native device resolution**, not resized. Both
stores accept downscaled images but they look soft next to competitors, and
Play rejects anything outside 320–3840px on either edge.

**The VPN banner is dismissed in the captures** via `localStorage` in an init
script. It still appears on a real first run — this only keeps it from eating
the top of every asset.

**The feature graphic uses abstract poster shapes, not real artwork.** Putting
actual film posters on a store banner would be publishing someone else's
copyrighted images as our own marketing, which is a different thing entirely
from showing them as search results inside the app.

## Still needed from you

- **Google Play** wants a 512×512 app icon uploaded separately in the console;
  `web/dist/icon-512.png` is the right file.
- **Partner Center** wants a 300×300 store logo; it can be generated from the
  same source if you want one that is not just the launcher icon scaled.
