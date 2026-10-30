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
