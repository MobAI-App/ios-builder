package otainstall

import (
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

const quietZone = 2

// QR renders text as a QR code in Unicode half blocks, two module rows per
// line. Light modules print as full blocks and dark ones as spaces, which
// scans on the usual light-on-dark terminal; invert flips that for a dark-on-
// light one.
func QR(text string, invert bool) (string, error) {
	modules, err := qrModules(text)
	if err != nil {
		return "", err
	}
	size := len(modules) + 2*quietZone
	dark := func(x, y int) bool {
		x, y = x-quietZone, y-quietZone
		if x < 0 || y < 0 || x >= len(modules) || y >= len(modules) {
			return invert
		}
		return modules[y][x] != invert
	}
	var b strings.Builder
	for y := 0; y < size; y += 2 {
		for x := 0; x < size; x++ {
			top := dark(x, y)
			bottom := y+1 >= size || dark(x, y+1)
			switch {
			case !top && !bottom:
				b.WriteString("█")
			case !top && bottom:
				b.WriteString("▀")
			case top && !bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// qrModules is the code's module grid (true = dark) without a border; the
// lowest error-correction level keeps the code small enough for a terminal.
func qrModules(text string) ([][]bool, error) {
	q, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return nil, err
	}
	q.DisableBorder = true
	return q.Bitmap(), nil
}
