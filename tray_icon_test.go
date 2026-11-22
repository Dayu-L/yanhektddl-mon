//go:build windows

package main

// 真实校验 createTrayHICON 的产物:把生成的 HICON 画到 32bpp 内存位图上,
// 与源 RGBA 逐像素比对(全不透明区域应零失真,半透明边缘按 alpha 混合校验)。
// go test -run TestTrayHICONPixels -v

import (
	"testing"
	"unsafe"
)

var (
	procCreateCompatDC = gdi32.NewProc("CreateCompatibleDC")
	procSelectObject   = gdi32.NewProc("SelectObject")
	procDrawIconEx     = user32.NewProc("DrawIconEx")
	procDeleteDC       = gdi32.NewProc("DeleteDC")
	procDestroyIcon    = user32.NewProc("DestroyIcon")
	procGetDC          = user32.NewProc("GetDC")
	procReleaseDC      = user32.NewProc("ReleaseDC")
)

func TestTrayHICONPixels(t *testing.T) {
	const size = 40
	img := pickTrayIcon(size)
	if img == nil {
		t.Fatalf("缺少 %dpx 托盘图标档位", size)
	}
	if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
		t.Fatalf("档位尺寸异常:%dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}

	hIcon := createTrayHICON(img)
	if hIcon == 0 {
		t.Fatalf("createTrayHICON 失败")
	}
	defer procDestroyIcon.Call(hIcon)

	// 目标位图:品红底(便于校验 alpha)
	bi := bitmapInfo{Header: bitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:    size,
		Height:   -size,
		Planes:   1,
		BitCount: 32,
	}}
	var bits unsafe.Pointer
	hBmp, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)),
		0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hBmp == 0 {
		t.Fatalf("CreateDIBSection 失败")
	}
	defer procDeleteObject.Call(hBmp)
	raw := unsafe.Slice((*byte)(bits), size*size*4)
	for i := range raw {
		raw[i] = 0
	}
	// BGRA 品红
	for i := 0; i < size*size; i++ {
		o := i * 4
		raw[o], raw[o+1], raw[o+2], raw[o+3] = 255, 0, 255, 255
	}

	hdc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, hdc)
	memDC, _, _ := procCreateCompatDC.Call(hdc)
	if memDC == 0 {
		t.Fatalf("CreateCompatibleDC 失败")
	}
	defer procDeleteDC.Call(memDC)
	old, _, _ := procSelectObject.Call(memDC, hBmp)
	defer procSelectObject.Call(memDC, old)

	if r, _, _ := procDrawIconEx.Call(memDC, 0, 0, hIcon, size, size, 0, 0, 3 /*DI_NORMAL*/); r == 0 {
		t.Fatalf("DrawIconEx 失败")
	}

	// 比对:不透明像素应等于源色;半透明按 alpha 与品红混合;全透明保持品红
	const magentaR, magentaG, magentaB = 255, 0, 255
	badOpaque, badBlend, badAlpha := 0, 0, 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			o := (y*size + x) * 4
			// DIB 顶朝下,源图也是顶朝下,行序一致;源图通道序是 R,G,B,A
			sr, sg, sb, sa := int(img.Pix[o]), int(img.Pix[o+1]), int(img.Pix[o+2]), int(img.Pix[o+3])
			dr, dg, db := int(raw[o+2]), int(raw[o+1]), int(raw[o])
			switch {
			case sa == 255:
				if abs(dr-sr) > 2 || abs(dg-sg) > 2 || abs(db-sb) > 2 {
					badOpaque++
				}
			case sa == 0:
				if dr != magentaR || dg != magentaG || db != magentaB {
					badAlpha++
				}
			default:
				er := (sr*sa + magentaR*(255-sa) + 127) / 255
				eg := (sg*sa + magentaG*(255-sa) + 127) / 255
				eb := (sb*sa + magentaB*(255-sa) + 127) / 255
				if abs(dr-er) > 3 || abs(dg-eg) > 3 || abs(db-eb) > 3 {
					badBlend++
				}
			}
		}
	}
	t.Logf("不透明失真=%d 混合失真=%d 透明失真=%d / %d 像素",
		badOpaque, badBlend, badAlpha, size*size)
	if badOpaque > 0 || badAlpha > 0 || badBlend > size*size/1000 {
		t.Fatalf("HICON 与源图不符:不透明=%d 混合=%d 透明=%d",
			badOpaque, badBlend, badAlpha)
	}
}
