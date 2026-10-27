"""Generate the four synthetic photo-like fixtures used by the corpus.

Everything here is drawn procedurally; no external photos are copied. The
derived variants intentionally exercise resize, crop, recompression and
contrast changes.
"""
from PIL import Image, ImageDraw, ImageEnhance, ImageFilter
import math
import os
import random
import shutil

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "testdata", "corpus")
os.makedirs(ROOT, exist_ok=True)
W, H = 1920, 1200


def vertical_gradient(size, top, bottom):
    w, h = size
    img = Image.new("RGB", size)
    d = ImageDraw.Draw(img)
    for y in range(h):
        t = y / max(1, h - 1)
        d.line(
            [(0, y), (w, y)],
            fill=tuple(round(top[i] * (1 - t) + bottom[i] * t) for i in range(3)),
        )
    return img


def glow(img, center, radius, color, strength=1.0):
    layer = Image.new("RGBA", img.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    for i in range(8, 0, -1):
        r = radius * i / 8
        alpha = int(18 * strength * (9 - i))
        d.ellipse(
            [center[0] - r, center[1] - r, center[0] + r, center[1] + r],
            fill=(color[0], color[1], color[2], alpha),
        )
    layer = layer.filter(ImageFilter.GaussianBlur(radius * 0.22))
    img.alpha_composite(layer)
    return img


def ridge(draw, y, amp, color, seed, step=70):
    rnd = random.Random(seed)
    pts = [(0, H)]
    x = 0
    while x <= W + step:
        pts.append((x, y + rnd.randint(-amp, amp)))
        x += step
    pts.extend([(W, H), (0, H)])
    draw.polygon(pts, fill=color)


def stars(img, count, seed, top=0, bottom=500):
    rnd = random.Random(seed)
    d = ImageDraw.Draw(img)
    for _ in range(count):
        x = rnd.randint(0, W)
        y = rnd.randint(top, bottom)
        r = rnd.choice((1, 1, 1, 2))
        alpha = rnd.randint(100, 220)
        d.ellipse([x, y, x + r, y + r], fill=(235, 242, 255, alpha))


def scene_aurora():
    img = vertical_gradient((W, H), (10, 20, 52), (126, 73, 137)).convert("RGBA")
    img = glow(img, (1450, 300), 280, (255, 194, 112), 1.2)
    stars(img, 180, 11, 0, 620)
    d = ImageDraw.Draw(img)
    for band in range(5):
        color = (24 + band * 9, 80 + band * 13, 118 + band * 10, 90)
        pts = []
        for x in range(0, W + 40, 40):
            y = 330 + band * 42 + math.sin(x / 180 + band) * 55 + math.sin(x / 70) * 12
            pts.append((x, y))
        for x in range(W, -1, -40):
            y = 330 + band * 42 + math.sin(x / 180 + band) * 55 + math.sin(x / 70) * 12 + 48
            pts.append((x, y))
        d.polygon(pts, fill=color)
    # Water band and reflection.
    water_top = 710
    d.rectangle([0, water_top, W, H], fill=(11, 39, 60, 255))
    d.ellipse([1290, 610, 1520, 700], fill=(232, 177, 105, 255))
    d.ellipse([1270, 640, 1540, 760], fill=(63, 91, 115, 255))
    ridge(d, water_top + 24, 50, (10, 31, 48, 255), 22, 110)
    ridge(d, water_top + 78, 30, (14, 42, 55, 255), 23, 90)
    layer = img.filter(ImageFilter.GaussianBlur(1.1))
    return layer


def scene_canyon():
    img = vertical_gradient((W, H), (55, 24, 76), (247, 151, 92)).convert("RGBA")
    img = glow(img, (1420, 255), 235, (255, 220, 148), 1.0)
    d = ImageDraw.Draw(img)
    # Layered mesas.
    rnd = random.Random(33)
    for layer, (y, col) in enumerate(
        [(640, (95, 51, 62, 255)), (760, (64, 36, 48, 255)), (900, (39, 25, 34, 255))]
    ):
        pts = [(0, H)]
        x = 0
        while x < W + 100:
            w = rnd.randint(90, 260)
            h = rnd.randint(50, 150)
            pts.extend([(x, y), (x + 20, y - h), (x + w - 20, y - h), (x + w, y)])
            x += w + rnd.randint(20, 80)
        pts.extend([(W, H), (0, H)])
        d.polygon(pts, fill=col)
    # A river catching the light.
    d.polygon(
        [(760, H), (820, 820), (1000, 760), (1100, H)],
        fill=(221, 160, 111, 255),
    )
    layer = img.filter(ImageFilter.GaussianBlur(0.5))
    return layer


def scene_lake():
    img = vertical_gradient((W, H), (23, 68, 74), (158, 205, 177)).convert("RGBA")
    img = glow(img, (420, 235), 250, (233, 248, 211), 0.9)
    d = ImageDraw.Draw(img)
    ridge(d, 520, 58, (31, 75, 68, 255), 44, 90)
    ridge(d, 620, 30, (21, 55, 55, 255), 45, 70)
    d.rectangle([0, 700, W, H], fill=(18, 68, 70, 255))
    # Tree silhouettes.
    rnd = random.Random(55)
    for _ in range(38):
        x = rnd.randint(0, W)
        y = rnd.randint(700, 830)
        h = rnd.randint(70, 190)
        d.line([(x, y), (x, y - h)], fill=(8, 29, 27, 255), width=rnd.randint(3, 7))
        for _ in range(3):
            d.ellipse(
                [x - rnd.randint(16, 38), y - h - rnd.randint(10, 45),
                 x + rnd.randint(16, 38), y - h + rnd.randint(15, 40)],
                fill=(14, 43, 37, 255),
            )
    # Reflected sun.
    d.ellipse([350, 760, 490, 790], fill=(208, 228, 174, 180))
    return img.filter(ImageFilter.GaussianBlur(0.6))


def scene_dunes():
    img = vertical_gradient((W, H), (36, 26, 66), (250, 189, 121)).convert("RGBA")
    img = glow(img, (1050, 330), 230, (255, 242, 190), 1.0)
    d = ImageDraw.Draw(img)
    rnd = random.Random(66)
    for i, col in enumerate([(160, 95, 107, 255), (117, 66, 85, 255), (75, 46, 65, 255)]):
        pts = [(0, H)]
        base = 650 + i * 120
        for x in range(0, W + 50, 50):
            y = base + math.sin(x / 260 + i * 1.7) * 55 + rnd.randint(-15, 15)
            pts.append((x, y))
        pts.extend([(W, H), (0, H)])
        d.polygon(pts, fill=col)
    # Wind ripples on the front dune.
    for i in range(24):
        y = 890 + i * 9
        d.arc([-100, y, W + 100, y + 80], 190, 350, fill=(58, 34, 49, 90), width=2)
    return img.filter(ImageFilter.GaussianBlur(0.3))


def add_grain(img, seed):
    rnd = random.Random(seed)
    px = img.convert("RGB").load()
    for _ in range(9000):
        x, y = rnd.randrange(W), rnd.randrange(H)
        r, g, b = px[x, y]
        n = rnd.randint(-8, 8)
        px[x, y] = (max(0, min(255, r + n)), max(0, min(255, g + n)), max(0, min(255, b + n)))
    return img


def save(img, name, quality=90):
    img.convert("RGB").save(os.path.join(ROOT, name), quality=quality, optimize=True)


base1 = add_grain(scene_aurora(), 1)
save(base1, "photo_1.jpg", 92)
w, h = base1.size
base1.convert("RGB").resize((w // 2, h // 2), Image.Resampling.LANCZOS).save(
    os.path.join(ROOT, "photo_1_half.jpg"), quality=82, optimize=True
)
base1.convert("RGB").crop((int(w * 0.04), int(h * 0.03), int(w * 0.96), int(h * 0.97))).save(
    os.path.join(ROOT, "photo_1_crop.jpg"), quality=88, optimize=True
)
base1.convert("RGB").save(os.path.join(ROOT, "photo_1_low.jpg"), quality=40, optimize=True)
ImageEnhance.Contrast(base1.convert("RGB")).enhance(1.12).save(
    os.path.join(ROOT, "photo_1_contrast.jpg"), quality=90, optimize=True
)

base2 = add_grain(scene_canyon(), 2)
save(base2, "photo_2.jpg", 90)
shutil.copyfile(os.path.join(ROOT, "photo_2.jpg"), os.path.join(ROOT, "photo_2_copy.jpg"))

save(add_grain(scene_lake(), 3), "photo_3.jpg", 90)
save(add_grain(scene_dunes(), 4), "photo_4.jpg", 90)
print("generated synthetic photo fixtures")
