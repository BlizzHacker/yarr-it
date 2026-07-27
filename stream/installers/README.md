# Stream installers

All four packages wrap the **live site**, so a `deploy.sh` on the web app updates
every installed client with no store resubmission.

| Target | Artifact | Built by |
|---|---|---|
| Chrome / Edge extension | `stream-extension-1.0.0.zip` | `../extension/package.sh` |
| Android (Play) | `Stream_1.0.0.aab` | Bubblewrap TWA on LXC 171 |
| Android (sideload / testing) | `Stream_1.0.0.apk` | same build |
| Windows + Xbox | `Stream_1.0.0.0_neutral.msix` | `build_msix.mjs` |

## Identity — do not change these

Both store identities are tied to existing accounts. Changing either makes the
upload a *different app* and forfeits the listing.

**Windows / Xbox** — same Partner Center publisher as Cryptic Realm:

```
Name                 MOVEWEIGHT.Stream
Publisher            CN=6375D74B-5E4F-45B4-B246-B29507C1332A
PublisherDisplayName MOVE WEIGHT
```

**Android**

```
packageId   com.moveweight.stream
keystore    /opt/stream-installers/stream-upload.keystore   (on LXC 171)
alias       stream
SHA-256     CD:FE:97:8E:67:D7:EF:65:1F:34:F6:68:BF:92:24:CF:
            6A:72:99:7B:F8:92:16:C6:51:41:FB:8A:CB:23:2B:98
```

> **Back up that keystore off-machine.** Lose it and the Play listing can never
> be updated again — a new package name would be the only way forward.
> Passwords are in `/opt/stream-installers/keystore.properties`, mode 600.

## Android: the assetlinks requirement

The TWA only opens without a browser URL bar if the site proves it owns the app.
`https://stream.moveweight.com/.well-known/assetlinks.json` carries the SHA-256
above and is already live. **If the signing key ever changes, that file must
change with it**, or every installed app silently degrades to a Chrome tab.

Verified: the built APK's certificate digest matches the published fingerprint.

## Rebuilding

```bash
# Extension
cd ../extension && bash package.sh

# Windows / Xbox
node build_msix.mjs                       # STREAM_VERSION=1.0.1.0 to bump

# Android (on LXC 171)
ssh root@192.168.0.6
pct exec 171 -- bash -lc '
  cd /opt/stream-installers
  export JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64 ANDROID_HOME=/opt/android-sdk
  PASS=$(grep storePassword keystore.properties | cut -d= -f2)
  export BUBBLEWRAP_KEYSTORE_PASSWORD="$PASS" BUBBLEWRAP_KEY_PASSWORD="$PASS"
  printf "n\n" | ./node_modules/.bin/bubblewrap build --skipPwaValidation'
```

Bump `appVersionCode` in `twa-manifest.json` before every Play upload; Play
rejects a version code it has already seen.

### Two setup traps, already worked around

- Bubblewrap validates the SDK by looking for `<sdk>/tools` or `<sdk>/bin`, but
  modern SDKs put those under `cmdline-tools/latest`. A symlink
  `/opt/android-sdk/bin -> cmdline-tools/latest/bin` satisfies it.
- The CLI prompts for passwords unless `BUBBLEWRAP_KEYSTORE_PASSWORD` and
  `BUBBLEWRAP_KEY_PASSWORD` are exported, and prompts to regenerate the project
  unless fed `n`.

## Submission — a human step, deliberately

Nothing here uploads anything. Each store needs an account-holder decision:

- **Chrome Web Store** — upload the zip; one-time $5 registration.
- **Play Console** — upload the `.aab` to a track. First release requires the
  data-safety form and a content rating questionnaire.
- **Partner Center** — upload the `.msix` under a new product using the identity
  above. The Store signs it; the package is intentionally unsigned.

### Worth knowing before submitting

A torrent-streaming client is a policy-sensitive listing. Google Play and
Microsoft both allow general-purpose torrent clients, but reviewers reject apps
that appear to *curate* infringing content. The app's own framing matters: it
ships as a search-and-play tool with no bundled catalogue, and the VPN/privacy
notice is on the landing page. Expect review questions and be ready to describe
it as a client, not a content service.
