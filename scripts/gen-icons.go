// +build ignore

package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func main() {
	dir := filepath.Join("internal", "menubar", "icons")
	os.MkdirAll(dir, 0755)

	colors := map[string]color.RGBA{
		"green":  {R: 0x34, G: 0xD3, B: 0x99, A: 0xFF}, // emerald
		"yellow": {R: 0xFB, G: 0xBF, B: 0x24, A: 0xFF}, // amber
		"red":    {R: 0xEF, G: 0x44, B: 0x44, A: 0xFF}, // red
		"gray":   {R: 0x9C, G: 0xA3, B: 0xAF, A: 0xFF}, // gray-400
	}

	for name, c := range colors {
		img := image.NewRGBA(image.Rect(0, 0, 22, 22))
		cx, cy, r := 11.0, 11.0, 9.5

		// Draw anti-aliased colored circle
		for y := 0; y < 22; y++ {
			for x := 0; x < 22; x++ {
				dx := float64(x) + 0.5 - cx
				dy := float64(y) + 0.5 - cy
				dist := math.Sqrt(dx*dx + dy*dy)
				if dist <= r-0.5 {
					img.Set(x, y, c)
				} else if dist <= r+0.5 {
					alpha := uint8(255 * (r + 0.5 - dist))
					img.Set(x, y, color.RGBA{c.R, c.G, c.B, alpha})
				}
			}
		}

		// Draw white "A" glyph centered in the circle
		// Designed as a bold A for 22x22 at ~12px height
		// The A is defined as filled polygon regions
		drawA(img, cx, cy)

		f, _ := os.Create(filepath.Join(dir, name+".png"))
		png.Encode(f, img)
		f.Close()
	}
}

// drawA draws a bold white "A" centered in the icon.
// Uses sub-pixel rendering for crisp anti-aliased edges.
func drawA(img *image.RGBA, cx, cy float64) {
	white := color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}

	// A as a polygon: two legs meeting at top, with a crossbar
	// Centered at (cx, cy), spanning roughly 10px wide, 12px tall
	topY := cy - 5.5  // top of A
	botY := cy + 5.5  // bottom of A
	barY1 := cy + 0.5 // crossbar top
	barY2 := cy + 2.0 // crossbar bottom

	// Stroke width of the A legs
	sw := 2.2

	// The A shape: apex at top center, legs spread to bottom
	// Left leg outer/inner edges, right leg outer/inner edges
	halfTopW := 0.3  // half-width at apex
	halfBotW := 5.0  // half-width at base

	for y := 0; y < 22; y++ {
		fy := float64(y) + 0.5
		if fy < topY-0.5 || fy > botY+0.5 {
			continue
		}

		for x := 0; x < 22; x++ {
			fx := float64(x) + 0.5

			coverage := glyphCoverage(fx, fy, cx, topY, botY, barY1, barY2, halfTopW, halfBotW, sw)
			if coverage <= 0 {
				continue
			}

			// Blend white over existing pixel
			existing := img.RGBAAt(x, y)
			if existing.A == 0 {
				continue // Outside the circle
			}

			alpha := uint8(math.Min(255, coverage*255))
			blended := alphaBlend(existing, white, alpha)
			img.Set(x, y, blended)
		}
	}
}

// glyphCoverage returns 0..1 coverage for the "A" glyph at point (fx, fy).
// Uses 4x4 super-sampling for anti-aliasing.
func glyphCoverage(fx, fy, cx, topY, botY, barY1, barY2, halfTopW, halfBotW, sw float64) float64 {
	// Quick bounding box check
	if fx < cx-halfBotW-1 || fx > cx+halfBotW+1 {
		return 0
	}

	const samples = 4
	hit := 0
	for sy := 0; sy < samples; sy++ {
		for sx := 0; sx < samples; sx++ {
			px := fx - 0.5 + (float64(sx)+0.5)/float64(samples)
			py := fy - 0.5 + (float64(sy)+0.5)/float64(samples)

			if isInsideA(px, py, cx, topY, botY, barY1, barY2, halfTopW, halfBotW, sw) {
				hit++
			}
		}
	}
	return float64(hit) / float64(samples*samples)
}

// isInsideA checks if a point is inside the "A" shape.
func isInsideA(px, py, cx, topY, botY, barY1, barY2, halfTopW, halfBotW, sw float64) bool {
	if py < topY || py > botY {
		return false
	}

	// Interpolation factor: 0 at top, 1 at bottom
	t := (py - topY) / (botY - topY)

	// Outer edges of the A at this y
	outerHalf := halfTopW + t*(halfBotW-halfTopW)
	// Inner edges (the hollow triangle inside the A)
	innerHalf := outerHalf - sw

	dx := px - cx

	// Left leg: between left outer and left inner edge
	// Right leg: between right inner and right outer edge
	inLeftLeg := dx >= -outerHalf && dx <= -innerHalf
	inRightLeg := dx >= innerHalf && dx <= outerHalf

	// Top cap: when inner edges haven't separated yet, fill solid
	inTopCap := innerHalf <= 0 && dx >= -outerHalf && dx <= outerHalf

	// Crossbar
	inCrossbar := py >= barY1 && py <= barY2 && dx >= -outerHalf+0.3 && dx <= outerHalf-0.3

	return inLeftLeg || inRightLeg || inTopCap || inCrossbar
}

func alphaBlend(bg color.RGBA, fg color.RGBA, alpha uint8) color.RGBA {
	a := float64(alpha) / 255.0
	return color.RGBA{
		R: uint8(float64(bg.R)*(1-a) + float64(fg.R)*a),
		G: uint8(float64(bg.G)*(1-a) + float64(fg.G)*a),
		B: uint8(float64(bg.B)*(1-a) + float64(fg.B)*a),
		A: bg.A,
	}
}
