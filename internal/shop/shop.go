// Package shop is the catalog of a shop with photos: products, their sizes or other variants, stock and the
// reservation that keeps two customers from buying the last item twice. The owner edits it in the browser;
// the order core sees it as a list of priced items, one per variant.
//
// It lives in the shop's SQLite file next to the orders, with its own schema version, so a profile without
// the shop module never creates these tables.
package shop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// Product is one card of the storefront.
type Product struct {
	ID          int64
	Slug        string // the address of the product page: /p/<slug>
	Title       string
	Description string
	Price       money.Amount
	OldPrice    money.Amount // crossed out next to the price; 0 when there is no discount
	Hidden      bool
	Position    int
	Images      []Image
	Variants    []Variant
	CreatedAt   time.Time
}

// Variant is what the customer picks: a size, a colour or the single "one size". Stock is how many are left
// to sell; reserved items of open orders are already taken off.
type Variant struct {
	ID    int64
	Name  string // empty for a product without choices
	Stock int
}

// Image is a processed photo; File names the two JPEGs in the photo directory: File+".jpg" and File+"_t.jpg".
type Image struct {
	ID   int64
	File string
	W, H int
}

// InStock says whether anything of the product can be bought.
func (p *Product) InStock() bool {
	for _, v := range p.Variants {
		if v.Stock > 0 {
			return true
		}
	}
	return false
}

// Stock is the sum over variants.
func (p *Product) Stock() int {
	n := 0
	for _, v := range p.Variants {
		n += v.Stock
	}
	return n
}

// Cover is the first photo, or nil.
func (p *Product) Cover() *Image {
	if len(p.Images) == 0 {
		return nil
	}
	return &p.Images[0]
}

// Variant finds a variant of the product.
func (p *Product) Variant(id int64) (Variant, bool) {
	for _, v := range p.Variants {
		if v.ID == id {
			return v, true
		}
	}
	return Variant{}, false
}

// ItemID is how a variant appears in orders and in the checkout form: "v" and the variant id.
func ItemID(variant int64) string { return "v" + strconv.FormatInt(variant, 10) }

// ParseItemID is the reverse of ItemID.
func ParseItemID(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "v"), 10, 64)
	return n, strings.HasPrefix(s, "v") && err == nil && n > 0
}

var (
	ErrNotFound   = errors.New("товар не найден")
	ErrOutOfStock = errors.New("товар закончился")
)

// OutOfStock names what could not be reserved; it wraps ErrOutOfStock.
type OutOfStock struct {
	Item string // ItemID of the variant
	Left int
}

func (e *OutOfStock) Error() string { return fmt.Sprintf("%s: осталось %d", e.Item, e.Left) }
func (e *OutOfStock) Unwrap() error { return ErrOutOfStock }

// Catalog keeps the products. Build it with Open over the shop's database.
type Catalog struct {
	db  *sql.DB
	now func() time.Time
	// Services: the catalog holds a performer's services, not goods: no sizes and no stock, the price is a
	// «from» price and 0 means «by agreement».
	Services bool
}

var migrations = []string{
	`CREATE TABLE shop_products (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT NOT NULL UNIQUE,
		title TEXT NOT NULL,
		description TEXT NOT NULL,
		price INTEGER NOT NULL,
		old_price INTEGER NOT NULL DEFAULT 0,
		hidden INTEGER NOT NULL DEFAULT 0,
		position INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE shop_variants (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		product_id INTEGER NOT NULL REFERENCES shop_products(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		stock INTEGER NOT NULL CHECK (stock >= 0),
		position INTEGER NOT NULL,
		gone INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX shop_variants_product ON shop_variants(product_id, position);
	CREATE TABLE shop_images (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		product_id INTEGER NOT NULL REFERENCES shop_products(id) ON DELETE CASCADE,
		file TEXT NOT NULL,
		w INTEGER NOT NULL,
		h INTEGER NOT NULL,
		position INTEGER NOT NULL
	);
	CREATE INDEX shop_images_product ON shop_images(product_id, position);`,
}

