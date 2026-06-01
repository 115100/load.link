package handler

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/js"

	"github.com/115100/load.link/internal/config"
	"github.com/115100/load.link/internal/db"
	"github.com/115100/load.link/internal/mimeutil"
)

//go:embed assets/*.html assets/*.js assets/theme.css assets/delfticons/* assets/prismjs/*
var assetsFS embed.FS

var (
	cssMinifier = func() *minify.M {
		m := minify.New()
		m.AddFunc("text/css", css.Minify)
		return m
	}()
	jsMinifier = func() *minify.M {
		m := minify.New()
		m.AddFunc("text/javascript", js.Minify)
		return m
	}()
)

// Handler serves the load.link frontend and API.
type Handler struct {
	cfg               *config.Config
	db                *db.DB
	uploadDir         string
	configPath        string
	templates         map[string]*template.Template
	installerSessions map[string]struct{}
}

func New(cfg *config.Config, database *db.DB, uploadDir, configPath string) (*Handler, error) {
	h := &Handler{
		cfg:               cfg,
		db:                database,
		uploadDir:         uploadDir,
		configPath:        configPath,
		installerSessions: make(map[string]struct{}),
	}
	if err := h.initTemplates(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/theme.css", h.handleThemeCSS)
	mux.HandleFunc("/theme.js", h.handleThemeJS)
	mux.HandleFunc("/favicon.ico", h.handleFavicon)

	if h.cfg == nil {
		mux.HandleFunc("/", h.handleInstaller)
		return
	}
	mux.HandleFunc("/api", h.handleAPI)
	mux.HandleFunc("/login", h.handleLogin)
	mux.HandleFunc("/logout", h.handleLogout)
	mux.HandleFunc("/gallery", h.handleGallery)
	mux.HandleFunc("/static/", h.handleStatic)
	mux.HandleFunc("/", h.handleContent)
}

func (h *Handler) baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}

	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   h.cfg.Routing.BaseURL,
	}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	if h.cfg.Routing.Mode == "get" {
		u.ForceQuery = true
	}
	return u.String()
}

func (h *Handler) buildLink(uid, filename string, r *http.Request) string {
	link := h.baseURL(r) + uid
	if h.cfg.Link.ShowExtension && filename != "" {
		if ext := filepath.Ext(filename); ext != "" {
			link += ext
		}
	}
	return link
}

func (h *Handler) rawPath(uid string) string {
	base := h.cfg.Routing.BaseURL
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + uid + ".raw"
}

func (h *Handler) validSession(token string) bool {
	valid, err := h.db.GetSession(token)
	if err != nil {
		slog.Warn("session lookup failed", "error", err)
		return false
	}
	return valid
}

func (h *Handler) getTokenFromRequest(r *http.Request) string {
	if cookie, err := r.Cookie("token"); err == nil {
		return cookie.Value
	}

	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return after
	}
	return ""
}

func (h *Handler) saveUpload(tmpPath, filename string) (string, error) {
	destPath := filepath.Join(h.uploadDir, filename)

	suffix := h.cfg.Link.SameNameSuffix
	if suffix == "" {
		suffix = ".1"
	}
	for {
		if _, err := os.Stat(destPath); os.IsNotExist(err) {
			break
		}
		destPath += suffix
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		src, err := os.Open(tmpPath)
		if err != nil {
			return "", err
		}
		defer src.Close()

		dst, err := os.Create(destPath)
		if err != nil {
			return "", err
		}
		defer dst.Close()

		if _, err := io.Copy(dst, src); err != nil {
			return "", err
		}
	}
	return destPath, nil
}

func (h *Handler) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Warn("json encode failed", "error", err)
	}
}

func (h *Handler) jsonError(w http.ResponseWriter, status int, message string) {
	h.jsonResponse(w, status, map[string]string{"message": message})
}

func (h *Handler) textResponse(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	w.Write([]byte(text))
}

func (h *Handler) renderError(w http.ResponseWriter, status int, title, name string) {
	w.WriteHeader(status)
	h.renderPage(w, "message", map[string]any{
		"title": title, "name": name, "message": title, "refreshable": false,
	})
}

