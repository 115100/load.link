package handler

import (
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/115100/load.link/internal/config"
	"github.com/115100/load.link/internal/db"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed assets/installer_base.html assets/installer_body.html assets/installer.js
var installerFS embed.FS

var installerTmpl = template.Must(template.New("").ParseFS(installerFS,
	"assets/installer_base.html", "assets/installer_body.html"))

func (h *Handler) handleInstaller(w http.ResponseWriter, r *http.Request) {
	if h.cfg != nil {
		h.handleContent(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.showInstaller(w)
	case http.MethodPost:
		h.processInstaller(w, r)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) showInstaller(w http.ResponseWriter) {
	id := uuid.Must(uuid.NewV7()).String()
	h.installerSessions[id] = true

	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	installerTmpl.ExecuteTemplate(w, "installer_base.html", map[string]any{
		"UUID":       id,
		"Config":     config.Default().GetAll(),
		"UploadDir":  absUploadDir("uploads/"),
		"DBTypes":    dbTypeOptions("sqlite"),
		"RouteModes": routeModeOptions("path"),
		"Error":      "",
		"Success":    false,
	})
}

func (h *Handler) processInstaller(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	sessionID := r.FormValue("uuid")
	if sessionID == "" || !h.installerSessions[sessionID] {
		http.Error(w, "Session expired", http.StatusBadRequest)
		return
	}
	delete(h.installerSessions, sessionID)

	cfg := config.Default()
	if v := r.FormValue("database[type]"); v != "" {
		cfg.Database.Type = v
	}
	if v := r.FormValue("database[name]"); v != "" {
		cfg.Database.Name = v
	}
	if v := r.FormValue("database[host]"); v != "" {
		cfg.Database.Host = v
	}
	if v := r.FormValue("database[port]"); v != "" {
		cfg.Database.Port = v
	}
	if v := r.FormValue("database[username]"); v != "" {
		cfg.Database.Username = v
	}
	if v := r.FormValue("database[password]"); v != "" {
		cfg.Database.Password = v
	}
	if v := r.FormValue("link[upload_dir]"); v != "" {
		cfg.Link.UploadDir = v
	}
	if v := r.FormValue("link[characters]"); v != "" {
		cfg.Link.Characters = v
	}
	if v := r.FormValue("link[length]"); v != "" {
		cfg.Link.Length = atoiOr(v, 8)
	}
	if v := r.FormValue("routing[mode]"); v != "" {
		cfg.Routing.Mode = v
	}
	if v := r.FormValue("routing[baseurl]"); v != "" {
		cfg.Routing.BaseURL = v
	}
	if v := r.FormValue("login[username]"); v != "" {
		cfg.Login.Username = v
	}
	if v := r.FormValue("login[password]"); v != "" {
		cfg.SetPassword(v)
	}

	if err := cfg.Save(h.configPath); err != nil {
		h.renderInstallerError(w, "Failed to write config: "+err.Error())
		return
	}

	database, err := db.New(cfg)
	if err != nil {
		h.renderInstallerError(w, "Failed to connect to database: "+err.Error())
		return
	}
	if err := database.Install(); err != nil {
		database.Close()
		h.renderInstallerError(w, "Failed to initialise database: "+err.Error())
		return
	}

	uploadDir := absUploadDir(cfg.Link.UploadDir)
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		database.Close()
		h.renderInstallerError(w, "Failed to create upload directory: "+err.Error())
		return
	}

	h.cfg = cfg
	h.db = database
	h.uploadDir = uploadDir

	if err := h.initTemplates(); err != nil {
		h.renderInstallerError(w, "Failed to initialise templates: "+err.Error())
		return
	}

	redirectPath := cfg.Routing.BaseURL
	if redirectPath == "" {
		redirectPath = "/"
	}

	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	installerTmpl.ExecuteTemplate(w, "installer_base.html", map[string]any{
		"Success":    true,
		"PanelPath":  redirectPath,
		"Config":     cfg.GetAll(),
		"UploadDir":  uploadDir,
		"Error":      "",
		"DBTypes":    dbTypeOptions(cfg.Database.Type),
		"RouteModes": routeModeOptions(cfg.Routing.Mode),
	})
}

func (h *Handler) renderInstallerError(w http.ResponseWriter, msg string) {
	slog.Error("installer", "error", msg)

	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	installerTmpl.ExecuteTemplate(w, "installer_base.html", map[string]any{
		"Error":      msg,
		"UUID":       uuid.Must(uuid.NewV7()).String(),
		"Config":     config.Default().GetAll(),
		"UploadDir":  absUploadDir("uploads/"),
		"DBTypes":    dbTypeOptions("sqlite"),
		"RouteModes": routeModeOptions("path"),
		"Success":    false,
	})
}

func absUploadDir(dir string) string {
	if dir == "" || dir == "." {
		dir = "uploads"
	}
	if !filepath.IsAbs(dir) {
		if a, err := filepath.Abs(dir); err == nil {
			dir = a
		}
	}
	return dir
}

func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return fallback
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func dbTypeOptions(current string) template.HTML {
	types := []struct{ Val, Label string }{
		{"sqlite", "SQLite"},
		{"pgsql", "PostgreSQL"},
	}
	var b []byte
	for _, t := range types {
		sel := ""
		if t.Val == current {
			sel = " selected"
		}
		b = fmt.Appendf(b, `<option value="%s"%s>%s</option>`, t.Val, sel, t.Label)
	}
	return template.HTML(b)
}

func routeModeOptions(current string) template.HTML {
	modes := []struct{ Val, Label string }{
		{"path", "PATH"},
		{"get", "GET"},
	}
	var b []byte
	for _, m := range modes {
		sel := ""
		if m.Val == current {
			sel = " selected"
		}
		b = fmt.Appendf(b, `<option value="%s"%s>%s</option>`, m.Val, sel, m.Label)
	}
	return template.HTML(b)
}
