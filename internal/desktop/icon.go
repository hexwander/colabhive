package desktop

import (
	"bytes"
	"image"
	"image/png"
	"math"
)

// vec2 represents a 2D point in normalized `[-1, 1]` icon coordinate space.
type vec2 struct {
	x, y float64
}

type rgbColor struct {
	r, g, b float64
}

type hexCellSpec struct {
	center     vec2
	innerVerts [6]vec2
	cornerR    float64
	invRadius  float64
	topCol     rgbColor
	botCol     rgbColor
	hiCol      rgbColor
	deepCol    rgbColor
}

const (
	sqrt3     = 1.7320508075688772
	halfSqrt3 = 0.8660254037844386
	invSqrt3  = 0.5773502691896257
)

func buildHoneycombTriad(size int) (squircleA float64, cells [3]hexCellSpec) {
	squircleA = 0.895
	cellR := 0.322
	gap := 0.064
	cornerR := 0.038

	if size <= 20 {
		squircleA = 0.915
		cellR = 0.330
		gap = 0.080
		cornerR = 0.024
	} else if size <= 32 {
		squircleA = 0.905
		cellR = 0.325
		gap = 0.068
		cornerR = 0.032
	}

	rIn := halfSqrt3 * cellR
	distCenters := 2.0*rIn + gap

	// Center the 3-cell triad's vertical bounding box optically at y = 0:
	// topCenterY = -(sqrt(3)/4)*D, bottomCenterY = +(sqrt(3)/4)*D
	topY := -(sqrt3 * 0.25) * distCenters
	botY := +(sqrt3 * 0.25) * distCenters
	halfD := 0.5 * distCenters

	// Shrink polygon circumradius by cornerR / cos(30°) so Minkowski rounding restores exact apothem rIn.
	innerR := cellR - cornerR*(2.0/sqrt3)
	if innerR < 0.05 {
		innerR = 0.05
	}

	makePointyHex := func(cx, cy float64, topC, botC, hiC, deepC rgbColor) hexCellSpec {
		dx := halfSqrt3 * innerR
		dy := 0.5 * innerR
		return hexCellSpec{
			center: vec2{cx, cy},
			// Clockwise winding in top-down screen coordinates (`+Y` downward):
			innerVerts: [6]vec2{
				{cx, cy - innerR},      // Top vertex
				{cx + dx, cy - dy},     // Top-right vertex
				{cx + dx, cy + dy},     // Bottom-right vertex
				{cx, cy + innerR},      // Bottom vertex
				{cx - dx, cy + dy},     // Bottom-left vertex
				{cx - dx, cy - dy},     // Top-left vertex
			},
			cornerR:   cornerR,
			invRadius: 1.0 / cellR,
			topCol:    topC,
			botCol:    botC,
			hiCol:     hiC,
			deepCol:   deepC,
		}
	}

	// 0: Top Crown Cell — Warm Colab Amber-Gold (#FBBF24 -> #F59E0B)
	cells[0] = makePointyHex(
		0.0, topY,
		rgbColor{251, 191, 36},  // #FBBF24
		rgbColor{245, 158, 11},  // #F59E0B
		rgbColor{253, 230, 138}, // #FDE68A
		rgbColor{217, 119, 6},   // #D97706
	)

	// 1: Bottom-Left Cell — Crisp Warm Alabaster (#FAF9F5 -> #E5E3DC)
	cells[1] = makePointyHex(
		-halfD, botY,
		rgbColor{250, 249, 245}, // #FAF9F5
		rgbColor{229, 227, 220}, // #E5E3DC
		rgbColor{255, 255, 255}, // #FFFFFF
		rgbColor{212, 209, 199}, // #D4D1C7
	)

	// 2: Bottom-Right Cell — Live Compute Emerald (#34D399 -> #10B981)
	cells[2] = makePointyHex(
		+halfD, botY,
		rgbColor{52, 211, 153},  // #34D399
		rgbColor{16, 185, 129},  // #10B981
		rgbColor{110, 231, 183}, // #6EE7B7
		rgbColor{5, 150, 105},   // #059669
	)

	return squircleA, cells
}