func (h *Handler) handleContent(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.cfg.Routing.BaseURL)
	path = strings.TrimPrefix(path, "/")

	if h.cfg.Routing.Mode == "get" && path == "" {
		for k := range r.URL.Query() {
			path = k
			break
		}
	}

	if path == "" {
		if hp := h.cfg.Routing.Homepage; hp != "" {
			http.Redirect(w, r, hp, http.StatusMovedPermanently)
			return
		}
		h.handlePanel(w, r)
		return
	}

	if strings.HasPrefix(path, "static/") {
		h.handleStatic(w, r)
		return
	}

	if cp := h.cfg.Routing.Panel; cp != "" && path == cp {
		h.handlePanel(w, r)
		return
	}

	// Resolve the link
	pathInfo := strings.SplitN(path, ".", 2)
	uid := pathInfo[0]
	rawMode := len(pathInfo) > 1 && pathInfo[1] == "raw"

	link, err := h.db.GetLink(uid)
	if err != nil || link == nil {
		h.renderError(w, http.StatusNotFound, "Not Found", "error not_found")
		return
	}

	if link.Mime == "wwwserver/redirection" {
		http.Redirect(w, r, link.Name, http.StatusSeeOther)
		return
	}

	if link.Path != "" {
		if _, err := os.Stat(link.Path); os.IsNotExist(err) {
			h.renderError(w, http.StatusNotFound, "Not Found", "error not_found")
			return
		}
	}

	if rawMode {
		h.serveFile(w, r, link)
		return
	}

	// Optional renderers
	if h.cfg.UI.SyntaxHighlighter {
		ext := filepath.Ext(link.Name)
		if len(ext) > 0 {
			ext = ext[1:]
		}
		if mimeutil.IsCodeExtension(ext) {
			content, err := os.ReadFile(link.Path)
			if err != nil {
				content = []byte{}
			}
			h.renderPage(w, "syntax_highlighter", map[string]any{
				"static_path": h.cfg.Routing.BaseURL + "static/",
				"title":       link.Name,
				"name":        link.Name,
				"text":        string(content),
				"language":    mimeutil.GetLanguageFromExtension(ext),
				"raw_url":     h.rawPath(uid),
			})
			return
		}
	}

	if h.cfg.UI.MediaPlayer && mimeutil.IsAudioVideo(link.Mime) {
		mediaType := strings.SplitN(link.Mime, "/", 2)[0]
		h.renderPage(w, "media_player", map[string]any{
			"title":   link.Name,
			"type":    mediaType,
			"name":    link.Name,
			"mime":    link.Mime,
			"raw_url": h.rawPath(uid),
		})
		return
	}

	h.serveFile(w, r, link)
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, link *db.Link) {
	if link.Path == "" {
		h.renderError(w, http.StatusNotFound, "Not Found", "error not_found")
		return
	}

	file, err := os.Open(link.Path)
	if err != nil {
		h.renderError(w, http.StatusNotFound, "Not Found", "error not_found")
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	modTime := time.Now()
	if err == nil {
		modTime = stat.ModTime()
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", link.Name))
	http.ServeContent(w, r, link.Name, modTime, file)
}

func (h *Handler) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.cfg.Routing.BaseURL+"static/")
	path = strings.TrimPrefix(path, "/static/")
	path = filepath.Clean(path)

	if strings.Contains(path, "..") {
		h.renderError(w, http.StatusNotFound, "Not Found", "error not_found")
		return
	}

	data, err := assetsFS.ReadFile("assets/" + path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, path, time.Now(), bytes.NewReader(data))
}

func (h *Handler) handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("favicon.ico")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "favicon.ico", time.Now(), bytes.NewReader(data))
}

func (h *Handler) handleThemeCSS(w http.ResponseWriter, r *http.Request) {
	css, err := assetsFS.ReadFile("assets/theme.css")
	if err != nil {
		http.Error(w, "CSS not found", http.StatusInternalServerError)
		return
	}
	minified, err := cssMinifier.Bytes("text/css", css)
	if err != nil {
		minified = css
	}

	w.Header().Set("Content-Type", "text/css")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(minified)
}

