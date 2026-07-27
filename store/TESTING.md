# Proving the apps work before submission

Nothing goes to a store until it has been installed and used on a real device.
A store rejection for "app does not function" costs days and counts against the
account, and both packages wrap a live site — so a broken deploy breaks every
installed copy at once.

Neither of these steps can be done from the build machine: installing an AAB
needs a phone, and installing an unsigned MSIX needs an interactive Windows
session. Both are quick.

---

## Android

The build produces an **AAB** for the Play Console. An AAB cannot be installed
directly on a phone, so testing uses either the debug APK or Play's internal
testing track.

### Option A — sideload the APK (fastest)

On LXC 171:

```bash
cd /opt/stream-installers
export JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64
export ANDROID_HOME=/opt/android-sdk
./node_modules/.bin/bubblewrap build --skipPwaValidation
```

That writes `app-release-signed.apk` alongside the AAB. Copy it to the phone
and install it (Settings → allow install from this source).

### Option B — Play internal testing

Upload the AAB to the **Internal testing** track. It reaches your own device in
minutes and is the same artifact that ships, so it proves the exact build.

### What to verify

- [ ] App opens with **no browser address bar** — if a URL bar shows, Digital
      Asset Links failed; check `https://stream.moveweight.com/.well-known/assetlinks.json`
      still lists the signing key's SHA-256
- [ ] Splash screen shows the app icon, not a white flash
- [ ] VPN notice appears on first run
- [ ] Discover rails load with poster artwork
- [ ] Search returns results
- [ ] **Playback actually starts** — this is the one that matters
- [ ] Adult filter is OFF by default (18+ chip unlit)
- [ ] Back button navigates within the app rather than closing it
- [ ] Rotate to landscape and confirm the grid reflows

### Known risk

Android WebView and Chrome differ on media handling. If playback works in
desktop Chrome but not in the TWA, the cause is almost certainly the service
worker that backs `file.streamURL` — check `chrome://inspect` against the
device and look for a service worker registration failure.

---

## Windows

The MSIX is a **hosted web app**: it has no local code, it points at the live
site. It must be signed to install; for testing, self-sign it.

```powershell
# Generate a test certificate (once)
New-SelfSignedCertificate -Type Custom -Subject "CN=6375D74B-5E4F-45B4-B246-B29507C1332A" `
  -KeyUsage DigitalSignature -FriendlyName "Stream Test" `
  -CertStoreLocation "Cert:\CurrentUser\My" `
  -TextExtension @("2.5.29.37={text}1.3.6.1.5.5.7.3.3", "2.5.29.19={text}")

# Sign the package (Subject must match the MSIX Identity Publisher exactly)
& "C:\Program Files (x86)\Windows Kits\10\bin\10.0.22621.0\x64\signtool.exe" sign `
  /fd SHA256 /a /n "Stream Test" Stream_1.0.0.0_neutral.msix

# Trust the test cert, then install
Add-AppxPackage .\Stream_1.0.0.0_neutral.msix
```

The test certificate is **only** for local verification. Partner Center signs
the real submission with the MOVE WEIGHT publisher identity; never ship a
self-signed build.

### What to verify

- [ ] App launches in its own window, not a browser tab
- [ ] Title bar shows "Stream", taskbar shows the app icon
- [ ] Discover rails load
- [ ] Search returns results
- [ ] **Playback actually starts**
- [ ] Window resizes down to ~800px wide without the layout breaking
- [ ] Closing and reopening restores cleanly

---

## Shared: verify the live site first

Both packages wrap `https://stream.moveweight.com`. If the site is broken, both
apps are broken, and a store review will catch it.

```bash
curl -s -o /dev/null -w "site      %{http_code}\n" https://stream.moveweight.com/
curl -s -o /dev/null -w "privacy   %{http_code}\n" https://stream.moveweight.com/privacy.html
curl -s -o /dev/null -w "manifest  %{http_code}\n" https://stream.moveweight.com/manifest.webmanifest
curl -s -o /dev/null -w "assetlink %{http_code}\n" https://stream.moveweight.com/.well-known/assetlinks.json
```

All four must return 200.

---

## Then, and only then

Work through the checklist at the end of `LISTING.md`, upload, and submit.
