"""Push the Yarr.It mark out to every app that carries an icon.

One master (`yarrit-mark-1024.png`) feeds all of them, so a brand change is a
single re-run rather than a hunt through five directories.

Two different fits are needed and getting them backwards looks broken:
  - ICONS are square and the mark fills them edge to edge, because the OS
    already applies its own rounded mask.
  - SPLASH and BANNER art is wide, so the mark is placed on the dark ground at
    a modest size rather than stretched to the frame.
"""
import os
from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
STREAM = os.path.dirname(HERE)
GROUND = (0x0b, 0x0d, 0x11)

master = Image.open(os.path.join(HERE, "yarrit-mark-1024.png")).convert("RGB")

SQUARE = [
    ("web/dist/icon-192.png", 192),
    ("web/dist/icon-512.png", 512),
    ("extension/icons/icon-48.png", 48),
    ("extension/icons/icon-128.png", 128),
    # MSIX tiles. These used to be drawn procedurally inside build_msix.mjs as a
    # bare teal triangle, which meant the Windows app was the one client not
    # carrying the actual mark.
    ("installers/msix-assets/StoreLogo.png", 50),
    ("installers/msix-assets/Square44x44Logo.png", 44),
    ("installers/msix-assets/Square150x150Logo.png", 150),
    # Android launcher icons. Bubblewrap bakes these in at project-creation
    # time from the site icon, so they do not follow a rebrand on their own.
    ("installers/android-assets/mipmap-mdpi/ic_launcher.png", 48),
    ("installers/android-assets/mipmap-hdpi/ic_launcher.png", 72),
    ("installers/android-assets/mipmap-xhdpi/ic_launcher.png", 96),
    ("installers/android-assets/mipmap-xxhdpi/ic_launcher.png", 144),
    ("installers/android-assets/mipmap-xxxhdpi/ic_launcher.png", 192),
    # Roku's focus icon is not square; it is letterboxed below instead.
]

# (path, width, height, how much of the short edge the mark occupies)
FITTED = [
    ("roku/images/icon_focus_hd.png", 290, 218, 0.92),
    ("roku/images/icon_focus_sd.png", 214, 144, 0.92),
    ("roku/images/splash_fhd.png", 1920, 1080, 0.42),
    ("roku/images/splash_hd.png", 1280, 720, 0.42),
    ("roku/images/splash_sd.png", 720, 480, 0.42),
    ("installers/msix-assets/SplashScreen.png", 620, 300, 0.62),
    ("installers/android-assets/tv-banner.png", 320, 180, 0.86),
    # Android splash, one per density. Bubblewrap derives these from the site
    # icon at project-creation time, so a rebrand leaves the OLD art on the
    # launch screen while the launcher icon is already correct -- which looks
    # exactly like a failed rebrand.
    ("installers/android-assets/drawable-mdpi/splash.png", 300, 300, 0.62),
    ("installers/android-assets/drawable-hdpi/splash.png", 450, 450, 0.62),
    ("installers/android-assets/drawable-xhdpi/splash.png", 600, 600, 0.62),
    ("installers/android-assets/drawable-xxhdpi/splash.png", 900, 900, 0.62),
    ("installers/android-assets/drawable-xxxhdpi/splash.png", 1200, 1200, 0.62),
]


def write(rel, img):
    path = os.path.join(STREAM, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    img.save(path)
    print(f"  {rel}  {img.size[0]}x{img.size[1]}")


print("square icons:")
for rel, size in SQUARE:
    write(rel, master.resize((size, size), Image.LANCZOS))

print("fitted art:")
for rel, w, h, frac in FITTED:
    side = int(min(w, h) * frac)
    canvas = Image.new("RGB", (w, h), GROUND)
    tile = master.resize((side, side), Image.LANCZOS)
    canvas.paste(tile, ((w - side) // 2, (h - side) // 2))
    write(rel, canvas)

print("done")
