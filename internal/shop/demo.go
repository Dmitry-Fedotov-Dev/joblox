package shop

import (
	"context"
	"strconv"
	"strings"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// demoProducts fill the showcase shop (JOBLOX_DEMO=1) on its first start. They have no photos on purpose:
// the storefront shows its mascot instead, so the demo carries no one's pictures.
var demoProducts = []Input{
	{Title: "Кружка керамическая", Description: "Ручная лепка, глазурь цвета моря. 350 мл, можно в посудомойку.",
		Price: 90000, Variants: []VariantInput{{Stock: 7}}},
	{Title: "Разделочная доска из дуба", Description: "Цельный дуб, пропитка льняным маслом. 40 × 25 см.",
		Price: 240000, OldPrice: 290000, Variants: []VariantInput{{Stock: 3}}},
	{Title: "Варежки из шерсти", Description: "Двойная вязка, тёплые до минус двадцати.",
		Price: 120000, Variants: []VariantInput{{Name: "S", Stock: 2}, {Name: "M", Stock: 1}, {Name: "L", Stock: 0}}},
	{Title: "Свеча соевая «Хвоя»", Description: "Горит 30 часов, хлопковый фитиль, банка остаётся вам.",
		Price: 65000, Variants: []VariantInput{{Stock: 12}}},
	{Title: "Плед вязаный", Description: "Мериносовая шерсть, 130 × 170 см. Один в своём роде.",
		Price: 560000, Variants: []VariantInput{{Stock: 1}}},
	{Title: "Фартук льняной", Description: "Лён с карманом, завязки крест-накрест.",
		Price: 180000, Variants: []VariantInput{{Name: "S–M", Stock: 4}, {Name: "L–XL", Stock: 2}}},
}

// foodProducts are the examples of a bakery or a food stall: made to order, so nothing runs out.
var foodProducts = []Input{
	{Title: "Хлеб на закваске", Description: "Пшенично-ржаной, печём к вашему заказу. 700 г.", Price: 25000, Preorder: true},
	{Title: "Круассаны, 4 шт.", Description: "Сливочное масло, хрустящие, с утра.", Price: 36000, Preorder: true},
	{Title: "Пирог с яблоками", Description: "Открытый, на песочном тесте. Целый, 1 кг.", Price: 90000, Preorder: true},
	{Title: "Плов домашний", Description: "Порция 400 г, в контейнере, горячий к самовывозу.", Price: 45000, Preorder: true},
	{Title: "Сырники со сметаной", Description: "5 штук и сметана отдельно.", Price: 32000, Preorder: true},
	{Title: "Морс клюквенный", Description: "1 литр, без лишнего сахара.", Price: 20000, Preorder: true},
}

// examplePrefix opens the description of every example product: the owner sees what to replace.
const examplePrefix = "Пример товара: замените на свой или удалите в админке. "

// SeedExamples fills the empty catalog of a new installation with example products, so the owner sees the
// storefront working from the first minute: the shop's goods, or a food stall's dishes.
func SeedExamples(ctx context.Context, c *Catalog, food bool) error {
	list, err := c.List(ctx, true)
	if err != nil || len(list) > 0 {
		return err
	}
	src := demoProducts
	if food {
		src = foodProducts
	}
	for i := len(src) - 1; i >= 0; i-- {
		in := src[i]
		in.Description = examplePrefix + in.Description
		if _, err := c.Save(ctx, 0, in); err != nil {
			return err
		}
	}
	return nil
}

// SeedDemo adds the demo products when the catalog is empty.
func SeedDemo(ctx context.Context, c *Catalog) error {
	list, err := c.List(ctx, true)
	if err != nil || len(list) > 0 {
		return err
	}
	for i := len(demoProducts) - 1; i >= 0; i-- { // each new product goes first: add backwards to keep the order
		if _, err := c.Save(ctx, 0, demoProducts[i]); err != nil {
			return err
		}
	}
	return nil
}

// SeedServices fills an empty services catalog with the profile's services: title, description and the
// number in the price text («от 800 сом» → 800; no number: by agreement).
func SeedServices(ctx context.Context, c *Catalog, services []profile.Service) error {
	list, err := c.List(ctx, true)
	if err != nil || len(list) > 0 {
		return err
	}
	for i := len(services) - 1; i >= 0; i-- {
		sv := services[i]
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, sv.Price)
		var price money.Amount
		if n, err := strconv.ParseInt(digits, 10, 64); err == nil {
			price = money.Amount(n * 100)
		}
		if _, err := c.Save(ctx, 0, Input{Title: sv.Title, Description: sv.About, Price: price}); err != nil {
			return err
		}
	}
	return nil
}
