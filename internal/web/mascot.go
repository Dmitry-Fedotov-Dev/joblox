package web

import (
	"fmt"
	"html/template"
	"strings"
)

// mascot is the picture of a product without photos: a line-drawn kitten in one of several poses — asleep on
// a cushion, peeking from below at the visitor, sitting at the side and watching the page, playing with a
// ball of yarn. The product id picks the pose and the paper tone, so a shop of such cards still looks varied
// and calm. Plain inline SVG (no files to load); the tail, the ears, the blink and the «z» move with CSS in
// style.css and stand still for those who asked for less motion.
func mascot(id int64) template.HTML {
	n := int(id % 1000)
	if n < 0 {
		n = -n
	}
	tones := []string{"#efe9df", "#e8e2d6", "#f2ede5", "#e5e0d7", "#ebe5dc"}
	var pose string
	switch n % 4 {
	case 0:
		pose = poseSleep()
	case 1:
		pose = posePeek()
	case 2:
		pose = poseSit()
	default:
		pose = poseYarn()
	}
	return template.HTML(`<svg class="mascot" viewBox="0 0 200 250" role="img" aria-label="Фото скоро будет">` +
		`<rect width="200" height="250" fill="` + tones[n%len(tones)] + `"/>` +
		`<text x="186" y="26" text-anchor="end" font-family="system-ui,sans-serif" font-size="11" font-weight="600" fill="#8a8378">фото скоро</text>` +
		pose + `</svg>`)
}

const (
	ink  = "#2b2724"
	fur  = "#f7f3ec"
	pink = "#e9b8ad"
)

// line is the common stroke of the drawing.
func line(w float64) string {
	return fmt.Sprintf(`stroke="%s" stroke-width="%g" stroke-linecap="round" stroke-linejoin="round"`, ink, w)
}

// tail is a thick furry stroke with an outline: the ink line under a narrower fur line.
func tail(d, class string) string {
	return fmt.Sprintf(`<g class="%s"><path d="%s" fill="none" stroke="%s" stroke-width="13" stroke-linecap="round"/>`+
		`<path d="%s" fill="none" stroke="%s" stroke-width="7.5" stroke-linecap="round"/></g>`, class, d, ink, d, fur)
}

