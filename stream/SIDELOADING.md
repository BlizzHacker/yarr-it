# Sideloading Yarr.It

Every build is on the [Releases page](https://github.com/BlizzHacker/yarr-it/releases).
None of them are in an app store yet, and some of them never will be — see
[Will these ever be in the stores?](#will-these-ever-be-in-the-stores) at the
bottom. Sideloading is the supported path, not a workaround.

| Platform | Artifact | Store status |
|---|---|---|
| [Roku](#roku) | `Yarr.It-roku.zip` | Sideload only — realistically permanent |
| [Google TV / Android TV](#google-tv--android-tv) | `Yarr.It-tv-1.0.0.apk` | Not submitted |
| [Android phone / tablet](#android-phone--tablet) | `Yarr.It-1.0.0.apk` | Play submission prepared |
| [Windows 10/11 + Xbox](#windows-1011--xbox) | `Yarr.It_1.0.0.0_neutral.msix` | Partner Center submission prepared |
| [Chrome / Edge / Brave](#chrome--edge--brave) | `yarrit-extension-1.0.0.zip` | Not submitted |

**Verify what you downloaded.** Every release has a `SHA256SUMS.txt`. On Windows:

```bash
Get-FileHash .\Yarr.It-1.0.0.apk -Algorithm SHA256
```

On macOS or Linux:

```bash
shasum -a 256 Yarr.It-1.0.0.apk
```

---

## Roku

Roku has no browser engine, no WebRTC and no usable socket API, so the channel
cannot join a swarm itself. It browses through the public search API and asks
the **LAN gateway** to turn a magnet into an HTTP stream it can play. The
gateway must be reachable from the TV — see [gateway/README.md](gateway/).

### 1. Put the Roku into developer mode

On the Roku remote, from the home screen, press:

```
Home Home Home  Up Up  Right Left Right Left Right
```

A developer settings screen appears. Enable it, accept the licence, and **set a
developer password**. The device reboots.

That password is not your Roku account password, and it is not recoverable —
the device only stores a hash. To change it, re-run the same button sequence.

### 2. Find the TV's IP address

`Settings → Network → About`, or the Roku mobile app.

### 3. Install

Either use the script:

```bash
ROKU_PASS=<dev password> ./roku/sideload.sh 192.168.1.50
```

Or do it by hand: open `http://<roku-ip>/` in a browser, log in as user
`rokudev` with the developer password, choose `Yarr.It-roku.zip`, and click
**Install**. A successful upload answers `Application Received`.

### 4. If it fails

The dev server returns HTML, so the script extracts the one useful line.
Compile errors only appear in the device log:

```bash
telnet <roku-ip> 8085
```

Common causes, all of which have actually happened here:

- **`Install Failure: Compilation Failed`** — a BrightScript error. The log
  names the file and line.
- **Channel installs but shows a blank screen** — the zip was built with
  backslash path separators, so `pkg:/components/...` does not resolve. Rebuild
  with `./roku/package.sh`, which asserts against this.
- **`Identical to previous version`** — harmless; bump `build_version` in
  `roku/manifest` if you want a clean reinstall.

### 5. Rebuilding after a change

```bash
cd roku && ./package.sh && ROKU_PASS=<dev password> ./sideload.sh <roku-ip>
```

### Removing it

`Settings → System → Advanced system settings → Developer options`, or simply
uninstall the channel from the home screen like any other.

---

## Google TV / Android TV

Use `Yarr.It-tv-1.0.0.apk`, **not** the phone APK. The phone build declares no
`LEANBACK_LAUNCHER` intent, so Android TV installs it happily and then never
shows it in the launcher — you end up needing a third-party app drawer to start
it. The TV build declares the leanback launcher category, marks touchscreen
optional, and ships a TV banner.

### 1. Allow installs

`Settings → System → About` and click **Android TV OS build** seven times to
unlock Developer options. Then:

- `Settings → System → Developer options → USB debugging` → **On**
- `Settings → Apps → Security & restrictions → Unknown sources` → enable for
  whichever app you will install from

### 2a. Install over the network with adb

Find the IP under `Settings → Network & Internet → <your network>`.

```bash
adb connect 192.168.1.60:5555
adb install -r Yarr.It-tv-1.0.0.apk
```

The TV shows an **Allow USB debugging?** prompt the first time — accept it on
the TV, then re-run `adb connect`. If `adb connect` refuses, open
`Developer options → Wireless debugging` and use the port shown there instead
of 5555.

### 2b. Install without a computer

Install **Downloader** (by AFTVnews) from the Play Store on the device, enter
the release URL of the TV APK, and it will download and hand off to the package
installer.

### 3. Launch

It appears in the Google TV launcher as **Yarr.It**. Navigation is D-pad driven.

### 4. Uninstall

```bash
adb uninstall com.moveweight.stream
```

### Known limitation

Like Roku, a TV cannot join a BitTorrent swarm directly, so this build also
plays through the LAN gateway. On a TV with no gateway reachable, search and
browsing work but playback will not start.

---

## Android phone / tablet

1. Download `Yarr.It-1.0.0.apk` from Releases.
2. Android will warn that the browser is not allowed to install apps. Tap
   **Settings** in that prompt and enable **Allow from this source**.
3. Open the downloaded file and install.
4. Play Protect may show *Unsafe app blocked* or *App scan recommended* — that
   is Android reporting the app is not from a store it recognises, not a
   detection. **Install anyway** if you trust the source.

This build is a Trusted Web Activity: it wraps the live site and verifies
ownership via `https://yarrit.com/.well-known/assetlinks.json`. It
runs without a browser URL bar because the site's published SHA-256 matches the
APK's signing certificate.

Uninstall like any other app, or `adb uninstall com.moveweight.stream`.

---

## Windows 10/11 + Xbox

The package is signed with a **self-signed** certificate because it has not been
through the Microsoft Store. Windows will not install a package whose signature
does not chain to a trusted root, so the certificate must be trusted first.

> **This is a real security decision.** Adding the certificate to Trusted Root
> Certification Authorities means the machine will trust *anything* signed with
> that private key until you remove it. Only do this if you trust whoever built
> the package, and undo it when you finish testing. The removal commands are
> printed at the end of the install.

### Install

Download `Yarr.It_1.0.0.0_neutral.msix`, `Yarr.It-Sideload.cer` and
`Install-Windows.ps1` into the same folder. Then, in an **Administrator**
PowerShell:

```bash
Set-ExecutionPolicy -Scope Process Bypass -Force
.\Install-Windows.ps1
```

The script prints the certificate's subject, thumbprint and expiry, and waits
for you to type `YES` before trusting anything.

### If you see 0x800B0109

```
A certificate chain processed, but terminated in a root certificate
which is not trusted by the trust provider.
```

You are running an older copy of the script that only imported the certificate
into **TrustedPeople**. That store is the documented one for sideloaded
packages, but it only works when the certificate chains to a root Windows
already trusts. This certificate is self-signed — its Subject and Issuer are
identical — so it *is* its own root, and the chain terminated in nothing.
Download the current `Install-Windows.ps1`, which imports into **both** `Root`
and `TrustedPeople`.

### Uninstall, including withdrawing the trust

```bash
Get-AppxPackage *MOVEWEIGHT.Stream* | Remove-AppxPackage
```

Then remove the certificate from both stores using the thumbprint the installer
printed:

```bash
Remove-Item Cert:\LocalMachine\Root\<thumbprint>
```

```bash
Remove-Item Cert:\LocalMachine\TrustedPeople\<thumbprint>
```

### Xbox

The manifest targets `Windows.Xbox`, but Xbox consoles only sideload in
**Developer Mode**, which requires a registered developer account and converts
the console into a dev kit. That is out of scope for testing; the Xbox target
exists so a single Store submission covers both.

---

## Chrome / Edge / Brave

The extension hijacks magnet links, injects a **Stream** button on torrent
sites, and adds an omnibox keyword. It is not on the Web Store, so it loads
unpacked.

1. Download `yarrit-extension-1.0.0.zip` and **extract it** — Chrome cannot load
   a zip directly.
2. Open `chrome://extensions` (or `edge://extensions`, `brave://extensions`).
3. Turn on **Developer mode**, top right.
4. Click **Load unpacked** and select the extracted folder — the one containing
   `manifest.json`, not its parent.

Chrome shows *Disable developer mode extensions* on every restart. That warning
is unavoidable for unpacked extensions and does not indicate a problem.

To update, replace the folder contents and click the reload icon on the
extension's card.

---

## Will these ever be in the stores?

Honestly, some will not.

- **Roku** does not accept channels that stream arbitrary user-supplied
  BitTorrent content. Sideloading is the realistic permanent answer, and a
  sideloaded channel keeps working indefinitely — it does not expire.
- **Google Play** and the **Microsoft Store** submissions are prepared and the
  packages are store-valid, but approval is not something anyone can promise for
  an app in this category.
- **The Chrome Web Store** prohibits extensions that facilitate unauthorised
  access to copyrighted material, and that judgement is theirs to make.

This is exactly why every client works standalone: each app talks to a
configurable API endpoint and falls back to bundled defaults, so nothing here
depends on a store listing — or on `yarrit.com` staying up.

---

## Nothing here is a downloader

Yarr.It plays; it does not save. It hosts no media, indexes nothing itself, and
stores nothing on any server. It is a client for public indexes and
peer-to-peer networks the way a browser is a client for websites.

**Use a VPN.** Peer-to-peer means your IP address is visible to other peers,
exactly as with any BitTorrent client. That is inherent to the protocol, not a
choice made here. You are responsible for complying with the laws where you
live.
