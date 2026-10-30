package web

import (
	"fmt"
	"html/template"
)

// kittens is how many pictures there are in static/kittens.
const kittens = 9

// mascot is the picture of a product without photos: one of the kittens from static/kittens (asleep, in a box,
// after a butterfly, with a ball of yarn …), picked by the product id, so neighbouring cards differ.
func mascot(id int64) template.HTML {
	n := id % kittens
	if n < 0 {
		n = -n
	}
	return template.HTML(fmt.Sprintf(`<img class="mascot" src="/static/kittens/kitten-%d.jpg" alt="Фото скоро будет" width="480" height="600" loading="lazy" decoding="async">`, n+1))
}

// knit is the knitting shop's empty picture: a soft plain field, the yarn frame (below) does the rest.
func knit(int64) template.HTML {
	return `<span class="mascot knitbg" role="img" aria-label="Фото скоро будет"></span>`
}

// yarn frames a card without photos in a knitting shop: crossed needles in the top right corner with a bear or
// a bunny being knitted out from under them, the thread running round the card to a ball of yarn. The kitten
// may knock the ball off now and then (kitten.js): the thread snaps, winds back and wraps the card again.
func yarn(id int64) template.HTML {
	toy := "bear"
	if id%2 != 0 {
		toy = "bunny"
	}
	return template.HTML(fmt.Sprintf(`<span class="yarn y-%[1]s" aria-hidden="true">`+
		`<img class="needles" src="/static/knit/knit-%[1]s.gif" alt="" width="84" height="90" loading="lazy" decoding="async">`+
		`<span class="loop"><span class="thread"></span>`+
		`<img class="ball" src="/static/knit/ball-%[1]s.png" alt="" width="48" height="36" loading="lazy" decoding="async"></span></span>`, toy))
}

// spins is how many pictures there are in static/spin.
const spins = 4

// spin is the picture of a preorder without photos: Dymok, the site's kitten, walking slowly round after his tail in
// soft pink and pale violet, a calm «it is being made» rather than a hurry.
func spin(id int64) template.HTML {
	n := id % spins
	if n < 0 {
		n = -n
	}
	// under the pointer the walking kitten gives way to a purring one with hearts rising (CSS .love)
	return template.HTML(fmt.Sprintf(`<img class="mascot pixel" src="/static/spin/spin-%d.gif" alt="Готовится под заказ" width="200" height="248" loading="lazy" decoding="async">`+
		`<img class="mascot pixel love" src="/static/spin/love-%d.gif" alt="" width="200" height="248" loading="lazy" decoding="async">`, n+1, n+1))
}
