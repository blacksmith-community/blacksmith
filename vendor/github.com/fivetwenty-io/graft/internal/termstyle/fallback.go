// Copyright © 2019 The Homeport Team
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.
//
// Copyright (c) 2016 Yasuhiro Matsumoto
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
//
// Ported from github.com/gonvenience/bunt v1.4.3 (render.go) and
// github.com/mattn/go-ciede2000 at commit 782e8c62fec3 (ciede2000.go), and
// modified for graft: the color mode is an explicit value, and the color
// distance works on RGB values.

package termstyle

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// render turns a styled string into text for the mode.
func (m Mode) render(s styledString) string {
	var (
		buffer  = &bytes.Buffer{}
		current = uint64(0)
	)

	for _, r := range s {
		if m.Color && current != r.settings {
			// In case text emphasis like bold, italic, or underline was set,
			// but is now turned off, a reset sequence is in order to ensure
			// that the text emphasis is removed.
			prepend := []uint8{}
			if isBitTurnedOff(current, r.settings, boldMask) ||
				isBitTurnedOff(current, r.settings, italicMask) ||
				isBitTurnedOff(current, r.settings, underlineMask) {
				prepend = append(prepend, 0)
			}

			_, _ = buffer.WriteString(m.renderSGR(r.settings, prepend...))
			current = r.settings
		}

		_, _ = buffer.WriteRune(r.symbol)
	}

	// Make sure to finish with a reset escape sequence.
	if current != 0 {
		_, _ = buffer.WriteString(m.renderSGR(0))
	}

	return buffer.String()
}

func isBitTurnedOff(from uint64, to uint64, mask uint64) bool {
	return (from&mask) != 0 && (to&mask) == 0
}

func (m Mode) renderSGR(setting uint64, prepend ...uint8) string {
	if setting == 0 {
		return renderEscapeSequence(0)
	}

	// Start with the additional parameters to be prepended.
	parameters := append([]uint8{}, prepend...)

	if (setting & boldMask) != 0 {
		parameters = append(parameters, 1)
	}

	if (setting & italicMask) != 0 {
		parameters = append(parameters, 3)
	}

	if (setting & underlineMask) != 0 {
		parameters = append(parameters, 4)
	}

	if (setting & fgMask) != 0 {
		c := RGB{uint8((setting >> 8) & 0xFF), uint8((setting >> 16) & 0xFF), uint8((setting >> 24) & 0xFF)}
		if m.TrueColor {
			parameters = append(parameters, 38, 2, c.R, c.G, c.B)
		} else {
			parameters = append(parameters, Closest16(c))
		}
	}

	if (setting & bgMask) != 0 {
		c := RGB{uint8((setting >> 32) & 0xFF), uint8((setting >> 40) & 0xFF), uint8((setting >> 48) & 0xFF)}
		if m.TrueColor {
			parameters = append(parameters, 48, 2, c.R, c.G, c.B)
		} else {
			parameters = append(parameters, 10+Closest16(c))
		}
	}

	return renderEscapeSequence(parameters...)
}

func renderEscapeSequence(a ...uint8) string {
	values := make([]string, len(a))
	for i := range a {
		values[i] = strconv.Itoa(int(a[i]))
	}

	return fmt.Sprintf("\x1b[%sm", strings.Join(values, ";"))
}

// candidate16 is one of the 16 basic terminal colors.
type candidate16 struct {
	code  uint8
	color RGB
}

// candidates16 lists the basic colors in ascending code order.
var candidates16 = []candidate16{
	{30, RGB{0x00, 0x00, 0x00}},
	{31, RGB{0xAA, 0x00, 0x00}},
	{32, RGB{0x00, 0xAA, 0x00}},
	{33, RGB{0xFF, 0xFF, 0x00}},
	{34, RGB{0x00, 0x00, 0xAA}},
	{35, RGB{0xAA, 0x00, 0xAA}},
	{36, RGB{0x00, 0xAA, 0xAA}},
	{37, RGB{0xAA, 0xAA, 0xAA}},
	{90, RGB{0x55, 0x55, 0x55}},
	{91, RGB{0xFF, 0x55, 0x55}},
	{92, RGB{0x55, 0xFF, 0x55}},
	{93, RGB{0xFF, 0xFF, 0x55}},
	{94, RGB{0x55, 0x55, 0xFF}},
	{95, RGB{0xFF, 0x55, 0xFF}},
	{96, RGB{0x55, 0xFF, 0xFF}},
	{97, RGB{0xFF, 0xFF, 0xFF}},
}

