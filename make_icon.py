# -*- coding: utf-8 -*-
"""生成 app.ico:白底圆角方块 + 品牌蓝时钟(与应用内标记一致),多尺寸"""
from PIL import Image, ImageDraw

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

sizes = [16, 24, 32, 48, 64, 128, 256]
imgs = [draw_icon(s) for s in sizes]
imgs[-1].save(r"C:\Users\llyyt\Desktop\项目文件\yanhektddl-mon\app.ico",
              sizes=[(s, s) for s in sizes])
# 另存 256 PNG 供各页面 favicon/预览
imgs[-1].save(r"C:\Users\llyyt\Desktop\项目文件\yanhektddl-mon\app.png")
print("app.ico 已生成:", sizes)