// head draws the kitten's head at (x, y), turned by tilt degrees, looking by (dx, dy); closed eyes sleep.
func head(x, y, tilt, scale, dx, dy float64, closed bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<g transform="translate(%g %g) rotate(%g) scale(%g)">`, x, y, tilt, scale)
	fmt.Fprintf(&b, `<path class="ear" d="M-31 -8 L-27 -47 L-5 -27 Z" fill="%s" %s/>`, fur, line(3))
	fmt.Fprintf(&b, `<path class="ear ear-r" d="M31 -8 L27 -47 L5 -27 Z" fill="%s" %s/>`, fur, line(3))
	fmt.Fprintf(&b, `<path d="M-25 -36 L-23 -26 L-15 -28 Z M25 -36 L23 -26 L15 -28 Z" fill="%s"/>`, pink)
	fmt.Fprintf(&b, `<ellipse rx="36" ry="29" fill="%s" %s/>`, fur, line(3))
	// three stripes on the forehead
	fmt.Fprintf(&b, `<path d="M0 -27 v8 M-8 -26 l2 7 M8 -26 l-2 7" fill="none" %s/>`, line(2.2))
	if closed {
		fmt.Fprintf(&b, `<path d="M-20 0 q7 6 14 0 M6 0 q7 6 14 0" fill="none" %s/>`, line(2.8))
	} else {
		fmt.Fprintf(&b, `<g class="blink"><ellipse cx="%g" cy="%g" rx="6" ry="7.5" fill="%s"/><ellipse cx="%g" cy="%g" rx="6" ry="7.5" fill="%s"/>`+
			`<circle cx="%g" cy="%g" r="2.2" fill="#fff"/><circle cx="%g" cy="%g" r="2.2" fill="#fff"/></g>`,
			-13+dx, -1+dy, ink, 13+dx, -1+dy, ink, -15+dx, -4+dy, 11+dx, -4+dy)
	}
	fmt.Fprintf(&b, `<path d="M-3.5 9 h7 l-3.5 3.5 z" fill="%s"/>`, ink)
	fmt.Fprintf(&b, `<path d="M0 12.5 q-3 5 -7 3 M0 12.5 q3 5 7 3" fill="none" %s/>`, line(2.2))
	fmt.Fprintf(&b, `<path d="M-22 9 h-17 M-22 14 l-16 5 M22 9 h17 M22 14 l16 5" fill="none" %s/>`, line(1.5))
	b.WriteString(`</g>`)
	return b.String()
}

func shadow(x, y, rx float64) string {
	return fmt.Sprintf(`<ellipse cx="%g" cy="%g" rx="%g" ry="5" fill="%s" opacity=".08"/>`, x, y, rx, ink)
}

// poseSleep: curled up on a cushion, tail around the paws, a «z» rising now and then.
func poseSleep() string {
	return shadow(100, 232, 74) +
		fmt.Sprintf(`<ellipse cx="100" cy="214" rx="74" ry="20" fill="#ddd3c4" %s/>`, line(3)) +
		`<ellipse cx="100" cy="208" rx="62" ry="12" fill="#e7dfd2"/>` +
		fmt.Sprintf(`<ellipse cx="108" cy="190" rx="50" ry="27" fill="%s" %s/>`, fur, line(3)) +
		tail("M150 198 C150 222 92 224 70 210", "tail tail-slow") +
		head(70, 178, -14, .78, 0, 0, true) +
		fmt.Sprintf(`<text class="zzz" x="112" y="150" font-family="system-ui,sans-serif" font-size="16" font-weight="800" fill="%s">z</text>`, ink) +
		fmt.Sprintf(`<text class="zzz zzz-2" x="126" y="134" font-family="system-ui,sans-serif" font-size="12" font-weight="800" fill="%s">z</text>`, ink)
}

// posePeek: only the head and the paws above the bottom edge, looking straight at the visitor.
func posePeek() string {
	return head(74, 226, 6, 1.25, 1, -1, false) +
		fmt.Sprintf(`<ellipse cx="46" cy="246" rx="13" ry="9" fill="%s" %s/>`, fur, line(3)) +
		fmt.Sprintf(`<ellipse cx="104" cy="246" rx="13" ry="9" fill="%s" %s/>`, fur, line(3)) +
		fmt.Sprintf(`<path d="M41 242 v5 M46 241 v6 M51 242 v5 M99 242 v5 M104 241 v6 M109 242 v5" %s/>`, line(1.6))
}

// poseSit: sitting at the right edge, head turned to the page and tilted with interest, tail swaying.
func poseSit() string {
	return shadow(146, 233, 40) +
		tail("M170 224 C200 220 198 182 182 174", "tail") +
		fmt.Sprintf(`<path d="M118 232 C100 232 98 196 110 172 C120 154 166 154 176 172 C188 196 186 232 168 232 Z" fill="%s" %s/>`, fur, line(3)) +
		fmt.Sprintf(`<path d="M132 232 v-22 M154 232 v-22" fill="none" %s/>`, line(2.4)) +
		fmt.Sprintf(`<ellipse cx="129" cy="232" rx="10" ry="5" fill="%s" %s/><ellipse cx="157" cy="232" rx="10" ry="5" fill="%s" %s/>`, fur, line(2.6), fur, line(2.6)) +
		head(142, 142, -12, .95, -3.5, -2.5, false)
}

// poseYarn: lying on the belly, one paw reaching for a ball of yarn, eyes on the ball.
func poseYarn() string {
	return shadow(112, 234, 80) +
		tail("M164 214 C186 206 190 176 176 166", "tail") +
		fmt.Sprintf(`<ellipse cx="128" cy="214" rx="50" ry="20" fill="%s" %s/>`, fur, line(3)) +
		fmt.Sprintf(`<path d="M150 228 q8 6 18 0" fill="none" %s/>`, line(2.4)) +
		fmt.Sprintf(`<path d="M94 226 L64 226" stroke="%s" stroke-width="14" stroke-linecap="round"/><path d="M94 226 L64 226" stroke="%s" stroke-width="8.5" stroke-linecap="round"/>`, ink, fur) +
		`<g class="yarn"><circle cx="40" cy="222" r="14" fill="#e07a52"/>` +
		`<path d="M28 216 q12 -6 24 4 M28 226 q12 -8 24 2 M34 210 q2 12 -2 24" fill="none" stroke="#f6d6c8" stroke-width="1.8" stroke-linecap="round"/></g>` +
		`<path d="M40 236 q20 6 30 -2" fill="none" stroke="#e07a52" stroke-width="1.8" stroke-linecap="round"/>` +
		head(98, 188, -8, .82, -3.5, 3, false)
}
