import urllib.parse
"""Queue Yarr.It logo concepts on the ComfyUI box (VM 9000, RTX 3050 4GB).

SD 3.5 Large fp8 at 768x768 / 20 steps runs about 5 minutes on this card, so
everything is queued up front and ComfyUI drains it serially.
"""
import json, sys, time, urllib.request, os

HOST = "http://192.168.0.194:8188"
OUT = os.path.dirname(os.path.abspath(__file__))
CLIENT = "yarrit-logo"

NEG = ("text, letters, words, typography, watermark, signature, photograph, "
       "realistic, 3d render, noisy, cluttered, busy background, low contrast, "
       "drop shadow, bevel, multiple objects, frame, border")

PROMPTS = {
    "a_tricorn": (
        "flat vector logo, a pirate tricorn hat with a skull and crossbones badge on the "
        "front, bold simple geometric shapes, thick clean edges, symmetrical, centered, "
        "two colours only: bright teal and off-white on a flat black background, "
        "modern app icon, sharp silhouette, minimal"),
    "b_hat_play": (
        "flat vector app icon, a pirate tricorn hat sitting on top of a circular play "
        "button, minimal geometric design, bright teal and white on flat black, bold "
        "silhouette, clean thick shapes, centered, symmetrical, no gradients"),
    "c_silhouette": (
        "bold black silhouette emblem of a pirate tricorn hat, high contrast stencil, "
        "screen printed logo, crisp vector edges, single colour, flat teal background, "
        "centered, iconic, minimal, negative space"),
    "d_skull_hat": (
        "minimal vector logo of a skull wearing a pirate tricorn hat, bold flat shapes, "
        "thick outlines, teal white and black, centered emblem, sticker style, clean "
        "geometric, app icon"),
    "e_ornate": (
        "elegant emblem logo of an ornate pirate captain tricorn hat with gold trim and a "
        "feather plume, flat illustration, luxury badge, teal and gold on deep black, "
        "symmetrical, centered, crisp vector, minimal detail"),
    "f_geometric": (
        "extremely minimal geometric logo mark of a tricorn pirate hat, constructed from "
        "simple arcs and triangles, monoline, single weight, bright teal on black, "
        "centered, swiss design, corporate identity mark"),
}


def post(path, payload):
    req = urllib.request.Request(
        HOST + path, data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.load(r)


def graph(prompt, seed):
    return {
        "1": {"class_type": "CheckpointLoaderSimple",
              "inputs": {"ckpt_name": "sd3.5_large_fp8_scaled.safetensors"}},
        "2": {"class_type": "CLIPTextEncode", "inputs": {"clip": ["1", 1], "text": prompt}},
        "3": {"class_type": "CLIPTextEncode", "inputs": {"clip": ["1", 1], "text": NEG}},
        "4": {"class_type": "EmptySD3LatentImage",
              "inputs": {"width": 768, "height": 768, "batch_size": 1}},
        "5": {"class_type": "KSampler",
              "inputs": {"model": ["1", 0], "positive": ["2", 0], "negative": ["3", 0],
                         "latent_image": ["4", 0], "seed": seed, "steps": 20, "cfg": 4.5,
                         "sampler_name": "euler", "scheduler": "sgm_uniform", "denoise": 1.0}},
        "6": {"class_type": "VAEDecode", "inputs": {"samples": ["5", 0], "vae": ["1", 2]}},
        "7": {"class_type": "SaveImage",
              "inputs": {"images": ["6", 0], "filename_prefix": "yarrit"}},
    }


jobs = {}
for i, (name, prompt) in enumerate(PROMPTS.items()):
    r = post("/prompt", {"prompt": graph(prompt, 700100 + i * 37), "client_id": CLIENT})
    jobs[r["prompt_id"]] = name
    print("queued", name, r["prompt_id"], flush=True)

done = {}
while len(done) < len(jobs):
    time.sleep(20)
    for pid, name in jobs.items():
        if pid in done:
            continue
        try:
            with urllib.request.urlopen(f"{HOST}/history/{pid}", timeout=30) as r:
                h = json.load(r)
        except Exception:
            continue
        if pid not in h:
            continue
        st = h[pid].get("status", {})
        if not st.get("completed"):
            if st.get("status_str") == "error":
                done[pid] = None
                print("FAILED", name, json.dumps(st)[:500], flush=True)
            continue
        imgs = [i for o in h[pid]["outputs"].values() for i in o.get("images", [])]
        for img in imgs:
            q = urllib.parse.urlencode({"filename": img["filename"],
                                        "subfolder": img.get("subfolder", ""),
                                        "type": img.get("type", "output")})
            with urllib.request.urlopen(f"{HOST}/view?{q}", timeout=120) as r:
                data = r.read()
            path = os.path.join(OUT, f"{name}.png")
            open(path, "wb").write(data)
            print("saved", path, len(data), flush=True)
        done[pid] = True

print("all done", flush=True)