// Closest16 returns the foreground color code (30-37 or 90-97) of the basic
// terminal color that best matches c under the CIEDE2000 distance. A
// candidate wins only when its distance is strictly less than the best so
// far, so on an exact tie the lowest code wins, which agrees with bunt
// whenever its map iteration meets no exact tie.
func Closest16(c RGB) uint8 {
	var (
		result = uint8(0)
		best   = math.MaxFloat64
	)

	for _, candidate := range candidates16 {
		if distance := ciede2000(c, candidate.color); distance < best {
			best, result = distance, candidate.code
		}
	}

	return result
}

// lab is a color in the CIE L*a*b* space.
type lab struct {
	L float64
	A float64
	B float64
}

// toXYZ converts an RGB color to CIE XYZ. Each 8-bit channel first goes to
// the 16-bit scale that color.RGBA reports.
func toXYZ(c RGB) (float64, float64, float64) {
	r := float64(uint32(c.R)*0x101) / 65535.0
	g := float64(uint32(c.G)*0x101) / 65535.0
	b := float64(uint32(c.B)*0x101) / 65535.0

	if r > 0.04045 {
		r = math.Pow(((r + 0.055) / 1.055), 2.4)
	} else {
		r /= 12.92
	}

	if g > 0.04045 {
		g = math.Pow(((g + 0.055) / 1.055), 2.4)
	} else {
		g /= 12.92
	}

	if b > 0.04045 {
		b = math.Pow(((b + 0.055) / 1.055), 2.4)
	} else {
		b /= 12.92
	}

	r *= 100
	g *= 100
	b *= 100

	return r*0.4124 + g*0.3576 + b*0.1805, r*0.2126 + g*0.7152 + b*0.0722, r*0.0193 + g*0.1192 + b*0.9505
}

// labF is the nonlinear step of the XYZ to LAB conversion. Upstream writes
// the linear branch as (7.787 * x) + (16 / 116), where Go evaluates 16 / 116
// as integer division and gets 0, so the branch is 7.787 * x.
func labF(x float64) float64 {
	if x > 0.008856 {
		return math.Pow(x, (1.0 / 3.0))
	}

	return 7.787 * x
}

func toLAB(c RGB) *lab {
	x, y, z := toXYZ(c)
	x /= 95.047
	y /= 100.000
	z /= 108.883

	x, y, z = labF(x), labF(y), labF(z)

	l := (116 * y) - 16
	a := 500 * (x - y)
	b := 200 * (y - z)

	if l < 0.0 {
		l = 0.0
	}

	return &lab{l, a, b}
}

func deg2Rad(deg float64) float64 {
	return deg * (math.Pi / 180.0)
}

// hueAngle returns the hue angle of (a, b) in radians in [0, 2*pi), and 0
// when both are zero.
func hueAngle(a, b float64) float64 {
	if b == 0 && a == 0 {
		return 0.0
	}

	h := math.Atan2(b, a)
	// Convert to a hue angle between 0 and 360 degrees by adding 2*pi to
	// negative hue angles.
	if h < 0 {
		h += deg2Rad(360.0)
	}

	return h
}

// meanHue returns the mean hue angle of equation 14.
func meanHue(cPrime1, cPrime2, hPrime1, hPrime2 float64) float64 {
	deg360InRad := deg2Rad(360.0)
	deg180InRad := deg2Rad(180.0)
	hPrimeSum := hPrime1 + hPrime2

	switch {
	case cPrime1*cPrime2 == 0:
		return hPrimeSum
	case math.Abs(hPrime1-hPrime2) <= deg180InRad:
		return hPrimeSum / 2.0
	case hPrimeSum < deg360InRad:
		return (hPrimeSum + deg360InRad) / 2.0
	default:
		return (hPrimeSum - deg360InRad) / 2.0
	}
}