// Open brings the shop tables up to date in db (the orders database).
func Open(ctx context.Context, db *sql.DB) (*Catalog, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS shop_schema (v INTEGER NOT NULL)`); err != nil {
		return nil, err
	}
	var v int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(v), 0) FROM shop_schema`).Scan(&v); err != nil {
		return nil, err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("shop migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO shop_schema (v) VALUES (?)`, i+1); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return &Catalog{db: db, now: func() time.Time { return time.Now().UTC() }}, nil
}

// List returns the products in the owner's order; all includes the hidden ones.
func (c *Catalog) List(ctx context.Context, all bool) ([]*Product, error) {
	q := `SELECT id, slug, title, description, price, old_price, hidden, position, created_at FROM shop_products`
	if !all {
		q += ` WHERE hidden = 0`
	}
	rows, err := c.db.QueryContext(ctx, q+` ORDER BY position, id DESC`)
	if err != nil {
		return nil, err
	}
	var list []*Product
	byID := map[int64]*Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		list = append(list, p)
		byID[p.ID] = p
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := c.fill(ctx, byID); err != nil {
		return nil, err
	}
	return list, nil
}

type scanner interface{ Scan(...any) error }

func scanProduct(r scanner) (*Product, error) {
	var p Product
	var price, old, created int64
	var hidden int
	if err := r.Scan(&p.ID, &p.Slug, &p.Title, &p.Description, &price, &old, &hidden, &p.Position, &created); err != nil {
		return nil, err
	}
	p.Price, p.OldPrice, p.Hidden, p.CreatedAt = money.Amount(price), money.Amount(old), hidden != 0, time.UnixMilli(created).UTC()
	return &p, nil
}

// fill loads variants and photos for the products in two queries.
func (c *Catalog) fill(ctx context.Context, byID map[int64]*Product) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := c.db.QueryContext(ctx, `SELECT id, product_id, name, stock FROM shop_variants WHERE gone = 0 ORDER BY position, id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v Variant
		var pid int64
		if err := rows.Scan(&v.ID, &pid, &v.Name, &v.Stock); err != nil {
			_ = rows.Close()
			return err
		}
		if p := byID[pid]; p != nil {
			p.Variants = append(p.Variants, v)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = c.db.QueryContext(ctx, `SELECT id, product_id, file, w, h FROM shop_images ORDER BY position, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var im Image
		var pid int64
		if err := rows.Scan(&im.ID, &pid, &im.File, &im.W, &im.H); err != nil {
			return err
		}
		if p := byID[pid]; p != nil {
			p.Images = append(p.Images, im)
		}
	}
	return rows.Err()
}

