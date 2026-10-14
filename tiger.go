package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math"
)

// 老虎头是自己画的，不搬任何系统表情的图：几何形状都在 32×32 的格子里，
// 菜单栏的单色模板图和面板里的彩色头像共用这一份。

type tigerPart int

const (
	tigerNone   tigerPart = iota
	tigerFur              // 橙色的头和耳朵
	tigerCream            // 内耳和嘴边的白毛
	tigerStripe           // 黑色：花纹、眼睛、鼻子
)

// 头像在格子里上下占的范围，菜单栏按这段高度灌水位。
const tigerTop, tigerBottom = 2.8, 30.0

type pt struct{ x, y float64 }

func inEllipse(x, y, cx, cy, rx, ry float64) bool {
	dx, dy := (x-cx)/rx, (y-cy)/ry
	return dx*dx+dy*dy <= 1
}

func inTriangle(x, y float64, a, b, c pt) bool {
	side := func(p, q pt) float64 { return (q.x-p.x)*(y-p.y) - (q.y-p.y)*(x-p.x) }
	d1, d2, d3 := side(a, b), side(b, c), side(c, a)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

// 花纹左右对称，只写左半边，右边照镜子。
var tigerStripes = [][3]pt{
	{{14.3, 5.4}, {17.7, 5.4}, {16, 11}},    // 额头正中
	{{9.6, 7.6}, {12.6, 6.6}, {13, 11.4}},   // 额头左
	{{2.4, 14.8}, {2.6, 18}, {8.4, 16.9}},   // 脸颊上
	{{3.4, 21.2}, {4.6, 23.8}, {8.8, 21.4}}, // 脸颊下
	{{13.7, 20}, {18.3, 20}, {16, 22.6}},    // 鼻子
}

// tigerAt 说 32×32 格子里 (x, y) 这一点落在头像的哪一块。
func tigerAt(x, y float64) tigerPart {
	mx := x
	if mx > 16 {
		mx = 32 - mx
	}
	head := inEllipse(x, y, 16, 17.5, 13.5, 12.5)
	ear := inEllipse(mx, y, 8.4, 7.8, 5.2, 5.2)
	if !head && !ear {
		return tigerNone
	}
	if inEllipse(mx, y, 11.3, 16.2, 1.7, 2.2) {
		return tigerStripe // 眼睛
	}
	for _, s := range tigerStripes {
		if inTriangle(mx, y, s[0], s[1], s[2]) {
			return tigerStripe
		}
	}
	if !head && inEllipse(mx, y, 8.6, 8.4, 2.5, 2.5) {
		return tigerCream // 内耳
	}
	if inEllipse(mx, y, 13, 23.2, 4.2, 3.4) {
		return tigerCream // 嘴边
	}
	return tigerFur
}

// rasterTiger 把头像画成 size×size，每个像素 4×4 采样抗锯齿；
// paint 决定每一块用什么颜色，第三个参数是采样点的纵坐标（格子单位）。
func rasterTiger(size int, paint func(part tigerPart, y float64) color.NRGBA) *image.NRGBA {
	const n = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	unit := 32 / float64(size)
	for py := range size {
		for px := range size {
			var r, g, b, a float64
			for sy := range n {
				for sx := range n {
					x := (float64(px) + (float64(sx)+0.5)/n) * unit
					y := (float64(py) + (float64(sy)+0.5)/n) * unit
					part := tigerAt(x, y)
					if part == tigerNone {
						continue
					}
					c := paint(part, y)
					ca := float64(c.A)
					r += float64(c.R) * ca
					g += float64(c.G) * ca
					b += float64(c.B) * ca
					a += ca
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(r / a), G: uint8(g / a), B: uint8(b / a),
				A: uint8(math.Round(a / (n * n))),
			})
		}
	}
	return img
}

// tigerIcon 是菜单栏的单色模板图：老虎头当量杯，用掉多少就从下往上灌多满，
// 没灌到的部分淡淡地留着轮廓；花纹、眼睛、鼻子镂空，深浅色菜单栏都认得出。
func tigerIcon(fraction float64) []byte {
	level := tigerBottom - (tigerBottom-tigerTop)*math.Min(1, math.Max(0, fraction))
	img := rasterTiger(32, func(part tigerPart, y float64) color.NRGBA {
		if part == tigerStripe {
			return color.NRGBA{}
		}
		alpha := uint8(72)
		if y >= level {
			alpha = 255
		}
		return color.NRGBA{R: 255, G: 255, B: 255, A: alpha}
	})
	return encodePNG(img)
}

var (
	tigerOrange = color.NRGBA{R: 0xF5, G: 0x9A, B: 0x2E, A: 0xFF}
	tigerCreamC = color.NRGBA{R: 0xFF, G: 0xF4, B: 0xE4, A: 0xFF}
	tigerInk    = color.NRGBA{R: 0x2B, G: 0x1A, B: 0x10, A: 0xFF}
)

// tigerColor 是彩色头像里每一块的颜色。
func tigerColor(part tigerPart) color.NRGBA {
	switch part {
	case tigerCream:
		return tigerCreamC
	case tigerStripe:
		return tigerInk
	}
	return tigerOrange
}

// tigerFace 是彩色头像，给面板标题旁边用。
func tigerFace(size int) *image.NRGBA {
	return rasterTiger(size, func(part tigerPart, _ float64) color.NRGBA {
		return tigerColor(part)
	})
}

// dockIconSize 是 Dock 图标的边长。Dock 放大到最大也就 256pt，
// Retina 下 512 像素够用，再大只是白白多算。
const dockIconSize = 512

// tigerDockIcon 是 Dock 里的彩色老虎头，和菜单栏一样当量杯用：
// 用掉的部分从下往上是原色，没用到的部分褪成淡灰，一眼看得出还剩多少。
// 四周留出约一成的边，和别的 Dock 图标一般大。
func tigerDockIcon(fraction float64) []byte {
	return encodePNG(colorTiger(dockIconSize, fraction))
}

// tigerAppIcon 是打包进 .app 的静态图标，Finder 和启动台里看到的就是它：
// 没有用量可言，就是满色的老虎头。仓库里的 icon.png 由它生成，
// 见 TestAppIconUpToDate。
func tigerAppIcon() []byte {
	return encodePNG(colorTiger(1024, 1))
}

// colorTiger 把彩色头像画在 size 见方、四周留边的透明画布中间，
// fraction 以上的部分褪色。
func colorTiger(size int, fraction float64) *image.NRGBA {
	level := tigerBottom - (tigerBottom-tigerTop)*math.Min(1, math.Max(0, fraction))
	inner := size * 13 / 16
	face := rasterTiger(inner, func(part tigerPart, y float64) color.NRGBA {
		c := tigerColor(part)
		if y < level {
			c = faded(c)
		}
		return c
	})
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	off := (size - inner) / 2
	draw.Draw(img, face.Bounds().Add(image.Pt(off, off)), face, image.Point{}, draw.Src)
	return img
}

// faded 把颜色褪成同样明暗的灰，再调淡一些：深浅色 Dock 上都还看得见轮廓。
func faded(c color.NRGBA) color.NRGBA {
	g := uint8((299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000)
	return color.NRGBA{R: g, G: g, B: g, A: c.A / 2}
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Println("生成老虎图标：", err)
		return nil
	}
	return buf.Bytes()
}