// RenderColabHiveIconBGRA rasterizes the flagship ColabHive app icon:
//   - Continuous Apple superellipse (`(|nx|^4.5 + |ny|^4.5) <= 1.0`) in rich obsidian-titanium
//     vertical gradient (`#232220` -> `#111110`) with a 1.2px warm specular inner border and ambient glow
//   - 3-Cell Isometric Honeycomb Tensor Cluster (Top Colab Amber `#FBBF24 -> #F59E0B`,
//     Bottom-Left Crisp Alabaster `#FAF9F5 -> #E5E3DC`, Bottom-Right Live Compute Emerald `#34D399 -> #10B981`)
//
// Returns a top-down 32-bit premultiplied BGRA pixel buffer of length `size * size * 4` using 4x4 supersampling.
func RenderColabHiveIconBGRA(size int) []byte {
	if size <= 0 {
		size = 32
	}
	buf := make([]byte, size*size*4)

	const grid = 4
	const invGrid = 1.0 / float64(grid)
	const sampleWeight = 1.0 / float64(grid*grid)

	fSize := float64(size)
	invHalfSize := 2.0 / fSize
	halfSizePx := 0.5 * fSize

	squircleA, cells := buildHoneycombTriad(size)
	invSquircleA := 1.0 / squircleA

	showFineDetail := size > 20
	showRidgeCatchlight := size >= 48
	rimWidthPx := math.Max(1.2, fSize*0.0038)
	cellRimNorm := math.Max(1.1, fSize*0.0032) / halfSizePx

	// Isometric prism rays from cell center to Top-Right (-30°), Bottom (+90°), Top-Left (-150°):
	rayTR := vec2{+halfSqrt3, -0.5}
	rayBL := vec2{-halfSqrt3, -0.5}
	rayBot := vec2{0.0, +1.0}

	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var accR, accG, accB, accA float64

			for sy := 0; sy < grid; sy++ {
				uy := (float64(py)+(float64(sy)+0.5)*invGrid)*invHalfSize - 1.0
				ay := math.Abs(uy) * invSquircleA
				ay2 := ay * ay
				ay4 := ay2 * ay2
				sqrtAy := math.Sqrt(ay)
				ay45 := ay4 * sqrtAy
				if ay45 > 1.0 {
					continue
				}
				ay35 := ay2 * ay * sqrtAy

				for sx := 0; sx < grid; sx++ {
					ux := (float64(px)+(float64(sx)+0.5)*invGrid)*invHalfSize - 1.0
					ax := math.Abs(ux) * invSquircleA
					ax2 := ax * ax
					ax4 := ax2 * ax2
					sqrtAx := math.Sqrt(ax)
					ax45 := ax4 * sqrtAx

					sumP := ax45 + ay45
					if sumP > 1.0 {
						continue
					}

					// 1. Base Obsidian-Titanium vertical gradient (#232220 top -> #111110 bottom)
					tBg := clamp01((uy*invSquircleA + 1.0) * 0.5)
					r := lerp(35.0, 17.0, tBg)
					g := lerp(34.0, 17.0, tBg)
					b := lerp(32.0, 16.0, tBg)

					// Subtle warm studio radial ambient glow behind the tensor cluster
					glowDist := math.Hypot(ux, uy+0.03)
					if glowDist < 0.78 {
						gf := 1.0 - glowDist/0.78
						gf *= gf
						r += 11.0 * gf
						g += 8.0 * gf
						b += 4.5 * gf
					}

					// Subtle 1.2px warm specular inner border (`rgba(255, 248, 235, 0.16)` top -> `0.05` bottom)
					if showFineDetail && sumP > 0.70 {
						ax35 := ax2 * ax * sqrtAx
						gradMag := (4.5 * invSquircleA) * math.Hypot(ax35, ay35)
						if gradMag > 1e-9 {
							// Negative signed Euclidean distance in pixels inside squircle boundary
							distPx := ((sumP - 1.0) / gradMag) * halfSizePx
							if distPx >= -rimWidthPx {
								// Crisp inner bevel falloff
								edgeT := 1.0 - (-distPx / rimWidthPx)
								rimAlpha := lerp(0.16, 0.05, tBg) * (0.45 + 0.55*edgeT)
								r = lerp(r, 255.0, rimAlpha)
								g = lerp(g, 248.0, rimAlpha)
								b = lerp(b, 235.0, rimAlpha)
							}
						}
					}

					// 2. Soft physical drop shadow beneath the 3-cell honeycomb cluster
					if showFineDetail {
						shY := uy - 0.026
						if shY > -0.72 && shY < 0.72 && ux > -0.72 && ux < 0.72 {
							minSh := sdRoundedHex(ux, shY, &cells[0])
							if d1 := sdRoundedHex(ux, shY, &cells[1]); d1 < minSh {
								minSh = d1
							}
							if d2 := sdRoundedHex(ux, shY, &cells[2]); d2 < minSh {
								minSh = d2
							}
							if minSh < 0.045 {
								shAlpha := 0.38 * smoothstep(0.045, -0.012, minSh)
								r *= 1.0 - shAlpha
								g *= 1.0 - shAlpha
								b *= 1.0 - shAlpha
							}
						}
					}

					// 3. Iconic 3-Cell Isometric Honeycomb Tensor Cluster
					if ux > -0.72 && ux < 0.72 && uy > -0.72 && uy < 0.72 {
						hitIdx := -1
						var cellDist float64
						for i := 0; i < 3; i++ {
							d := sdRoundedHex(ux, uy, &cells[i])
							if d <= 0 {
								hitIdx = i
								cellDist = d
								break
							}
						}

						if hitIdx >= 0 {
							c := &cells[hitIdx]
							lx := (ux - c.center.x) * c.invRadius
							ly := (uy - c.center.y) * c.invRadius

							gt := clamp01((ly + 0.30*lx + 1.0) * 0.5)
							cr := lerp(c.topCol.r, c.botCol.r, gt)
							cg := lerp(c.topCol.g, c.botCol.g, gt)
							cb := lerp(c.topCol.b, c.botCol.b, gt)

							if showFineDetail {
								// 3D Isometric Cube Facets (Top diamond, Bottom-Left face, Bottom-Right face)
								topBoundaryY := -math.Abs(lx) * invSqrt3
								if ly < topBoundaryY {
									// Sunlit top diamond facet
									cr = lerp(cr, c.hiCol.r, 0.24)
									cg = lerp(cg, c.hiCol.g, 0.24)
									cb = lerp(cb, c.hiCol.b, 0.24)
								} else if lx >= 0 {
									// Sculpted right prism shadow facet
									cr = lerp(cr, c.deepCol.r, 0.22)
									cg = lerp(cg, c.deepCol.g, 0.22)
									cb = lerp(cb, c.deepCol.b, 0.22)
								} else {
									// Crisp warm left prism facet
									cr = lerp(cr, c.hiCol.r, 0.06)
									cg = lerp(cg, c.hiCol.g, 0.06)
									cb = lerp(cb, c.hiCol.b, 0.06)
								}

								// Subtle isometric Y-ridge specular catchlight meeting at cell center
								if showRidgeCatchlight {
									pRel := vec2{lx, ly}
									dRidge := distToRaySegment(pRel, rayTR)
									if d2 := distToRaySegment(pRel, rayBL); d2 < dRidge {
										dRidge = d2
									}
									if d3 := distToRaySegment(pRel, rayBot); d3 < dRidge {
										dRidge = d3
									}
									if dRidge < 0.045 {
										ridgeT := (1.0 - dRidge/0.045) * 0.16
										cr = lerp(cr, c.hiCol.r, ridgeT)
										cg = lerp(cg, c.hiCol.g, ridgeT)
										cb = lerp(cb, c.hiCol.b, ridgeT)
									}
								}

								// Crisp top-left bevel catchlight on each hex cell rim
								if cellDist >= -cellRimNorm && ly < 0.25 {
									bevelT := (1.0 - (-cellDist / cellRimNorm)) * clamp01(0.25-ly) * 0.45
									cr = lerp(cr, 255.0, bevelT)
									cg = lerp(cg, 255.0, bevelT)
									cb = lerp(cb, 252.0, bevelT)
								}
							}

							r, g, b = cr, cg, cb
						}
					}

					accR += r * sampleWeight
					accG += g * sampleWeight
					accB += b * sampleWeight
					accA += 255.0 * sampleWeight
				}
			}

			idx := (py*size + px) * 4
			alpha := clampByte(accA)
			if alpha == 0 {
				continue
			}
			// Premultiplied BGRA invariant: R, G, B never exceed A.
			bByte := minByte(clampByte(accB), alpha)
			gByte := minByte(clampByte(accG), alpha)
			rByte := minByte(clampByte(accR), alpha)

			buf[idx+0] = bByte
			buf[idx+1] = gByte
			buf[idx+2] = rByte
			buf[idx+3] = alpha
		}
	}
	return buf
}