func (c *Catalog) one(ctx context.Context, where string, arg any) (*Product, error) {
	row := c.db.QueryRowContext(ctx, `SELECT id, slug, title, description, price, old_price, hidden, position, created_at
		FROM shop_products WHERE `+where, arg)
	p, err := scanProduct(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, c.fill(ctx, map[int64]*Product{p.ID: p})
}

// Get returns a product by id, hidden or not.
func (c *Catalog) Get(ctx context.Context, id int64) (*Product, error) {
	return c.one(ctx, "id = ?", id)
}

// BySlug returns a product by its address, hidden or not.
func (c *Catalog) BySlug(ctx context.Context, slug string) (*Product, error) {
	return c.one(ctx, "slug = ?", slug)
}

// VariantInput is one line of the owner's "sizes and stock" list.
type VariantInput struct {
	ID    int64 // 0 for a new variant
	Name  string
	Stock int
}

// Input is the owner's product form.
type Input struct {
	Title, Description string
	Price, OldPrice    money.Amount
	Hidden             bool
	Variants           []VariantInput
}

// Problems are the mistakes of the product form, shown to the owner all at once.
type Problems []string

func (p Problems) Error() string { return strings.Join(p, ", ") }

func (in *Input) clean(services bool) Problems {
	var bad Problems
	in.Title = strings.Join(strings.Fields(in.Title), " ")
	in.Description = strings.TrimSpace(strings.ReplaceAll(in.Description, "\r\n", "\n"))
	if n := utf8.RuneCountInString(in.Title); n < 2 || n > 80 {
		bad = append(bad, "название от 2 до 80 букв")
	}
	if utf8.RuneCountInString(in.Description) > 3000 {
		bad = append(bad, "описание не длиннее 3000 букв")
	}
	if in.Price < 0 || in.Price == 0 && !services {
		bad = append(bad, "цена больше нуля")
	}
	if in.OldPrice != 0 && in.OldPrice <= in.Price {
		bad = append(bad, "старая цена больше новой или пусто")
	}
	seen := map[string]bool{}
	var vs []VariantInput
	for _, v := range in.Variants {
		v.Name = strings.Join(strings.Fields(v.Name), " ")
		if v.Name == "" && v.ID == 0 && v.Stock == 0 {
			continue // an empty row of the form
		}
		if utf8.RuneCountInString(v.Name) > 30 {
			bad = append(bad, "размер не длиннее 30 букв")
		}
		if v.Stock < 0 || v.Stock > 100000 {
			bad = append(bad, "остаток от 0 до 100000")
		}
		key := strings.ToLower(v.Name)
		if seen[key] {
			bad = append(bad, "размер «"+v.Name+"» указан дважды")
		}
		seen[key] = true
		vs = append(vs, v)
	}
	if services {
		vs = nil // a service has no sizes and no stock
	} else if len(vs) == 0 {
		bad = append(bad, "хотя бы одна строка с остатком")
	}
	if len(vs) > 1 {
		for _, v := range vs {
			if v.Name == "" {
				bad = append(bad, "у каждого размера есть название, когда их несколько")
				break
			}
		}
	}
	in.Variants = vs
	return bad
}

// Save creates (id 0) or updates a product and returns its id. A variant missing from the input is retired,
// not deleted: past orders still point at it.
func (c *Catalog) Save(ctx context.Context, id int64, in Input) (int64, error) {
	if bad := in.clean(c.Services); len(bad) > 0 {
		return 0, bad
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if id == 0 {
		var pos int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MIN(position), 0) - 1 FROM shop_products`).Scan(&pos); err != nil {
			return 0, err
		}
		slug, err := freeSlug(ctx, tx, Slug(in.Title))
		if err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO shop_products (slug, title, description, price, old_price, hidden, position, created_at)
			VALUES (?,?,?,?,?,?,?,?)`, slug, in.Title, in.Description, int64(in.Price), int64(in.OldPrice), b2i(in.Hidden), pos,
			c.now().UnixMilli())
		if err != nil {
			return 0, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE shop_products SET title = ?, description = ?, price = ?, old_price = ?, hidden = ?
			WHERE id = ?`, in.Title, in.Description, int64(in.Price), int64(in.OldPrice), b2i(in.Hidden), id)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, ErrNotFound
		}
	}
	keep := map[int64]bool{}
	for i, v := range in.Variants {
		if v.ID != 0 {
			res, err := tx.ExecContext(ctx, `UPDATE shop_variants SET name = ?, stock = ?, position = ?, gone = 0
				WHERE id = ? AND product_id = ?`, v.Name, v.Stock, i, v.ID, id)
			if err != nil {
				return 0, err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				keep[v.ID] = true
				continue
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO shop_variants (product_id, name, stock, position) VALUES (?,?,?,?)`, id, v.Name, v.Stock, i)
		if err != nil {
			return 0, err
		}
		vid, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		keep[vid] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM shop_variants WHERE product_id = ? AND gone = 0`, id)
	if err != nil {
		return 0, err
	}
	var retire []int64
	for rows.Next() {
		var vid int64
		if err := rows.Scan(&vid); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if !keep[vid] {
			retire = append(retire, vid)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, vid := range retire {
		if _, err := tx.ExecContext(ctx, `UPDATE shop_variants SET gone = 1, stock = 0 WHERE id = ?`, vid); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetHidden shows or hides a product on the storefront.
func (c *Catalog) SetHidden(ctx context.Context, id int64, hidden bool) error {
	_, err := c.db.ExecContext(ctx, `UPDATE shop_products SET hidden = ? WHERE id = ?`, b2i(hidden), id)
	return err
}

// Delete removes a product with its variants and photo records and returns the photo files to delete.
// Orders keep their own copy of titles and prices, so history does not change.
func (c *Catalog) Delete(ctx context.Context, id int64) ([]string, error) {
	p, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := c.db.ExecContext(ctx, `DELETE FROM shop_products WHERE id = ?`, id); err != nil {
		return nil, err
	}
	var files []string
	for _, im := range p.Images {
		files = append(files, im.File)
	}
	return files, nil
}

// MaxImages per product.
const MaxImages = 12

// AddImage attaches a processed photo at the end of the gallery.
func (c *Catalog) AddImage(ctx context.Context, product int64, im Image) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var n, pos int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(position), -1) + 1 FROM shop_images WHERE product_id = ?`,
		product).Scan(&n, &pos); err != nil {
		return err
	}
	if n >= MaxImages {
		return fmt.Errorf("не больше %d фото у товара", MaxImages)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO shop_images (product_id, file, w, h, position) VALUES (?,?,?,?,?)`,
		product, im.File, im.W, im.H, pos); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteImage removes a photo of the product and returns its file name.
func (c *Catalog) DeleteImage(ctx context.Context, product, image int64) (string, error) {
	var file string
	err := c.db.QueryRowContext(ctx, `SELECT file FROM shop_images WHERE id = ? AND product_id = ?`, image, product).Scan(&file)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	_, err = c.db.ExecContext(ctx, `DELETE FROM shop_images WHERE id = ?`, image)
	return file, err
}

// CoverImage moves a photo to the front of the gallery.
func (c *Catalog) CoverImage(ctx context.Context, product, image int64) error {
	_, err := c.db.ExecContext(ctx, `UPDATE shop_images SET position = (SELECT COALESCE(MIN(position), 0) - 1 FROM shop_images
		WHERE product_id = ?) WHERE id = ? AND product_id = ?`, product, image, product)
	return err
}

// Move shifts a product one place up (-1) or down (+1) on the storefront.
func (c *Catalog) Move(ctx context.Context, id int64, dir int) error {
	list, err := c.List(ctx, true)
	if err != nil {
		return err
	}
	i := -1
	for k, p := range list {
		if p.ID == id {
			i = k
		}
	}
	j := i + dir
	if i < 0 || j < 0 || j >= len(list) {
		return nil
	}
	list[i], list[j] = list[j], list[i]
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for k, p := range list {
		if _, err := tx.ExecContext(ctx, `UPDATE shop_products SET position = ? WHERE id = ?`, k, p.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Items is the catalog as the order core prices it: one item per variant that is on sale, with the stock as
// the most one order may take. Hidden products are left out, so a stale cart cannot buy them.
func (c *Catalog) Items(ctx context.Context) ([]profile.Item, error) {
	list, err := c.List(ctx, false)
	if err != nil {
		return nil, err
	}
	var items []profile.Item
	for _, p := range list {
		for _, v := range p.Variants {
			title := p.Title
			if v.Name != "" {
				title += ", " + v.Name
			}
			items = append(items, profile.Item{ID: ItemID(v.ID), Title: title, Price: p.Price, Unit: "шт", MaxQty: v.Stock,
				Hidden: v.Stock == 0})
		}
	}
	return items, nil
}

// Reserve takes the quantities (by ItemID) off the stock in one transaction: all or nothing.
func (c *Catalog) Reserve(ctx context.Context, qty map[string]int) error {
	return c.adjust(ctx, qty, -1)
}

// Release puts the quantities back, when an order is cancelled or expires.
func (c *Catalog) Release(ctx context.Context, qty map[string]int) error {
	return c.adjust(ctx, qty, +1)
}

func (c *Catalog) adjust(ctx context.Context, qty map[string]int, sign int) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for item, n := range qty {
		id, ok := ParseItemID(item)
		if !ok || n <= 0 {
			continue
		}
		if sign > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE shop_variants SET stock = stock + ? WHERE id = ? AND gone = 0`, n, id); err != nil {
				return err
			}
			continue
		}
		res, err := tx.ExecContext(ctx, `UPDATE shop_variants SET stock = stock - ? WHERE id = ? AND gone = 0 AND stock >= ?`, n, id, n)
		if err != nil {
			return err
		}
		if done, _ := res.RowsAffected(); done == 0 {
			var left int
			_ = tx.QueryRowContext(ctx, `SELECT stock FROM shop_variants WHERE id = ? AND gone = 0`, id).Scan(&left)
			return &OutOfStock{Item: item, Left: left}
		}
	}
	return tx.Commit()
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "y",
	'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f",
	'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'ң': "n", 'ө': "o", 'ү': "u", 'қ': "k", 'ғ': "g", 'ә': "a", 'һ': "h", 'і': "i",
}

// Slug makes a readable latin address from a title: "Платье льняное" → "plate-lnyanoe".
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		s, ok := translit[r]
		switch {
		case ok:
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			s = string(r)
		default:
			s = "-"
		}
		if s == "-" {
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = true
			continue
		}
		if s != "" {
			b.WriteString(s)
			dash = false
		}
		if b.Len() >= 60 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "item"
	}
	return out
}

func freeSlug(ctx context.Context, tx *sql.Tx, base string) (string, error) {
	slug := base
	for i := 2; ; i++ {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM shop_products WHERE slug = ?`, slug).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return slug, nil
		}
		slug = base + "-" + strconv.Itoa(i)
	}
}

// Short tells the order core which item ran out.
func (e *OutOfStock) Short() (string, int) { return e.Item, e.Left }
