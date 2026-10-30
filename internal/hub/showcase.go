package hub

import "net/http"

// Showcase is a ready-made template shown on the market with a phone mockup: what a performer's site
// built from that profile looks like, before anyone launches one.
type Showcase struct {
	ID, Title, Lead string
	For, Features   []string
	Demo            string // a live demo site built from the template
}

var showcases = map[string]Showcase{
	"master": {ID: "master", Title: "Мастер на выезд", Demo: "https://master-demo.openline.one/",
		Lead: "Сайт для мастера, который работает у клиента: клиент выбирает, что сделать, описывает задачу и удобное время, мастер называет цену.",
		For: []string{"Сборка мебели, полки, карнизы, мелкий ремонт", "Сантехника и электрика по мелочи",
			"Ручная работа на заказ: пошив, вязание, резьба", "Уборка, мойка окон, помощь по хозяйству"},
		Features: []string{"Заявка с выбором работы, адресом и временем", "Цену называете вы, клиент подтверждает её на сайте",
			"Статусы «Записан», «Приехал», «Готово» с письмами клиенту", "Оплата после работы: на месте или переводом по вашим реквизитам",
			"Звонок клиенту по ссылке без номера, пока заказ открыт", "Заявки приходят в Telegram"}},
	"shop": {ID: "shop", Title: "Магазин с фото", Demo: "https://shop-demo.openline.one/",
		Lead: "Свой интернет-магазин: товары с фото и размерами, остатки, корзина. Товары добавляются с телефона, без программиста.",
		For:  []string{"Одежда и вещи ручной работы", "Лавка у дома, фермерские продукты", "Сувениры, украшения, косметика"},
		Features: []string{"До 12 фото у товара: программа сама поворачивает и уменьшает их и убирает место съёмки",
			"Размеры и остаток: последнюю вещь купит только один покупатель", "Отмена или неоплата возвращает товар в продажу",
			"Метки «Последний», «Скидка», «Новинка» ставятся сами", "Оплата переводом по вашим реквизитам или при получении",
			"Заказы приходят в Telegram"}},
}

func (h *Hub) showcase(w http.ResponseWriter, r *http.Request) {
	sc, ok := showcases[r.PathValue("id")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.render(w, "showcase.html", struct {
		Showcase
		Contact string
	}{sc, h.Contact})
}
