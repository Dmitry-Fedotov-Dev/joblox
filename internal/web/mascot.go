package web

import (
	"fmt"
	"html/template"
)

// mascot is the picture of a product without photos: a cartoon kitten holding a «фото скоро» sign. The
// product id picks the background, the eyes and the accessory, so a shop full of such cards still looks
// varied. Plain inline SVG: no files, nothing to load, sharp on every screen.
func mascot(id int64) template.HTML {
	bgs := [][2]string{{"#ffd8e4", "#ffb3c9"}, {"#ffe2a6", "#ffc95c"}, {"#cfe0ff", "#a3c1ff"}, {"#cdeedb", "#97d8b3"},
		{"#e2d8ff", "#c3b0ff"}, {"#ffd2bd", "#ffad88"}}
	n := int(id % 1000)
	if n < 0 {
		n = -n
	}
	bg := bgs[n%len(bgs)]
	eyes := `<circle cx="84" cy="104" r="7" fill="#141312"/><circle cx="116" cy="104" r="7" fill="#141312"/>` +
		`<circle cx="86" cy="101" r="2.4" fill="#fff"/><circle cx="118" cy="101" r="2.4" fill="#fff"/>`
	if n%3 == 1 { // happy, squinting
		eyes = `<path d="M76 106q8-9 16 0M108 106q8-9 16 0" stroke="#141312" stroke-width="4.5" fill="none" stroke-linecap="round"/>`
	}
	extra := ""
	switch n % 4 {
	case 1: // a bow between the ears
		extra = `<path d="M100 64l-16-9v18zM100 64l16-9v18z" fill="#ff5a1f"/><circle cx="100" cy="64" r="5" fill="#ff5a1f"/>`
	case 2: // a party hat
		extra = `<path d="M100 26l15 36h-30z" fill="#ff5a1f"/><circle cx="100" cy="25" r="5" fill="#fff"/>` +
			`<path d="M92 44l9 4M89 54l14 5" stroke="#fff" stroke-width="3" stroke-linecap="round"/>`
	case 3: // whiskers turned up and a tongue
		extra = `<path d="M96 128q4 7 8 0" fill="#ff8fa3"/>`
	}
	return template.HTML(fmt.Sprintf(`<svg class="mascot" viewBox="0 0 200 250" role="img" aria-label="Фото скоро будет">`+
		`<defs><linearGradient id="mg%d" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="%s"/><stop offset="1" stop-color="%s"/></linearGradient></defs>`+
		`<rect width="200" height="250" fill="url(#mg%d)"/>`+
		`<circle cx="36" cy="40" r="5" fill="#fff" opacity=".7"/><circle cx="168" cy="58" r="3.5" fill="#fff" opacity=".7"/><circle cx="160" cy="24" r="2.5" fill="#fff" opacity=".7"/>`+
		`<ellipse cx="100" cy="232" rx="58" ry="8" fill="#141312" opacity=".08"/>`+
		`<path d="M58 92L54 50l34 22zM142 92l4-42-34 22z" fill="#9b93a8"/><path d="M62 82l-2-22 16 12zM138 82l2-22-16 12z" fill="#f2b8c6"/>`+
		`<ellipse cx="100" cy="112" rx="50" ry="44" fill="#b3abbf"/>`+
		`<ellipse cx="100" cy="190" rx="40" ry="38" fill="#b3abbf"/>`+
		`%s`+
		`<ellipse cx="72" cy="120" rx="8" ry="5" fill="#ff8fa3" opacity=".6"/><ellipse cx="128" cy="120" rx="8" ry="5" fill="#ff8fa3" opacity=".6"/>`+
		`<path d="M100 116l-5-5h10z" fill="#ff8fa3"/><path d="M100 117q-4 8-10 6M100 117q4 8 10 6" stroke="#141312" stroke-width="2.5" fill="none" stroke-linecap="round"/>`+
		`<path d="M52 114h-20M52 122l-18 5M148 114h20M148 122l18 5" stroke="#fff" stroke-width="2.2" stroke-linecap="round" opacity=".85"/>`+
		`%s`+
		`<g transform="rotate(-6 100 186)"><rect x="52" y="160" width="96" height="48" rx="10" fill="#fffdf8" stroke="#141312" stroke-width="3"/>`+
		`<text x="100" y="182" text-anchor="middle" font-family="system-ui,sans-serif" font-size="15" font-weight="800" fill="#141312">фото</text>`+
		`<text x="100" y="199" text-anchor="middle" font-family="system-ui,sans-serif" font-size="13" font-weight="700" fill="#ff5a1f">скоро</text></g>`+
		`<ellipse cx="62" cy="182" rx="11" ry="9" fill="#9b93a8"/><ellipse cx="138" cy="182" rx="11" ry="9" fill="#9b93a8"/>`+
		`</svg>`, n, bg[0], bg[1], n, eyes, extra))
}
