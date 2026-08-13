package handler

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/115100/load.link/internal/config"
	"github.com/115100/load.link/internal/db"
	"github.com/115100/load.link/internal/mimeutil"
)

func (h *Handler) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.jsonError(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}

	if h.tryAlternativeUpload(w, r) {
		return
	}

	headersFile, _, err := r.FormFile("headers")
	if err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	headersData, err := io.ReadAll(headersFile)
	headersFile.Close()
	if err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}

	var action struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(headersData, &action); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}

	switch action.Action {
	case "get_token":
		h.apiGetToken(w, headersData)
	case "get_links":
		h.apiGetLinks(w, r, headersData)
	case "count":
		h.apiCount(w, r, headersData)
	case "get_thumbnail":
		h.apiGetThumbnail(w, r, headersData)
	case "upload":
		h.apiUpload(w, r, headersData)
	case "shorten_url":
		h.apiShortenURL(w, r, headersData)
	case "delete":
		h.apiDelete(w, r, headersData)
	case "edit_settings":
		h.apiEditSettings(w, r, headersData)
	case "release_token":
		h.apiReleaseToken(w, r, headersData)
	case "release_all_tokens":
		h.apiReleaseAllTokens(w, r, headersData)
	case "prune_unused":
		h.apiPruneUnused(w, r, headersData)
	default:
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
	}
}

func (h *Handler) tryAlternativeUpload(w http.ResponseWriter, r *http.Request) bool {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return false
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" || !h.validSession(token) {
		h.textResponse(w, http.StatusForbidden, "Access Denied.")
		return true
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return false
	}
	defer file.Close()

	tmpFile, err := os.CreateTemp("", "loadlink-upload-*")
	if err != nil {
		h.textResponse(w, http.StatusAccepted, "Upload Failed.")
		return true
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, file); err != nil {
		tmpFile.Close()
		h.textResponse(w, http.StatusAccepted, "Upload Failed.")
		return true
	}
	tmpFile.Close()

	data, _ := os.ReadFile(tmpFile.Name())
	if len(data) > 512 {
		data = data[:512]
	}
	mime := mimeutil.DetectMime(data, filepath.Ext(header.Filename))

	uploadPath, err := h.saveUpload(tmpFile.Name(), header.Filename)
	if err != nil {
		h.textResponse(w, http.StatusAccepted, "Upload Failed.")
		return true
	}

	uid, err := h.db.AddLink(uploadPath, header.Filename, mime)
	if err != nil {
		h.textResponse(w, http.StatusAccepted, "Upload Failed.")
		return true
	}

	if mimeutil.IsImage(mime) {
		if data, err := os.ReadFile(uploadPath); err == nil {
			if thumb := mimeutil.GenerateThumbnail(data, mime); thumb != nil {
				h.db.StoreThumbnail(uid, &db.Thumbnail{
					Data: thumb.Data, Width: thumb.Width,
					Height: thumb.Height, Mime: thumb.Mime,
				})
			}
		}
	}
	h.textResponse(w, http.StatusCreated, h.buildLink(uid, header.Filename, r))
	return true
}

func (h *Handler) auth(w http.ResponseWriter, r *http.Request, token string) bool {
	if token == "" {
		if cookie, err := r.Cookie("token"); err == nil {
			token = cookie.Value
		}
	}
	if token == "" {
		h.jsonResponse(w, http.StatusForbidden, map[string]string{"message": "Access Denied."})
		return false
	}
	valid, err := h.db.GetSession(token)
	if err != nil {
		slog.Warn("session lookup failed", "error", err)
		h.jsonResponse(w, http.StatusForbidden, map[string]string{"message": "Access Denied."})
		return false
	}
	if !valid {
		h.jsonResponse(w, http.StatusForbidden, map[string]string{"message": "Access Denied."})
		return false
	}
	return true
}

type apiGetTokenReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handler) apiGetToken(w http.ResponseWriter, headersData []byte) {
	var req apiGetTokenReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}

	if req.Username != h.cfg.Login.Username || !h.cfg.CheckPassword(req.Password) {
		h.jsonResponse(w, http.StatusForbidden, map[string]string{"message": "Access Denied."})
		return
	}

	token, err := h.db.AddSession()
	if err != nil {
		h.jsonError(w, http.StatusInternalServerError, "Token generation failed.")
		return
	}

	h.jsonResponse(w, http.StatusOK, map[string]string{"message": "OK.", "token": token})
}

type apiGetLinksReq struct {
	Token  string `json:"token"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	Search string `json:"search"`
}

func (h *Handler) apiGetLinks(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiGetLinksReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}

	links, err := h.db.GetLinks(req.Limit, req.Offset, req.Search)
	if err != nil {
		h.jsonError(w, http.StatusInternalServerError, "Failed to get links.")
		return
	}
	if links == nil {
		links = []db.Link{}
	}

	h.jsonResponse(w, http.StatusOK, map[string]any{"message": "OK.", "links": links})
}

type apiTokenReq struct {
	Token string `json:"token"`
}

type apiCountReq struct {
	Token  string `json:"token"`
	Search string `json:"search"`
}

func (h *Handler) apiCount(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiCountReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}

	count, err := h.db.CountLinks(req.Search)
	if err != nil {
		h.jsonError(w, http.StatusInternalServerError, "Failed to count links.")
		return
	}

	h.jsonResponse(w, http.StatusOK, map[string]any{"message": "OK.", "count": count})
}

func (h *Handler) apiReleaseToken(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiTokenReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}

	h.db.DelSession(req.Token)
	h.jsonResponse(w, http.StatusOK, map[string]string{"message": "OK."})
}

func (h *Handler) apiReleaseAllTokens(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiTokenReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}

	h.db.DelAllSessions()
	h.jsonResponse(w, http.StatusOK, map[string]string{"message": "OK."})
}

func (h *Handler) apiPruneUnused(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiTokenReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}

	links, err := h.db.GetLinksAll()
	if err != nil {
		h.jsonError(w, http.StatusInternalServerError, "Failed to get links.")
		return
	}

	pruned := 0
	for _, link := range links {
		if link.Mime == "wwwserver/redirection" {
			continue
		}
		if link.Path == "" {
			continue
		}
		if _, err := os.Stat(link.Path); os.IsNotExist(err) {
			h.db.DelLink(link.UID)
			pruned++
		}
	}

	h.jsonResponse(w, http.StatusOK, map[string]any{"message": "OK.", "pruned": pruned})
}

type apiThumbnailReq struct {
	Token string `json:"token"`
	UID   string `json:"uid"`
}

func (h *Handler) apiGetThumbnail(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiThumbnailReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}

	thumb, err := h.db.GetThumbnail(req.UID)
	if err != nil || thumb == nil {
		h.jsonResponse(w, http.StatusAccepted, map[string]string{"message": "Could not get thumbnail."})
		return
	}

	h.jsonResponse(w, http.StatusOK, map[string]any{
		"message": "OK.",
		"data":    base64.StdEncoding.EncodeToString(thumb.Data),
		"width":   thumb.Width,
		"height":  thumb.Height,
		"mime":    thumb.Mime,
	})
}

type apiUploadReq struct {
	Token    string `json:"token"`
	Filename string `json:"filename"`
}

func (h *Handler) apiUpload(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiUploadReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}

	filename := req.Filename
	if filename == "" {
		filename = "unnamed"
	}

	dataFile, _, err := r.FormFile("data")
	if err != nil {
		h.jsonResponse(w, http.StatusAccepted, map[string]string{"message": "Upload Failed."})
		return
	}
	defer dataFile.Close()

	tmpFile, err := os.CreateTemp("", "loadlink-upload-*")
	if err != nil {
		h.jsonResponse(w, http.StatusAccepted, map[string]string{"message": "Upload Failed."})
		return
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, dataFile); err != nil {
		tmpFile.Close()
		h.jsonResponse(w, http.StatusAccepted, map[string]string{"message": "Upload Failed."})
		return
	}
	tmpFile.Close()

	uploadPath, err := h.saveUpload(tmpFile.Name(), filename)
	if err != nil {
		h.jsonResponse(w, http.StatusAccepted, map[string]string{"message": "Upload Failed."})
		return
	}

	data, err := os.ReadFile(uploadPath)
	if err != nil {
		data = []byte{}
	}

	ext := filepath.Ext(filename)
	if len(ext) > 0 {
		ext = ext[1:]
	}

	mime := mimeutil.DetectMime(data, ext)

	uid, err := h.db.AddLink(uploadPath, filename, mime)
	if err != nil {
		h.jsonResponse(w, http.StatusAccepted, map[string]string{"message": "Upload Failed."})
		return
	}

	if mimeutil.IsImage(mime) {
		if thumb := mimeutil.GenerateThumbnail(data, mime); thumb != nil {
			h.db.StoreThumbnail(uid, &db.Thumbnail{
				Data: thumb.Data, Width: thumb.Width,
				Height: thumb.Height, Mime: thumb.Mime,
			})
		}
	}

	h.jsonResponse(w, http.StatusCreated, map[string]any{
		"message": "OK.",
		"uid":     uid,
		"name":    filename,
		"mime":    mime,
		"ext":     ext,
		"link":    h.buildLink(uid, filename, r),
	})
}

type apiShortenReq struct {
	Token string `json:"token"`
	URL   string `json:"url"`
}

func (h *Handler) apiShortenURL(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiShortenReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}

	if req.URL == "" {
		h.jsonResponse(w, http.StatusAccepted, map[string]string{"message": "Shortening Failed."})
		return
	}

	uid, err := h.db.AddLink("", req.URL, "wwwserver/redirection")
	if err != nil {
		h.jsonResponse(w, http.StatusAccepted, map[string]string{"message": "Shortening Failed."})
		return
	}

	h.jsonResponse(w, http.StatusCreated, map[string]any{
		"message": "OK.", "uid": uid,
		"link": h.buildLink(uid, "", r),
	})
}

type apiDeleteReq struct {
	Token string `json:"token"`
	UID   string `json:"uid"`
}

func (h *Handler) apiDelete(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiDeleteReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}

	link, err := h.db.GetLink(req.UID)
	if err == nil && link != nil && link.Path != "" {
		if err := os.Remove(link.Path); err != nil {
			slog.Error("api delete", "error", err)
		}
	}

	h.db.DelLink(req.UID)
	h.jsonResponse(w, http.StatusOK, map[string]string{"message": "OK."})
}

type apiEditSettingsReq struct {
	Token    string                  `json:"token"`
	Password string                  `json:"password"`
	Settings *config.SettingsPayload `json:"settings"`
}

func (h *Handler) apiEditSettings(w http.ResponseWriter, r *http.Request, headersData []byte) {
	var req apiEditSettingsReq
	if err := json.Unmarshal(headersData, &req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "Badly Formatted Request.")
		return
	}
	if !h.auth(w, r, req.Token) {
		return
	}
	if !h.cfg.CheckPassword(req.Password) {
		h.jsonResponse(w, http.StatusForbidden, map[string]string{"message": "Could not update settings: wrong password."})
		return
	}

	if req.Settings != nil && req.Settings.Login != nil {
		if pw := req.Settings.Login.Password; pw != nil && *pw != "" {
			h.cfg.SetPassword(*pw)
			req.Settings.Login.Password = nil
		}
	}

	h.cfg.ApplySettings(req.Settings)
	if err := h.cfg.Save(h.configPath); err != nil {
		slog.Error("api edit_settings", "error", err)
	}
	h.jsonResponse(w, http.StatusOK, map[string]string{"message": "OK."})
}
