package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"
	"github.com/mrdoob/glsl-sandbox/server/store"
)

const (
	pathGallery = "./server/assets/gallery.html"
	pathThumbs  = "./data/thumbs"
	perPage     = 50
)

var (
	ErrInvalidData = fmt.Errorf("invalid data")
)

type Template struct {
	templates *template.Template
}

func (t *Template) Render(
	w io.Writer, name string, data interface{}, c echo.Context,
) error {
	tpl := template.New("")
	tpl = tpl.Funcs(template.FuncMap{
		"checkboxID": func(id int) string {
			return fmt.Sprintf("hidden_%d", id)
		},
		"checked": func(b bool) string {
			if b {
				return "checked"
			}
			return ""
		},
	})
	tpl, err := tpl.ParseFiles(pathGallery)
	if err != nil {
		fmt.Println("template error", err.Error())
		return err
	}
	return tpl.ExecuteTemplate(w, name, data)
	// return t.templates.ExecuteTemplate(w, name, data)
}

type Server struct {
	echo     *echo.Echo
	template *Template
	effects  *store.Effects
	dataPath string
}

func New(e *store.Effects, dataPath string) *Server {
	t := template.New("")
	t = t.Funcs(template.FuncMap{
		"checkboxID": func(id int) string {
			return fmt.Sprintf("hidden_%d", id)
		},
		"checked": func(b bool) string {
			if b {
				return "checked"
			}
			return ""
		},
	})
	t = template.Must(t.ParseFiles(pathGallery))

	return &Server{
		echo: echo.New(),
		template: &Template{
			templates: t,
		},
		effects:  e,
		dataPath: dataPath,
	}
}

func (s *Server) Start() error {
	s.setup()
	return s.echo.Start(":8888")
}

func (s *Server) setup() {
	s.echo.Renderer = s.template
	s.echo.Logger.SetLevel(log.DEBUG)
	s.echo.Use(middleware.Logger())
	s.routes()
}

func (s *Server) routes() {
	s.echo.GET("/", s.indexHandler)
	s.echo.GET("/e", s.effectHandler)
	s.echo.POST("/e", s.saveHandler)
	s.echo.GET("/item/:id", s.itemHandler)
	s.echo.GET("/admin", s.adminHandler)
	s.echo.POST("/admin", s.adminPostHandler)

	s.echo.Static("/thumbs", filepath.Join(s.dataPath, "thumbs"))
	s.echo.Static("/css", "./server/assets/css")
	s.echo.Static("/js", "./server/assets/js")
	s.echo.File("/diff", "./server/assets/diff.html")
}

func (s *Server) indexHandler(c echo.Context) error {
	return s.indexRender(c, false)
}

func (s *Server) adminHandler(c echo.Context) error {
	return s.indexRender(c, true)
}

// galleryEffect has information about each effect displayed in the gallery.
type galleryEffect struct {
	// ID is the effect identifyier.
	ID int
	// Version is the latest effect version.
	Version int
	// Image holds the thumbnail name.
	Image string
	// Hidden tells if the effect has been moderated.
	Hidden bool
}

// galleryData has information about the current gallery page.
type galleryData struct {
	// Effects is an array with all the effects for the page.
	Effects []galleryEffect
	// URL is the path of the gallery. Can be "/" or "/admin".
	URL string
	// Page holds the current page number.
	Page int
	// IsPrevious is true if there is a previous page.
	IsPrevious bool
	// PreviousPage is the previous page number.
	PreviousPage int
	// IsNext is true if there is a next page.
	IsNext bool
	// NextPage is the next page number.
	NextPage int
	// Admin is true when accessing "/admin" path.
	Admin bool
}

func (s *Server) indexRender(c echo.Context, admin bool) error {
	pString := c.QueryParam("page")
	if pString == "" {
		pString = "0"
	}
	page, err := strconv.Atoi(pString)
	if err != nil {
		page = 0
	}

	p, err := s.effects.Page(page, perPage, admin)
	if err != nil {
		return c.String(http.StatusInternalServerError, "error")
	}

	effects := make([]galleryEffect, len(p))
	for i, e := range p {
		effects[i] = galleryEffect{
			ID:      e.ID,
			Version: len(e.Versions) - 1,
			Image:   path.Join("/thumbs", e.ImageName()),
			Hidden:  e.Hidden,
		}
	}

	url := "/"
	if admin {
		url = "/admin"
	}
	d := galleryData{
		Effects:      effects,
		URL:          url,
		Page:         page,
		IsNext:       len(effects) == perPage,
		NextPage:     page + 1,
		IsPrevious:   page > 0,
		PreviousPage: page - 1,
		Admin:        admin,
	}

	err = c.Render(http.StatusOK, "gallery", d)
	if err != nil {
		panic(err)
	}
	return err
}

func (s *Server) effectHandler(c echo.Context) error {
	return c.File("./static/index.html")
}

type itemResponse struct {
	Code   string `json:"code"`
	User   string `json:"user"`
	Parent string `json:"parent,omitempty"`
}

