package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/shop"
)

// The owner's catalog: products with photos, sizes and stock, edited from a phone in the owner's area.

const (
	uploadPath  = "/owner/catalog/save"
	uploadLimit = 120 << 20
)

func (s *Server) catalogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /owner/catalog", s.ownerOnly(s.catalogPage))
	mux.HandleFunc("GET /owner/catalog/new", s.ownerOnly(s.itemNew))
	mux.HandleFunc("GET /owner/catalog/{id}", s.ownerOnly(s.itemEdit))
	mux.HandleFunc("POST "+uploadPath, s.ownerOnly(s.itemSave))
	mux.HandleFunc("POST /owner/catalog/{id}/{action}", s.ownerOnly(s.itemAction))
}

type catalogView struct {
	base
	Products []productCard
	Currency string
}

func (s *Server) catalogPage(w http.ResponseWriter, r *http.Request) {
	list, err := s.cfg.Shop.List(r.Context(), true)
	if err != nil {
		s.log.Error("catalog", "err", err)
		s.message(w, r, http.StatusInternalServerError, "Ошибка", "Товары не загрузились, обновите страницу.")
		return
	}
	s.render(w, "catalog", http.StatusOK, catalogView{base: s.base(w, r, "Товары"), Products: cards(list),
		Currency: currencySign(s.svc.P.Business.Currency)})
}

type variantRow struct {
	ID    int64
	Name  string
	Stock string
}

type productForm struct {
	base
	Product                   *shop.Product // nil for a new one
	Title, Description        string
	Price, OldPrice           string
	Hidden                    bool
	Rows                      []variantRow
	Problems                  []string
	Saved                     bool
	Currency                  string
	MaxImages, ImagesLeft, MB int
}

func (s *Server) productFormView(w http.ResponseWriter, r *http.Request, p *shop.Product) productForm {
	v := productForm{base: s.base(w, r, "Новый товар"), Product: p, Currency: currencySign(s.svc.P.Business.Currency),
		MaxImages: shop.MaxImages, ImagesLeft: shop.MaxImages, MB: shop.MaxUpload >> 20}
	if p != nil {
		v.Title, v.Description, v.Hidden = p.Title, p.Description, p.Hidden
		v.base.Title = p.Title
		v.Price = p.Price.Plain()
		if p.OldPrice > 0 {
			v.OldPrice = p.OldPrice.Plain()
		}
		for _, vr := range p.Variants {
			v.Rows = append(v.Rows, variantRow{ID: vr.ID, Name: vr.Name, Stock: strconv.Itoa(vr.Stock)})
		}
		v.ImagesLeft = shop.MaxImages - len(p.Images)
	}
	return v
}

// withBlankRows gives the form room for new sizes.
func (v *productForm) withBlankRows() {
	if len(v.Rows) == 0 {
		v.Rows = append(v.Rows, variantRow{Stock: "1"})
	}
	for range 3 {
		v.Rows = append(v.Rows, variantRow{})
	}
}

func (s *Server) itemNew(w http.ResponseWriter, r *http.Request) {
	v := s.productFormView(w, r, nil)
	v.withBlankRows()
	s.render(w, "item", http.StatusOK, v)
}

func (s *Server) itemEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.cfg.Shop.Get(r.Context(), id)
	if err != nil {
		s.message(w, r, http.StatusNotFound, "Товар не найден", "Обновите список товаров.")
		return
	}
	v := s.productFormView(w, r, p)
	v.Saved = r.URL.Query().Get("saved") == "1"
	if msg := r.URL.Query().Get("photo"); msg != "" {
		v.Problems = append(v.Problems, msg)
	}
	v.withBlankRows()
	s.render(w, "item", http.StatusOK, v)
}

