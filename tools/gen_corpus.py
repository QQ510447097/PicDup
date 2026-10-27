from PIL import Image, ImageDraw, ImageEnhance
import os, shutil, random, math

root = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'testdata', 'corpus')
os.makedirs(root, exist_ok=True)
random.seed(42)

def scene(seed, w=1200, h=800):
    img = Image.new('RGB', (w, h), (20, 30, 50))
    d = ImageDraw.Draw(img)
    top = (random.randint(10, 60), random.randint(20, 80), random.randint(60, 120))
    bottom = (random.randint(150, 240), random.randint(90, 170), random.randint(60, 120))
    for y in range(h):
        t = y / h
        d.line([(0, y), (w, y)], fill=tuple(int(top[i]*(1-t)+bottom[i]*t) for i in range(3)))
    sunx, suny = random.randint(100, w-100), random.randint(80, h//3)
    r = random.randint(50, 110)
    d.ellipse([sunx-r, suny-r, sunx+r, suny+r], fill=(random.randint(220,255), random.randint(170,230), random.randint(80,160)))
    for layer in range(3):
        base = h//2 + layer*70
        pts = [(0, h), (0, base)]
        x = 0
        while x < w:
            x += random.randint(60, 160)
            pts.append((x, base + random.randint(-90, 40)))
        pts += [(w, base), (w, h)]
        shade = 25 + layer*22
        d.polygon(pts, fill=(shade, shade+18, shade+35))
    for _ in range(12):
        x = random.randint(0, w); y = random.randint(h//2, h)
        hh = random.randint(30, 120)
        d.line([(x, y), (x, y-hh)], fill=(30, 40, 35), width=4)
        d.ellipse([x-20, y-hh-20, x+20, y-hh+20], fill=(20+random.randint(0,40), 70+random.randint(0,60), 40))
    img = Image.effect_noise((w, h), 6).convert('RGB')
    return img

# Six distinct scenes.
bases = []
for i in range(6):
    random.seed(100+i)
    # make a custom scene with seed dependent on i
    img = Image.new('RGB', (1200, 800))
    d = ImageDraw.Draw(img)
    c1 = [(20,40,90),(70,20,80),(10,70,60),(90,60,20),(30,30,40),(5,55,95)][i]
    c2 = [(240,170,90),(230,90,140),(250,210,110),(120,180,220),(200,200,210),(190,150,240)][i]
    for y in range(800):
        t = y/800
        d.line([(0,y),(1200,y)], fill=tuple(int(c1[k]*(1-t)+c2[k]*t) for k in range(3)))
    # random geometric photo-like structure
    rnd = random.Random(i*7+3)
    for k in range(8):
        x0 = rnd.randint(0,1100); y0 = rnd.randint(100,700)
        x1 = x0 + rnd.randint(50,350); y1 = y0 + rnd.randint(40,260)
        col = (rnd.randint(0,255), rnd.randint(0,255), rnd.randint(0,255))
        if k % 2:
            d.ellipse([x0,y0,x1,y1], fill=col, outline=(255,255,255), width=3)
        else:
            d.rectangle([x0,y0,x1,y1], fill=col, outline=(255,255,255), width=3)
    for k in range(3000):
        x = rnd.randint(0,1199); y = rnd.randint(0,799)
        v = rnd.randint(-20,20)
        p = img.getpixel((x,y))
        img.putpixel((x,y), tuple(max(0,min(255,p[j]+v)) for j in range(3)))
    bases.append(img)

for i, img in enumerate(bases):
    img.save(os.path.join(root, f'scene_{i+1}.jpg'), quality=92)

# Exact duplicates of scene 1 (different folders).
os.makedirs(os.path.join(root, 'backup'), exist_ok=True)
shutil.copy(os.path.join(root, 'scene_1.jpg'), os.path.join(root, 'scene_1_copy.jpg'))
shutil.copy(os.path.join(root, 'scene_1.jpg'), os.path.join(root, 'backup', 'scene_1_backup.jpg'))
shutil.copy(os.path.join(root, 'scene_2.jpg'), os.path.join(root, 'backup', 'scene_2_copy.jpg'))

# Similar variants of scene 3.
base = bases[2]
base.resize((600, 400), Image.LANCZOS).save(os.path.join(root, 'scene_3_small.jpg'), quality=85)
base.save(os.path.join(root, 'scene_3_low.jpg'), quality=35)
ImageEnhance.Brightness(base).enhance(1.15).save(os.path.join(root, 'scene_3_bright.jpg'), quality=88)
w, h = base.size
base.crop((60, 40, w-60, h-40)).save(os.path.join(root, 'scene_3_crop.jpg'), quality=88)
base.rotate(90, expand=True).save(os.path.join(root, 'scene_3_rot.jpg'), quality=88)

# Similar variants of scene 5.
base = bases[4]
base.resize((900, 600), Image.LANCZOS).save(os.path.join(root, 'scene_5_resize.png'))
base.convert('L').convert('RGB').save(os.path.join(root, 'scene_5_gray.jpg'), quality=85)

# A couple of screenshots / flat images.
for i, color in enumerate([(28,32,44),(240,240,240)]):
    im = Image.new('RGB', (1000, 700), color)
    d = ImageDraw.Draw(im)
    d.rectangle([60,60,940,120], fill=(90,120,200))
    for j in range(6):
        d.rectangle([60,160+j*80,940,210+j*80], fill=tuple(min(255,c+30) for c in color))
    im.save(os.path.join(root, f'screenshot_{i+1}.png'))

print('corpus:', root)
for f in sorted(os.listdir(root)):
    print(' ', f)
