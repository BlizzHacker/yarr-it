"""Recolour the chosen SD logo onto the Yarr.It palette and centre it.

The generation came out as black artwork on a white disc on a teal field. The
brand is the other way round: dark ground, teal accent, artwork knocked out of
it. So the three tones are remapped rather than redrawn --

    teal field -> #0b0d11 (ground)
    white disc -> teal    (the roundel)
    black art  -> #0b0d11 (skull and tricorn, same as ground)

which also means the skull reads as negative space cut out of the roundel,
exactly like the hand-drawn attempt was trying to do.
"""
import numpy as np
from PIL import Image, ImageFilter

SRC = "d_skull_hat.png"
DST = "yarrit-mark-1024.png"

GROUND = np.array([0x0b, 0x0d, 0x11], dtype=np.float32)
TEAL_A = np.array([0x5e, 0xea, 0xd4], dtype=np.float32)  # top-left of the gradient
TEAL_B = np.array([0x22, 0xc5, 0xb0], dtype=np.float32)  # bottom-right

img = Image.open(SRC).convert("RGB")
a = np.asarray(img).astype(np.float32) / 255.0

# Luminance separates the three tones cleanly: the black art is dark, the white
# disc is bright, and the teal field sits in between but is far more saturated.
lum = a @ np.array([0.299, 0.587, 0.114], dtype=np.float32)
sat = a.max(axis=2) - a.min(axis=2)

is_disc = (lum > 0.72) & (sat < 0.22)     # white disc
is_art = lum < 0.35                        # black skull + hat

h, w = lum.shape
yy, xx = np.mgrid[0:h, 0:w]
t = ((xx / w) * 0.5 + (yy / h) * 0.5)[..., None]
teal = TEAL_A * (1 - t) + TEAL_B * t

# Keep the original's tonal relationship: dark artwork on a light ground. The
# first attempt mapped the white disc to teal and left the art dark, which put
# the black tricorn at exactly the background colour -- so the hat, which
# overhangs the disc, disappeared into it. Everything that is not artwork
# becomes teal instead, which drops the disc entirely and leaves a clean
# silhouette.
out = teal.copy()
out[is_art] = GROUND

# Build the mark by compositing the artwork through its own mask onto a full
# canvas of teal. Pasting a cropped RECTANGLE instead leaves a visible seam,
# because the crop carries its own slice of the gradient and does not line up
# with the gradient underneath it.
soft = Image.fromarray((is_art * 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(0.7))
bbox = soft.getbbox()
soft = soft.crop(bbox)

SIDE = 1024
scale = (SIDE * 0.80) / max(soft.size)
soft = soft.resize((max(1, int(soft.width * scale)), max(1, int(soft.height * scale))), Image.LANCZOS)

gy, gx = np.mgrid[0:SIDE, 0:SIDE]
gt = ((gx / SIDE) * 0.5 + (gy / SIDE) * 0.5)[..., None]
bg = (TEAL_A * (1 - gt) + TEAL_B * gt).astype(np.uint8)
canvas = Image.fromarray(bg)

ink = Image.new("RGB", (SIDE, SIDE), tuple(GROUND.astype(int)))
pos = ((SIDE - soft.width) // 2, (SIDE - soft.height) // 2)
full = Image.new("L", (SIDE, SIDE), 0)
full.paste(soft, pos)
canvas = Image.composite(ink, canvas, full)
canvas.save(DST)
print(f"{DST}  art bbox {bbox}  placed at {pos}  size {soft.size}")