// itemSave creates or updates a product and adds the uploaded photos.
func (s *Server) itemSave(w http.ResponseWriter, r *http.Request) {
	// a dozen photos over a slow mobile network and their processing take longer than a page
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(10 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(12 * time.Minute))
	if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.message(w, r, http.StatusRequestEntityTooLarge, "Слишком много за раз",
			"Загрузите фото частями, по несколько штук. Не больше 100 МБ за одно сохранение.")
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	if !s.csrfOK(r) {
		s.message(w, r, http.StatusForbidden, "Страница устарела", "Обновите страницу и повторите.")
		return
	}
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	in := shop.Input{Title: r.PostFormValue("title"), Description: r.PostFormValue("description"), Hidden: r.PostFormValue("hidden") == "1"}
	var problems []string
	var err error
	if raw := strings.ReplaceAll(r.PostFormValue("price"), " ", ""); raw == "" && s.isServices() {
		in.Price = 0 // a service by agreement
	} else if in.Price, err = money.Parse(raw); err != nil {
		problems = append(problems, "цена: число, например 4500")
	}
	if old := strings.ReplaceAll(r.PostFormValue("old_price"), " ", ""); old != "" {
		if in.OldPrice, err = money.Parse(old); err != nil {
			problems = append(problems, "старая цена: число или пусто")
		}
	}
	var rows []variantRow
	for i := 0; i < 60; i++ {
		k := strconv.Itoa(i)
		name, stock := r.PostFormValue("v_name_"+k), strings.TrimSpace(r.PostFormValue("v_stock_"+k))
		vid, _ := strconv.ParseInt(r.PostFormValue("v_id_"+k), 10, 64)
		if name == "" && stock == "" && vid == 0 {
			continue
		}
		rows = append(rows, variantRow{ID: vid, Name: name, Stock: stock})
		n, err := strconv.Atoi(stock)
		if stock == "" {
			n, err = 0, nil
		}
		if err != nil {
			problems = append(problems, "остаток: целое число")
			continue
		}
		in.Variants = append(in.Variants, shop.VariantInput{ID: vid, Name: name, Stock: n})
	}
	if len(problems) == 0 {
		id, err = s.cfg.Shop.Save(r.Context(), id, in)
		var bad shop.Problems
		switch {
		case errors.As(err, &bad):
			problems = append(problems, bad...)
		case errors.Is(err, shop.ErrNotFound):
			s.message(w, r, http.StatusNotFound, "Товар не найден", "Его удалили, пока вы редактировали.")
			return
		case err != nil:
			s.log.Error("catalog save", "err", err)
			problems = append(problems, "не получилось сохранить, попробуйте ещё раз")
		}
	}
	if len(problems) > 0 {
		var p *shop.Product
		if id != 0 {
			p, _ = s.cfg.Shop.Get(r.Context(), id)
		}
		v := s.productFormView(w, r, p)
		v.Title, v.Description, v.Price, v.OldPrice, v.Hidden = in.Title, in.Description, r.PostFormValue("price"),
			r.PostFormValue("old_price"), in.Hidden
		v.Rows, v.Problems = rows, problems
		v.withBlankRows()
		s.render(w, "item", http.StatusUnprocessableEntity, v)
		return
	}
	photoMsg := s.savePhotos(r, id)
	target := "/owner/catalog/" + strconv.FormatInt(id, 10) + "?saved=1"
	if photoMsg != "" {
		target += "&photo=" + url.QueryEscape(photoMsg)
	}
	s.redirect(w, r, target, http.StatusSeeOther)
}

// savePhotos processes the uploaded files one by one; a bad file is skipped and named in the answer.
func (s *Server) savePhotos(r *http.Request, product int64) string {
	if r.MultipartForm == nil {
		return ""
	}
	var failed []string
	for _, fh := range r.MultipartForm.File["photos"] {
		if fh.Size == 0 {
			continue
		}
		f, err := fh.Open()
		if err != nil {
			failed = append(failed, fh.Filename)
			continue
		}
		im, err := s.cfg.Photos.Save(f)
		_ = f.Close()
		if err != nil {
			s.log.Info("photo refused", "file", fh.Filename, "err", err)
			failed = append(failed, fh.Filename+" ("+err.Error()+")")
			continue
		}
		if err := s.cfg.Shop.AddImage(r.Context(), product, im); err != nil {
			s.cfg.Photos.Remove(im.File)
			failed = append(failed, fh.Filename+" ("+err.Error()+")")
		}
	}
	if len(failed) == 0 {
		return ""
	}
	return "Не загрузились: " + strings.Join(failed, ", ") + ". Остальное сохранено."
}

// itemAction is the owner's buttons in the list and under the photos.
func (s *Server) itemAction(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	back := "/owner/catalog#p" + strconv.FormatInt(id, 10)
	switch action := r.PathValue("action"); {
	case action == "hide" || action == "show":
		err = s.cfg.Shop.SetHidden(ctx, id, action == "hide")
	case action == "up" || action == "down":
		dir := -1
		if action == "down" {
			dir = 1
		}
		err = s.cfg.Shop.Move(ctx, id, dir)
	case action == "delete":
		if r.PostFormValue("sure") != "1" {
			s.redirect(w, r, "/owner/catalog/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
			return
		}
		var files []string
		if files, err = s.cfg.Shop.Delete(ctx, id); err == nil {
			for _, f := range files {
				s.cfg.Photos.Remove(f)
			}
		}
		back = "/owner/catalog"
	case strings.HasPrefix(action, "img-"):
		back = "/owner/catalog/" + strconv.FormatInt(id, 10) + "#photos"
		op, rest, _ := strings.Cut(strings.TrimPrefix(action, "img-"), "-")
		img, perr := strconv.ParseInt(rest, 10, 64)
		if perr != nil {
			http.NotFound(w, r)
			return
		}
		switch op {
		case "del":
			var file string
			if file, err = s.cfg.Shop.DeleteImage(ctx, id, img); err == nil {
				s.cfg.Photos.Remove(file)
			}
		case "cover":
			err = s.cfg.Shop.CoverImage(ctx, id, img)
		default:
			http.NotFound(w, r)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil && !errors.Is(err, shop.ErrNotFound) {
		s.log.Error("catalog action", "err", err)
		s.message(w, r, http.StatusInternalServerError, "Не получилось", "Обновите страницу и попробуйте ещё раз.")
		return
	}
	s.redirect(w, r, back, http.StatusSeeOther)
}
