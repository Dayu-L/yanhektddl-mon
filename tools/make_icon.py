# -*- coding: utf-8 -*-
"""生成 app.ico:白底圆角方块 + 品牌蓝时钟(与应用内标记一致),多尺寸"""
from PIL import Image, ImageDraw
import os

BLUE = (29, 95, 191, 255)      # #1d5fbf
WHITE = (255, 255, 255, 255)
BORDER = (217, 222, 229, 255)  # #d9dee5

def draw_icon(size, ss=8):
    S = size * ss
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    # 圆角方块
    r = S * 0.22
    d.rounded_rectangle([0, 0, S - 1, S - 1], radius=r, fill=WHITE, outline=BORDER, width=max(1, S // 90))

    # 表盘
    cx = cy = S / 2
    ring_r = S * 0.315
    stroke = S * 0.055
    d.ellipse([cx - ring_r, cy - ring_r, cx + ring_r, cy + ring_r], outline=BLUE, width=int(stroke))

    # 指针(指向 ~11:59,离零点最近的时刻 = 截止)
    hand_w = int(stroke)
    d.line([cx, cy, cx, cy - ring_r * 0.62], fill=BLUE, width=hand_w)
    ang_x = cx + ring_r * 0.42 * 0.94
    ang_y = cy + ring_r * 0.42 * 0.34
    d.line([cx, cy, ang_x, ang_y], fill=BLUE, width=hand_w)
    # 中心点
    dot = stroke * 0.9
    d.ellipse([cx - dot, cy - dot, cx + dot, cy + dot], fill=BLUE)

    return img.resize((size, size), Image.LANCZOS)

# 输出到仓库的 assets/ 目录(脚本位于 tools/,取其父目录),不写死绝对路径
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ASSETS = os.path.join(ROOT, "assets")
os.makedirs(ASSETS, exist_ok=True)

sizes = [16, 24, 32, 48, 64, 128, 256]
imgs = [draw_icon(s) for s in sizes]
ico_path = os.path.join(ASSETS, "app.ico")
imgs[-1].save(ico_path, sizes=[(s, s) for s in sizes])
# 另存 256 PNG 供各页面 favicon/预览
imgs[-1].save(os.path.join(ASSETS, "app.png"))

# 托盘专用 PNG:覆盖常见缩放档位(100%..400%),运行时按 SM_CXSMICON
# 选档替换托盘图标,避免 energye/systray 固定按 SM_CXICON 加载导致的
# 高 DPI 双重重采样发糊。每一档都用矢量管线独立绘制,不是互相缩放。
tray_dir = os.path.join(ASSETS, "tray")
os.makedirs(tray_dir, exist_ok=True)
for s in (16, 20, 24, 28, 32, 40, 48, 64, 80, 96, 128):
    draw_icon(s).save(os.path.join(tray_dir, "tray_%d.png" % s))
print("app.ico 已生成:", ico_path, sizes)
print("tray PNG 已生成:", tray_dir)