func (s *Server) itemHandler(c echo.Context) error {
	param := c.Param("id")
	id, version, err := idVersion(param)
	if err != nil {
		return c.String(http.StatusBadRequest, "{}")
	}

	effect, err := s.effects.Effect(id)
	if err != nil {
		return c.String(http.StatusBadRequest, "{}")
	}

	if version >= len(effect.Versions) || version < 0 {
		return c.String(http.StatusNotFound, "{}")
	}

	parent := ""
	if effect.Parent > 0 {
		parent = fmt.Sprintf("/e#%d.%d", effect.Parent, effect.ParentVersion)
	}

	item := itemResponse{
		Code:   effect.Versions[version].Code,
		User:   effect.User,
		Parent: parent,
	}

	data, err := json.Marshal(item)
	if err != nil {
		return c.String(http.StatusInternalServerError, "{}")
	}

	return c.Blob(http.StatusOK, "application/json", data)
}

type saveQuery struct {
	Code   string `json:"code"`
	Image  string `json:"image"`
	User   string `json:"user"`
	CodeID string `json:"code_id"`
	Parent string `json:"parent"`
}

func (s *Server) saveHandler(c echo.Context) error {
	if c.Request().Body == nil {
		c.Logger().Errorf("empty body")
		return c.String(http.StatusBadRequest, "")
	}

	data, err := io.ReadAll(c.Request().Body)
	if err != nil {
		c.Logger().Errorf("could not read body: %s", err.Error())
		return c.String(http.StatusInternalServerError, "")
	}

	var save saveQuery
	err = json.Unmarshal(data, &save)
	if err != nil {
		c.Logger().Errorf("could not parse json: %s", err.Error())
		return c.String(http.StatusBadRequest, "")
	}

	parts := strings.Split(save.Image, ",")
	if len(parts) != 2 {
		c.Logger().Errorf("malformed encoded image")
		return c.String(http.StatusBadRequest, "")
	}
	imgData := parts[1]

	img, err := base64.StdEncoding.DecodeString(imgData)
	if err != nil {
		c.Logger().Errorf("could not decode image: %s", err.Error())
		return c.String(http.StatusBadRequest, "")
	}

	var id, version int
	if save.CodeID == "" {
		parent, parentVersion, err := idVersion(save.Parent)
		if err != nil {
			parent, parentVersion = -1, -1
		}

		id, err = s.effects.Add(parent, parentVersion, save.User, save.Code)
		if err != nil {
			c.Logger().Errorf("could not save new effect: %s", err.Error())
			return c.String(http.StatusInternalServerError, "")
		}
	} else {
		parts := strings.Split(save.CodeID, ".")
		if len(parts) < 1 {
			c.Logger().Errorf("malformed code id: %s", err.Error())
			return c.String(http.StatusBadRequest, "")
		}

		id, err = strconv.Atoi(parts[0])
		if err != nil {
			c.Logger().Errorf("malformed code id: %s", err.Error())
			return c.String(http.StatusBadRequest, "")
		}

		version, err = s.effects.AddVersion(id, save.Code)
		if err != nil {
			c.Logger().Errorf("could not save new version: %s", err.Error())
			return c.String(http.StatusInternalServerError, "")
		}
	}

	err = saveImage(thumbPath(s.dataPath, id), img)
	if err != nil {
		c.Logger().Errorf("could not save image: %s", err.Error())
		return c.String(http.StatusInternalServerError, "")
	}

	answer := fmt.Sprintf("%d.%d", id, version)
	return c.String(http.StatusOK, answer)
}

func (s *Server) adminPostHandler(c echo.Context) error {
	pageTxt := c.FormValue("page")
	// TODO(jfontan): check error?
	page, _ := strconv.Atoi(pageTxt)
	url := fmt.Sprintf("/admin?page=%d", page)

	values, err := c.FormParams()
	if err != nil {
		c.Logger().Errorf("malformed form: %s", err.Error())
		return c.Redirect(http.StatusSeeOther, url)

	}

	for n, v := range values {
		if !strings.HasPrefix(n, "hidden_") {
			continue
		}
		if len(v) != 1 || v[0] != "on" {
			continue
		}

		parts := strings.Split(n, "_")
		if len(parts) != 2 {
			continue
		}

		id, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}

		err = s.effects.Hide(id, true)
		if err != nil {
			c.Logger().Errorf("could not hide effect: %s", err.Error())
		}
	}

	return c.Redirect(http.StatusSeeOther, url)
}

func thumbPath(dataPath string, id int) string {
	return filepath.Join(dataPath, "thumbs", fmt.Sprintf("%d.png", id))
}

func saveImage(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cannot create thumbnail: %w", err)
	}
	defer f.Close()

	_, err = f.Write(data)
	if err != nil {
		return fmt.Errorf("cannot write thumbnail: %w", err)
	}

	return nil
}

func idVersion(param string) (int, int, error) {
	var idString, versionString string
	parts := strings.Split(strings.TrimPrefix(param, "#"), ".")
	switch len(parts) {
	case 1:
		idString = parts[0]
	case 2:
		idString = parts[0]
		versionString = parts[1]
	default:
		return 0, 0, ErrInvalidData
	}

	id, err := strconv.Atoi(idString)
	if err != nil {
		return 0, 0, ErrInvalidData
	}

	version, err := strconv.Atoi(versionString)
	if err != nil {
		return 0, 0, ErrInvalidData
	}

	return id, version, nil
}