func (h *Handler) handleThemeJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	token := h.getTokenFromRequest(r)

	baseroute := "/"
	if h.cfg != nil && h.cfg.Routing.BaseURL != "" {
		baseroute = h.cfg.Routing.BaseURL
	}

	autostart := false
	displayThumb := true
	showExt := false
	deletionConfirm := true
	msgTimeout := 10000
	galleryItems := 30
	if h.cfg != nil {
		autostart = h.cfg.UI.AutostartUpload
		displayThumb = h.cfg.UI.DisplayThumbnail
		showExt = h.cfg.Link.ShowExtension
		deletionConfirm = h.cfg.UI.DeletionConfirmation
		if h.cfg.UI.MessageTimeout > 0 {
			msgTimeout = h.cfg.UI.MessageTimeout
		}
		if h.cfg.UI.GalleryItems > 0 {
			galleryItems = h.cfg.UI.GalleryItems
		}
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "var baseroute='%s';\n", baseroute)
	fmt.Fprintf(&buf, "var api='%sapi';\n", baseroute)
	fmt.Fprintf(&buf, "var token='%s';\n", token)
	fmt.Fprintf(&buf, "var default_uploader_text='Choose or drag file here.';\n")
	fmt.Fprintf(&buf, "var autostart_upload=%t;\n", autostart)
	fmt.Fprintf(&buf, "var display_thumbnail=%t;\n", displayThumb)
	fmt.Fprintf(&buf, "var show_extension=%t;\n", showExt)
	fmt.Fprintf(&buf, "var deletion_confirmation=%t;\n", deletionConfirm)
	fmt.Fprintf(&buf, "var message_timeout=%d;\n", msgTimeout)
	fmt.Fprintf(&buf, "var gallery_limit=%d;\n", galleryItems)

	for _, f := range []string{"_global", "message_popup", "forms", "hideables", "refresh", "bytes_to_hr", "delfticons", "panel", "gallery"} {
		data, err := assetsFS.ReadFile("assets/" + f + ".js")
		if err == nil {
			buf.Write(data)
			buf.WriteByte('\n')
		}
	}

	minified, err := jsMinifier.Bytes("text/javascript", buf.Bytes())
	if err != nil {
		w.Write(buf.Bytes())
		return
	}
	w.Write(minified)
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.renderPage(w, "login", map[string]any{
			"login_path": h.cfg.Routing.BaseURL + "login",
			"config":     h.cfg.GetAll(),
		})
		return
	}

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		username := r.FormValue("login[username]")
		password := r.FormValue("login[password]")

		if username == h.cfg.Login.Username && h.cfg.CheckPassword(password) {
			token, err := h.db.AddSession()
			if err != nil {
				slog.Error("session creation failed", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: "token", Value: token, Path: "/",
				MaxAge: 31536000, HttpOnly: true,
			})
			h.renderPage(w, "message", map[string]any{
				"title":       "Logged in. Redirecting...",
				"message":     "Logged in. Redirecting...",
				"name":        "notice",
				"refreshable": true,
				"redirect":    map[string]any{"wait": 3, "url": "/"},
			})
			return
		}

		w.WriteHeader(http.StatusForbidden)
		h.renderPage(w, "message", map[string]any{
			"title": "Access Denied", "message": "Access Denied",
			"name": "error forbidden", "refreshable": false,
		})
	}
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	purge := r.FormValue("purge") == "on"

	if cookie, err := r.Cookie("token"); err == nil {
		if purge {
			h.db.DelAllSessions()
		} else {
			h.db.DelSession(cookie.Value)
		}
	}

	http.SetCookie(w, &http.Cookie{Name: "token", Value: "", Path: "/", MaxAge: -1})
	h.renderPage(w, "message", map[string]any{
		"title":       "Logged out. Redirecting...",
		"message":     "Logged out. Redirecting...",
		"name":        "notice",
		"refreshable": true,
		"redirect":    map[string]any{"wait": 3, "url": "/"},
	})
}

func (h *Handler) handlePanel(w http.ResponseWriter, r *http.Request) {
	token := h.getTokenFromRequest(r)
	if token == "" || !h.validSession(token) {
		h.renderPage(w, "login", map[string]any{
			"login_path": h.cfg.Routing.BaseURL + "login",
			"config":     h.cfg.GetAll(),
		})
		return
	}

	baseroute := h.cfg.Routing.BaseURL
	if baseroute == "" {
		baseroute = "/"
	}
	if h.cfg.Routing.Mode == "get" {
		baseroute += "?"
	}

	languages := map[string]map[string]string{
		"none":         {"name": "None", "ext": "txt"},
		"bash":         {"name": "Bash", "ext": "sh"},
		"c":            {"name": "C", "ext": "c"},
		"cpp":          {"name": "C++", "ext": "cpp"},
		"csharp":       {"name": "C#", "ext": "cs"},
		"coffeescript": {"name": "CoffeeScript", "ext": "coffee"},
		"css":          {"name": "CSS", "ext": "css"},
		"go":           {"name": "Go", "ext": "go"},
		"haskell":      {"name": "Haskell", "ext": "hs"},
		"ini":          {"name": "INI", "ext": "ini"},
		"java":         {"name": "Java", "ext": "java"},
		"javascript":   {"name": "JavaScript", "ext": "js"},
		"latex":        {"name": "LaTeX", "ext": "tex"},
		"markup":       {"name": "Markup", "ext": "xml"},
		"objectivec":   {"name": "Objective-C", "ext": "m"},
		"php":          {"name": "PHP", "ext": "php"},
		"python":       {"name": "Python", "ext": "py"},
		"ruby":         {"name": "Ruby", "ext": "rb"},
		"scss":         {"name": "SCSS", "ext": "scss"},
		"sql":          {"name": "SQL", "ext": "sql"},
		"swift":        {"name": "Swift", "ext": "swift"},
		"twig":         {"name": "Twig", "ext": "twig"},
	}

	h.renderPage(w, "panel", map[string]any{
		"baseroute":             baseroute,
		"api":                   baseroute + "api",
		"token":                 token,
		"languages":             languages,
		"config":                h.cfg.GetAll(),
		"gallery_path":          baseroute + "gallery",
		"logout_path":           baseroute + "logout",
		"default_uploader_text": "Choose or drag file here.",
	})
}