// ciede2000Lab returns the CIEDE2000 distance between two LAB colors.
func ciede2000Lab(lab1, lab2 *lab) float64 {
	// "For these and all other numerical/graphical delta E00 values reported
	// in this article, we set the parametric weighting factors to unity
	// (i.e., k_L = k_C = k_H = 1.0)." (Page 27).
	kL, kC, kH := 1.0, 1.0, 1.0
	deg360InRad := deg2Rad(360.0)
	deg180InRad := deg2Rad(180.0)
	pow25To7 := 6103515625.0 // math.Pow(25, 7)

	// Step 1, equations 2 to 7.
	c1 := math.Sqrt((lab1.A * lab1.A) + (lab1.B * lab1.B))
	c2 := math.Sqrt((lab2.A * lab2.A) + (lab2.B * lab2.B))
	barC := (c1 + c2) / 2.0
	g := 0.5 * (1 - math.Sqrt(math.Pow(barC, 7)/(math.Pow(barC, 7)+pow25To7)))
	a1Prime := (1.0 + g) * lab1.A
	a2Prime := (1.0 + g) * lab2.A
	cPrime1 := math.Sqrt((a1Prime * a1Prime) + (lab1.B * lab1.B))
	cPrime2 := math.Sqrt((a2Prime * a2Prime) + (lab2.B * lab2.B))
	hPrime1 := hueAngle(a1Prime, lab1.B)
	hPrime2 := hueAngle(a2Prime, lab2.B)

	// Step 2, equations 8 to 11.
	deltaLPrime := lab2.L - lab1.L
	deltaCPrime := cPrime2 - cPrime1

	var deltahPrime float64

	cPrimeProduct := cPrime1 * cPrime2
	if cPrimeProduct != 0 {
		deltahPrime = hPrime2 - hPrime1
		if deltahPrime < -deg180InRad {
			deltahPrime += deg360InRad
		} else if deltahPrime > deg180InRad {
			deltahPrime -= deg360InRad
		}
	}

	deltaHPrime := 2.0 * math.Sqrt(cPrimeProduct) * math.Sin(deltahPrime/2.0)

	// Step 3, equations 12 to 21.
	barLPrime := (lab1.L + lab2.L) / 2.0
	barCPrime := (cPrime1 + cPrime2) / 2.0
	barhPrime := meanHue(cPrime1, cPrime2, hPrime1, hPrime2)

	t := 1.0 - (0.17 * math.Cos(barhPrime-deg2Rad(30.0))) +
		(0.24 * math.Cos(2.0*barhPrime)) +
		(0.32 * math.Cos((3.0*barhPrime)+deg2Rad(6.0))) -
		(0.20 * math.Cos((4.0*barhPrime)-deg2Rad(63.0)))
	deltaTheta := deg2Rad(30.0) * math.Exp(-math.Pow((barhPrime-deg2Rad(275.0))/deg2Rad(25.0), 2.0))
	rC := 2.0 * math.Sqrt(math.Pow(barCPrime, 7.0)/(math.Pow(barCPrime, 7.0)+pow25To7))
	sL := 1 + ((0.015 * math.Pow(barLPrime-50.0, 2.0)) /
		math.Sqrt(20+math.Pow(barLPrime-50.0, 2.0)))
	sC := 1 + (0.045 * barCPrime)
	sH := 1 + (0.015 * barCPrime * t)
	rT := (-math.Sin(2.0 * deltaTheta)) * rC

	// Equation 22.
	return math.Sqrt(
		math.Pow(deltaLPrime/(kL*sL), 2.0) +
			math.Pow(deltaCPrime/(kC*sC), 2.0) +
			math.Pow(deltaHPrime/(kH*sH), 2.0) +
			(rT * (deltaCPrime / (kC * sC)) * (deltaHPrime / (kH * sH))))
}

// ciede2000 returns the CIEDE2000 distance between two RGB colors.
func ciede2000(a, b RGB) float64 {
	return ciede2000Lab(toLAB(a), toLAB(b))
}
