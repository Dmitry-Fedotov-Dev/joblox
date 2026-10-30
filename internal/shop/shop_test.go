package shop

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
)

func open(t *testing.T) *Catalog {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "shop.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	c, err := Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), db); err != nil { // the second start finds the tables in place
		t.Fatal(err)
	}
	return c
}

func dress(stockS, stockM int) Input {
	return Input{Title: "Платье льняное", Description: "Лён, на пуговицах", Price: money.Amount(450000),
		Variants: []VariantInput{{Name: "S", Stock: stockS}, {Name: "M", Stock: stockM}}}
}

func TestSaveAndList(t *testing.T) {
	ctx := context.Background()
	c := open(t)
	id, err := c.Save(ctx, 0, dress(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "plate-lnyanoe" || len(p.Variants) != 2 || p.Stock() != 3 || !p.InStock() {
		t.Fatalf("product: %+v", p)
	}
	if _, err := c.Save(ctx, 0, dress(0, 0)); err != nil {
		t.Fatal(err)
	}
	second, _ := c.BySlug(ctx, "plate-lnyanoe-2")
	if second == nil || second.InStock() {
		t.Fatalf("second product with the same title: %+v", second)
	}
	list, _ := c.List(ctx, false)
	if len(list) != 2 || list[0].ID != second.ID {
		t.Fatalf("newest first: %+v", list)
	}

	// the owner renames M, drops S and adds L: S is retired, M keeps its id
	m := p.Variants[1]
	in := dress(0, 0)
	in.Variants = []VariantInput{{ID: m.ID, Name: "M", Stock: 5}, {Name: "L", Stock: 1}}
	if _, err := c.Save(ctx, id, in); err != nil {
		t.Fatal(err)
	}
	p, _ = c.Get(ctx, id)
	if len(p.Variants) != 2 || p.Variants[0].ID != m.ID || p.Variants[0].Stock != 5 || p.Variants[1].Name != "L" {
		t.Fatalf("variants after edit: %+v", p.Variants)
	}

	if err := c.SetHidden(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	list, _ = c.List(ctx, false)
	all, _ := c.List(ctx, true)
	if len(list) != 1 || len(all) != 2 {
		t.Fatalf("hidden: %d shown, %d in all", len(list), len(all))
	}
	items, _ := c.Items(ctx)
	for _, it := range items {
		if it.ID == ItemID(m.ID) {
			t.Fatal("a hidden product is on sale")
		}
	}
}

func TestInputProblems(t *testing.T) {
	ctx := context.Background()
	c := open(t)
	_, err := c.Save(ctx, 0, Input{Title: "x", Price: 10, OldPrice: 5, Variants: []VariantInput{{Name: "S", Stock: 1}, {Name: "s", Stock: 2}}})
	var bad Problems
	if !errors.As(err, &bad) || len(bad) != 3 {
		t.Fatalf("want 3 problems, got %v", err)
	}
	if _, err := c.Save(ctx, 0, Input{Title: "Шарф", Price: 100, Variants: []VariantInput{{Stock: 3}}}); err != nil {
		t.Fatalf("one size without a name: %v", err)
	}
}

func TestReserveRelease(t *testing.T) {
	ctx := context.Background()
	c := open(t)
	id, _ := c.Save(ctx, 0, dress(1, 2))
	p, _ := c.Get(ctx, id)
	s, m := ItemID(p.Variants[0].ID), ItemID(p.Variants[1].ID)

	// all or nothing: M is there, S is not enough, so M stays untouched
	err := c.Reserve(ctx, map[string]int{m: 1, s: 2})
	var oos *OutOfStock
	if !errors.As(err, &oos) || oos.Item != s || oos.Left != 1 || !errors.Is(err, ErrOutOfStock) {
		t.Fatalf("want out of stock for S, got %v", err)
	}
	if p, _ = c.Get(ctx, id); p.Stock() != 3 {
		t.Fatalf("a failed reservation changed the stock: %d", p.Stock())
	}
	if err := c.Reserve(ctx, map[string]int{s: 1, m: 2}); err != nil {
		t.Fatal(err)
	}
	if p, _ = c.Get(ctx, id); p.InStock() {
		t.Fatal("everything is reserved but the product is in stock")
	}
	if err := c.Release(ctx, map[string]int{m: 2}); err != nil {
		t.Fatal(err)
	}
	if p, _ = c.Get(ctx, id); p.Variants[1].Stock != 2 {
		t.Fatalf("released: %+v", p.Variants)
	}
}

// The last item goes to one buyer only, however many press the button at once.
func TestReserveRace(t *testing.T) {
	ctx := context.Background()
	c := open(t)
	id, _ := c.Save(ctx, 0, Input{Title: "Последнее платье", Price: 100, Variants: []VariantInput{{Stock: 1}}})
	p, _ := c.Get(ctx, id)
	item := ItemID(p.Variants[0].ID)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.Reserve(ctx, map[string]int{item: 1}) == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d buyers got the last item", won)
	}
}

func TestMoveAndImages(t *testing.T) {
	ctx := context.Background()
	c := open(t)
	a, _ := c.Save(ctx, 0, Input{Title: "Первый", Price: 1, Variants: []VariantInput{{Stock: 1}}})
	b, _ := c.Save(ctx, 0, Input{Title: "Второй", Price: 1, Variants: []VariantInput{{Stock: 1}}})
	if err := c.Move(ctx, a, -1); err != nil {
		t.Fatal(err)
	}
	list, _ := c.List(ctx, true)
	if list[0].ID != a || list[1].ID != b {
		t.Fatalf("order after move: %d, %d", list[0].ID, list[1].ID)
	}
	for _, f := range []string{"aaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbb"} {
		if err := c.AddImage(ctx, a, Image{File: f, W: 10, H: 10}); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := c.Get(ctx, a)
	if err := c.CoverImage(ctx, a, p.Images[1].ID); err != nil {
		t.Fatal(err)
	}
	if p, _ = c.Get(ctx, a); p.Cover().File != "bbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("cover: %+v", p.Images)
	}
	files, err := c.Delete(ctx, a)
	if err != nil || len(files) != 2 {
		t.Fatalf("delete: %v %v", files, err)
	}
	if _, err := c.Get(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted product: %v", err)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Платье льняное":       "plate-lnyanoe",
		"  Шёлк №5 — синий!  ": "shelk-5-siniy",
		"Coat 2026":            "coat-2026",
		"🧶":                    "item",
		"Көйнөк үчүн жип (кызыл)": "koynok-uchun-zhip-kyzyl",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// exifJPEG is a w×h JPEG with a red left half and an EXIF orientation tag.
func exifJPEG(t *testing.T, w, h, orientation int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x < w/2 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	var body bytes.Buffer
	if err := jpeg.Encode(&body, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	// TIFF header (little endian), one IFD entry: orientation, SHORT, count 1, value
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(orientation), 0, 0, 0, 0, 0, 0, 0}
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, byte((len(app1) + 2) >> 8), byte(len(app1) + 2)}
	raw := body.Bytes()
	out := append([]byte{}, raw[:2]...)
	out = append(out, seg...)
	out = append(out, app1...)
	return append(out, raw[2:]...)
}

func TestPhotoOrientationAndSize(t *testing.T) {
	ph := &Photos{Dir: t.TempDir()}
	raw := exifJPEG(t, 2000, 1000, 6) // a phone held upright: stored sideways, tag says turn 90° clockwise
	if exifOrientation(raw) != 6 {
		t.Fatalf("orientation not read: %d", exifOrientation(raw))
	}
	im, err := ph.Save(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if im.W != 800 || im.H != 1600 {
		t.Fatalf("full size %dx%d, want 800x1600", im.W, im.H)
	}
	f, err := os.Open(filepath.Join(ph.Dir, im.File+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := jpeg.Decode(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	// turned clockwise, the red left half is on top now
	if r, g, _, _ := got.At(400, 100).RGBA(); r>>8 < 200 || g>>8 > 80 {
		t.Fatalf("top is not red: %d %d", r>>8, g>>8)
	}
	saved, _ := os.ReadFile(filepath.Join(ph.Dir, im.File+".jpg"))
	if bytes.Contains(saved, []byte("Exif")) {
		t.Fatal("the saved photo still carries EXIF")
	}
	thumb, err := os.Open(filepath.Join(ph.Dir, im.File+"_t.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(thumb)
	_ = thumb.Close()
	if err != nil || cfg.Height != ThumbSide {
		t.Fatalf("thumbnail: %+v %v", cfg, err)
	}
	if !ValidFile(im.File+".jpg") || !ValidFile(im.File+"_t.jpg") || ValidFile("../x.jpg") {
		t.Fatal("ValidFile")
	}
	ph.Remove(im.File)
	if _, err := os.Stat(filepath.Join(ph.Dir, im.File+".jpg")); !os.IsNotExist(err) {
		t.Fatal("not removed")
	}
	if _, err := ph.Save(bytes.NewReader([]byte("not a picture"))); !errors.Is(err, ErrNotImage) {
		t.Fatalf("want ErrNotImage, got %v", err)
	}
}

func TestSeedDemo(t *testing.T) {
	ctx := context.Background()
	c := open(t)
	for range 2 { // the second start finds the products and adds nothing
		if err := SeedDemo(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := c.List(ctx, true)
	if len(list) != len(demoProducts) || list[0].Title != demoProducts[0].Title {
		t.Fatalf("demo products: %d, first %q", len(list), list[0].Title)
	}
}
