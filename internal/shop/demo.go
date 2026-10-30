package shop

import (
	"context"
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
