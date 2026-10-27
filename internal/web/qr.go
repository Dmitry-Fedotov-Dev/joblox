package web

import (
	"fmt"
	"html/template"
	"strings"

	"rsc.io/qr"
)

// qrSVG draws text as a QR code in inline SVG: the owner shows it to a customer, the phone camera opens the
// site. Inline SVG needs no image route and passes the page's CSP. Empty on failure.
func qrSVG(text string) template.HTML {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	const quiet = 4 // the white border scanners need
	n := c.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="qr" viewBox="0 0 %d %d" role="img" aria-label="QR-код ссылки на сайт" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String()) // #nosec G203 -- built here from numbers only, the text is not in the markup
}