// RenderBrandIconBGRA is preserved as an alias to RenderColabHiveIconBGRA for Win32 callers.
func RenderBrandIconBGRA(size int) []byte {
	return RenderColabHiveIconBGRA(size)
}

// RenderColabHiveIconRGBA rasterizes the ColabHive flagship app icon into a standard premultiplied `*image.RGBA`.
func RenderColabHiveIconRGBA(size int) *image.RGBA {
	if size <= 0 {
		size = 32
	}
	bgra := RenderColabHiveIconBGRA(size)
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	pix := img.Pix
	for i := 0; i+3 < len(bgra); i += 4 {
		pix[i+0] = bgra[i+2] // R
		pix[i+1] = bgra[i+1] // G
		pix[i+2] = bgra[i+0] // B
		pix[i+3] = bgra[i+3] // A
	}
	return img
}

// EncodeColabHivePNG renders and encodes the ColabHive flagship app icon as a lossless RGBA PNG at `size x size`.
func EncodeColabHivePNG(size int) ([]byte, error) {
	img := RenderColabHiveIconRGBA(size)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// BuildIconAndMaskBits returns both the top-down 32-bit BGRA color buffer and the
// 16-bit/32-bit WORD-aligned 1bpp monochrome AND mask required by Win32 `CreateIconIndirect`.
func BuildIconAndMaskBits(size int) (bgra []byte, mask []byte) {
	if size <= 0 {
		size = 32
	}
	bgra = RenderColabHiveIconBGRA(size)

	// Win32 CreateBitmap scanlines are 16-bit WORD aligned; aligning to 4 bytes (32 bits) satisfies both WORD and DWORD alignment.
	strideBytes := ((size + 31) / 32) * 4
	mask = make([]byte, strideBytes*size)
	for y := 0; y < size; y++ {
		rowOff := y * strideBytes
		for x := 0; x < size; x++ {
			alpha := bgra[(y*size+x)*4+3]
			if alpha == 0 {
				// Bit = 1 in AND mask indicates transparent background pixel on legacy GDI paths.
				mask[rowOff+(x>>3)] |= 1 << uint(7-(x&7))
			}
		}
	}
	return bgra, mask
}

func sdRoundedHex(px, py float64, cell *hexCellSpec) float64 {
	edgeDist := distToHexOutline(px, py, cell.innerVerts)
	if pointInConvexHex(px, py, cell.innerVerts) {
		return -edgeDist - cell.cornerR
	}
	return edgeDist - cell.cornerR
}

func distToRaySegment(p vec2, dir vec2) float64 {
	// dir is unit-length ray from (0,0) to hex vertex (length 0.92 so ridge stops cleanly inside rim)
	const maxLen = 0.90
	t := p.x*dir.x + p.y*dir.y
	if t < 0 {
		t = 0
	} else if t > maxLen {
		t = maxLen
	}
	return math.Hypot(p.x-t*dir.x, p.y-t*dir.y)
}

func distToSegment(px, py float64, a, b vec2) float64 {
	vx := b.x - a.x
	vy := b.y - a.y
	wx := px - a.x
	wy := py - a.y
	lenSq := vx*vx + vy*vy
	t := 0.0
	if lenSq > 1e-9 {
		t = (wx*vx + wy*vy) / lenSq
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
	}
	dx := px - (a.x + t*vx)
	dy := py - (a.y + t*vy)
	return math.Hypot(dx, dy)
}

func distToHexOutline(px, py float64, verts [6]vec2) float64 {
	best := math.MaxFloat64
	for i := 0; i < 6; i++ {
		d := distToSegment(px, py, verts[i], verts[(i+1)%6])
		if d < best {
			best = d
		}
	}
	return best
}

func pointInConvexHex(px, py float64, verts [6]vec2) bool {
	// Clockwise winding in top-down screen coordinates: cross product >= 0 for all edges.
	for i := 0; i < 6; i++ {
		a := verts[i]
		b := verts[(i+1)%6]
		cross := (b.x-a.x)*(py-a.y) - (b.y-a.y)*(px-a.x)
		if cross < 0 {
			return false
		}
	}
	return true
}

func lerp(a, b, t float64) float64 {
	return a + (b-a)*t
}

func clamp01(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	return v
}

func smoothstep(edge0, edge1, x float64) float64 {
	if edge0 == edge1 {
		if x >= edge1 {
			return 1
		}
		return 0
	}
	t := clamp01((x - edge0) / (edge1 - edge0))
	return t * t * (3.0 - 2.0*t)
}

func clampByte(v float64) byte {
	n := int(math.Round(v))
	if n <= 0 {
		return 0
	}
	if n >= 255 {
		return 255
	}
	return byte(n)
}

func minByte(a, b byte) byte {
	if a < b {
		return a
	}
	return b
}
