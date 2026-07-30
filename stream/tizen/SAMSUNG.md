# Samsung / Tizen — what only you can do

Everything in this directory is finished and valid: `config.xml` (already on
yarrit.com and auth.yarrit.com), `index.html`, `icon.png`, and `build.sh` which
packages and signs in one command.

What is missing is a **Samsung certificate**, and Samsung will only issue one to
a signed-in human. That is the whole blocker — not the code.

---

## Why a certificate is unavoidable

A `.wgt` is a zip, so anyone can build one. But a Samsung TV refuses to install
a package unless it carries two signatures:

- an **author certificate** — proves who wrote it
- a **distributor certificate** — issued by Samsung, tied to your account

Both come out of Tizen Studio's Certificate Manager, which opens a Samsung
account sign-in window to get them. That sign-in is the step I cannot do for
you. It applies even to sideloading onto your own television.

---

## Step 1 — Samsung account (2 min, free)

https://account.samsung.com/accounts/v1/MBR/signUp

Any email. This is the same account type as a Samsung phone or TV; if you
already have one, skip.

---

## Step 2 — Tizen Studio

**https://download.tizen.org/sdk/Installer/tizen-studio_6.1/**

Two choices in that directory:

| Installer | Size | Use |
|---|---|---|
| `web-cli_Tizen_Studio_6.1_windows-64.exe` | small | **Recommended.** CLI + Package Manager, which is all `build.sh` needs |
| `web-ide_Tizen_Studio_6.1_windows-64.exe` | large | Full IDE, if you ever want to edit visually |

Install to the default location (`C:\tizen-studio`). Tick **"Launch the Package
Manager"** at the end.

---

## Step 3 — two extensions in Package Manager

Open the **Extension SDK** tab and install both:

- **TV Extensions** (whatever the current version is)
- **Samsung Certificate Extension** — must be **2.0.73 or newer**; Samsung
  disabled certificate creation in older ones as of September 2025

---

## Step 4 — the certificate

**Tools → Certificate Manager → + → Samsung → TV**

- Profile name: **`YarritTV`** ← must match exactly; `build.sh` passes this to
  the signer
- It opens a Samsung sign-in — this is the step that needs you
- Choose **Create a new author certificate**, set a password, keep it safe
- For the distributor certificate it asks for **DUIDs** — the device IDs of TVs
  allowed to run the build. Your TV's DUID is on the set:
  **Menu → Support → About This TV**, or the Device Manager in Tizen Studio
  once the TV is in developer mode.

---

## Step 5 — hand it back to me

Once the profile exists, one command:

```
TIZEN_HOME=/c/tizen-studio bash build.sh
```

That builds, signs with `YarritTV`, and writes
`../installers/dist/Yarr.It-tizen-1.0.0.wgt`. Tell me when the profile is made
and I will run it, verify the package and add it to the GitHub release
alongside the other artifacts.

---

## Putting it on your own TV

1. TV: **Apps → press 1,2,3,4,5 on the remote → Developer mode ON**, enter your
   PC's IP, restart the TV
2. Then I can install it over the network with
   `tizen install -n Yarr.It-tizen-1.0.0.wgt -t <tv>`

---

## Publishing to the Samsung TV store — separate, later

**https://seller.samsungapps.com/**

A different account from the developer one, and it wants business details and a
tax/banking profile. Worth doing *after* the foundation's 501(c)(3) letter
arrives, since some of that paperwork asks what kind of entity you are.

Sideloading needs none of it.

---

## Summary

| | |
|---|---|
| You | Samsung account, install Tizen Studio, create the `YarritTV` profile |
| Me | everything else — build, sign, verify, release, install to the TV |

Realistically 20 minutes, most of it waiting on a download.