func (h *Handler) handleGallery(w http.ResponseWriter, r *http.Request) {
	token := h.getTokenFromRequest(r)
	if token == "" || !h.validSession(token) {
		http.Redirect(w, r, h.cfg.Routing.BaseURL, http.StatusSeeOther)
		return
	}

	baseroute := h.cfg.Routing.BaseURL
	if baseroute == "" {
		baseroute = "/"
	}
	if h.cfg.Routing.Mode == "get" {
		baseroute += "?"
	}

	h.renderPage(w, "gallery", map[string]any{
		"baseroute": baseroute,
		"api":       baseroute + "api",
		"token":     token,
		"config":    h.cfg.GetAll(),
	})
}

func (h *Handler) initTemplates() error {
	h.templates = make(map[string]*template.Template)

	funcMap := template.FuncMap{
		"cssSafe": func(s string) template.CSS { return template.CSS(s) },
		"jsSafe":  func(s string) template.JS { return template.JS(s) },
		"jsStr":   func(s string) template.JS { return template.JS("'" + strings.ReplaceAll(s, "'", "\\'") + "'") },
		"jsNum":   func(s string) template.JS { return template.JS(s) },
	}

	baseHTML, err := assetsFS.ReadFile("assets/base.html")
	if err != nil {
		return fmt.Errorf("failed to read base template: %w", err)
	}

	dir, err := assetsFS.ReadDir("assets")
	if err != nil {
		return fmt.Errorf("failed to read template dir: %w", err)
	}

	for _, entry := range dir {
		name := entry.Name()
		if !strings.HasSuffix(name, ".html") || name == "theme.css" || strings.HasPrefix(name, "installer") {
			continue
		}
		pageName := strings.TrimSuffix(name, ".html")

		bodyHTML, err := assetsFS.ReadFile("assets/" + name)
		if err != nil {
			continue
		}

		full := strings.Replace(string(baseHTML), "{{.body}}", string(bodyHTML), 1)
		tmpl, err := template.New(pageName).Funcs(funcMap).Parse(full)
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", pageName, err)
		}
		h.templates[pageName] = tmpl
	}
	return nil
}

func (h *Handler) renderPage(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")

	if redirect, ok := data["redirect"].(map[string]any); ok {
		wait := fmt.Sprint(redirect["wait"])
		if u, ok := redirect["url"].(string); ok && u != "" {
			data["redirectMeta"] = template.HTML(
				fmt.Sprintf("<meta http-equiv=\"refresh\" content=\"%s;url=%s\">", wait, u),
			)
		} else {
			data["redirectMeta"] = template.HTML(
				fmt.Sprintf("<meta http-equiv=\"refresh\" content=\"%s\">", wait),
			)
		}
	} else {
		data["redirectMeta"] = template.HTML("")
	}

	baseurl := "/"
	if h.cfg != nil {
		if b := h.cfg.Routing.BaseURL; b != "" {
			baseurl = b
		}
	}
	data["baseurl"] = baseurl

	tmpl := h.templates[name]
	if tmpl == nil {
		tmpl = h.templates["message"]
	}
	if tmpl == nil {
		fmt.Fprintf(w, "Error: template not found")
		return
	}

	if err := tmpl.Execute(w, data); err != nil {
		slog.Error("template render", "error", err)
	}
}
