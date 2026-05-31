package handler_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/115100/load.link/internal/config"
	"github.com/115100/load.link/internal/db"
	"github.com/115100/load.link/internal/handler"
)

type apiTokenResponse struct {
	Message string `json:"message"`
	Token   string `json:"token"`
}

type apiUploadResponse struct {
	Message string `json:"message"`
	UID     string `json:"uid"`
	Name    string `json:"name"`
	Mime    string `json:"mime"`
	Ext     string `json:"ext"`
	Link    string `json:"link"`
}

type apiShortenResponse struct {
	Message string `json:"message"`
	UID     string `json:"uid"`
	Link    string `json:"link"`
}

type apiCountResponse struct {
	Message string `json:"message"`
	Count   int    `json:"count"`
}

type apiLinksResponse struct {
	Message string    `json:"message"`
	Links   []db.Link `json:"links"`
}

type apiThumbnailResponse struct {
	Message string `json:"message"`
	Data    string `json:"data"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Mime    string `json:"mime"`
}

type apiPruneResponse struct {
	Message string `json:"message"`
	Pruned  int    `json:"pruned"`
}

// tinyPNG is a valid 1×1 PNG image used by thumbnail tests.
var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
	0x10, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x62, 0xfa, 0xff, 0xff, 0x3f,
	0x20, 0x00, 0x00, 0xff, 0xff, 0x06, 0x06, 0x03, 0x00, 0xb7, 0x66, 0x11,
	0x21, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60,
	0x82,
}

type testServer struct {
	*httptest.Server
	Config    *config.Config
	DB        *db.DB
	UploadDir string
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	uploadDir := t.TempDir()

	cfg := config.Default()
	// Override only the values needed for testing; everything else stays at
	// its Default() value so there are no dead config lines.
	cfg.Database.Name = "file:test-" + t.Name() + "?mode=memory&cache=shared"
	cfg.Link.UploadDir = uploadDir + "/"
	cfg.Link.Length = 4
	cfg.Login.Username = "testuser"
	cfg.Routing.BaseURL = "/"
	cfg.SetPassword("testpass")

	database, err := db.New(cfg)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	if err := database.Install(); err != nil {
		t.Fatalf("failed to install database: %v", err)
	}

	h, err := handler.New(cfg, database, uploadDir, "")
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &testServer{
		Server:    srv,
		Config:    cfg,
		DB:        database,
		UploadDir: uploadDir,
	}
}

// apiPost sends a multipart/form-data API request with a JSON headers part.
func (ts *testServer) apiPost(headers map[string]any, fileData map[string]io.Reader) (*http.Response, error) {
	return ts.apiPostWithAuth("", headers, fileData)
}

// apiPostWithAuth sends a multipart API request.  When token is non-empty it
// adds an Authorization: Bearer header (alternative upload path).  When
// headers is non-nil the JSON-serialised map is sent as the "headers" form
// file (the normal API path).  fileData maps form-field names to readers.
func (ts *testServer) apiPostWithAuth(token string, headers map[string]any, fileData map[string]io.Reader) (*http.Response, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// JSON headers part (normal API contract)
	if headers != nil {
		hdrJSON, err := json.Marshal(headers)
		if err != nil {
			return nil, err
		}
		fw, err := w.CreateFormFile("headers", "headers")
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(hdrJSON); err != nil {
			return nil, err
		}
	}

	for name, reader := range fileData {
		fw, err := w.CreateFormFile(name, name)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(fw, reader); err != nil {
			return nil, err
		}
	}

	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", ts.URL+"/api", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return http.DefaultClient.Do(req)
}

// getToken obtains a valid session token for the test user.
func (ts *testServer) getToken(t *testing.T) string {
	t.Helper()
	resp, err := ts.apiPost(map[string]any{
		"action":   "get_token",
		"username": "testuser",
		"password": "testpass",
	}, nil)
	if err != nil {
		t.Fatalf("get_token request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("get_token returned %d: %s", resp.StatusCode, body)
	}

	var result apiTokenResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Token
}

// upload is a convenience helper that uploads a named text file and returns
// the API response.
func (ts *testServer) upload(t *testing.T, token, filename, content string) apiUploadResponse {
	t.Helper()
	resp, err := ts.apiPost(map[string]any{
		"action":   "upload",
		"token":    token,
		"filename": filename,
	}, map[string]io.Reader{
		"data": strings.NewReader(content),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	var result apiUploadResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}

func TestAPIGetToken_Success(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.apiPost(map[string]any{
		"action":   "get_token",
		"username": "testuser",
		"password": "testpass",
	}, nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result apiTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if result.Token == "" {
		t.Error("expected non-empty token")
	}
}

func TestAPIGetToken_BadCredentials(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.apiPost(map[string]any{
		"action":   "get_token",
		"username": "testuser",
		"password": "wrongpass",
	}, nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestAPIGetLinks_Unauthorized(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.apiPost(map[string]any{
		"action": "get_links",
		"token":  "invalid-token",
	}, nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestAPIGetLinks_Success(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	// Upload one file so there is at least one link.
	ts.upload(t, token, "test.txt", "hello world")

	resp, err := ts.apiPost(map[string]any{
		"action": "get_links",
		"token":  token,
		"limit":  10,
		"offset": 0,
	}, nil)
	if err != nil {
		t.Fatalf("get_links failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result apiLinksResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(result.Links) != 1 {
		t.Errorf("expected 1 link, got %d", len(result.Links))
	}
	if result.Links[0].Name != "test.txt" {
		t.Errorf("expected name 'test.txt', got %q", result.Links[0].Name)
	}
}

func TestAPICount(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	ts.upload(t, token, "a.txt", "content")
	ts.upload(t, token, "b.txt", "content")

	resp, err := ts.apiPost(map[string]any{
		"action": "count",
		"token":  token,
	}, nil)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result apiCountResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Count != 2 {
		t.Errorf("expected count 2, got %d", result.Count)
	}
}

func TestAPIUpload_Success(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	result := ts.upload(t, token, "hello.txt", "Hello, load.link!")

	if result.UID == "" {
		t.Error("expected non-empty uid")
	}
	if result.Name != "hello.txt" {
		t.Errorf("expected name 'hello.txt', got %q", result.Name)
	}
	if result.Ext != "txt" {
		t.Errorf("expected ext 'txt', got %q", result.Ext)
	}
	if result.Mime != "text/plain" {
		t.Errorf("expected mime 'text/plain', got %q", result.Mime)
	}
	if result.Link == "" {
		t.Error("expected non-empty link")
	}

	// Verify the file was actually written to disk.
	files, _ := os.ReadDir(ts.UploadDir)
	if len(files) != 1 {
		t.Errorf("expected 1 file in upload dir, got %d", len(files))
	}
}

func TestAPIUpload_NoToken(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.apiPost(map[string]any{
		"action":   "upload",
		"filename": "test.txt",
	}, map[string]io.Reader{
		"data": strings.NewReader("data"),
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestAPIShortenURL_Success(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	resp, err := ts.apiPost(map[string]any{
		"action": "shorten_url",
		"token":  token,
		"url":    "https://example.com/very/long/url/that/needs/shortening",
	}, nil)
	if err != nil {
		t.Fatalf("shorten_url failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	var result apiShortenResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.UID == "" {
		t.Error("expected non-empty uid")
	}
	if result.Link == "" {
		t.Error("expected non-empty link")
	}
}

func TestAPIDelete_Success(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	uploadResult := ts.upload(t, token, "todelete.txt", "delete me")

	// Delete it.
	resp, err := ts.apiPost(map[string]any{
		"action": "delete",
		"token":  token,
		"uid":    uploadResult.UID,
	}, nil)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// File should be gone from disk.
	files, _ := os.ReadDir(ts.UploadDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files after delete, got %d", len(files))
	}

	// Count should be 0.
	resp, err = ts.apiPost(map[string]any{
		"action": "count",
		"token":  token,
	}, nil)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	var cr apiCountResponse
	json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	if cr.Count != 0 {
		t.Errorf("expected count 0 after delete, got %d", cr.Count)
	}
}

func TestAPIEditSettings_Success(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	resp, err := ts.apiPost(map[string]any{
		"action":   "edit_settings",
		"token":    token,
		"password": "testpass",
		"settings": map[string]any{
			"ui": map[string]any{
				"history_length":   20,
				"autostart_upload": true,
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("edit_settings failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if ts.Config.UI.HistoryLength != 20 {
		t.Errorf("expected history_length 20, got %d", ts.Config.UI.HistoryLength)
	}
}

func TestAPIEditSettings_WrongPassword(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	resp, err := ts.apiPost(map[string]any{
		"action":   "edit_settings",
		"token":    token,
		"password": "wrongpassword",
		"settings": map[string]any{
			"ui": map[string]any{
				"history_length": 20,
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("edit_settings failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestAPIReleaseToken(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	// Release the token.
	resp, err := ts.apiPost(map[string]any{
		"action": "release_token",
		"token":  token,
	}, nil)
	if err != nil {
		t.Fatalf("release_token failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Token should be invalid now.
	resp, err = ts.apiPost(map[string]any{
		"action": "count",
		"token":  token,
	}, nil)
	if err != nil {
		t.Fatalf("count after release failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("expected 403 after release, got %d", resp.StatusCode)
	}
}

func TestAPIReleaseAllTokens(t *testing.T) {
	ts := newTestServer(t)
	token1 := ts.getToken(t)
	token2 := ts.getToken(t)

	// Release all tokens with token1.
	resp, err := ts.apiPost(map[string]any{
		"action": "release_all_tokens",
		"token":  token1,
	}, nil)
	if err != nil {
		t.Fatalf("release_all_tokens failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	for _, tok := range []string{token1, token2} {
		resp, err := ts.apiPost(map[string]any{
			"action": "count",
			"token":  tok,
		}, nil)
		if err != nil {
			t.Errorf("count for %s failed: %v", tok, err)
			continue
		}
		if resp.StatusCode != 403 {
			t.Errorf("expected 403 for %s after release_all, got %d", tok, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestAPIPruneUnused(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	ts.upload(t, token, "prune_test.txt", "prune me")

	// Delete file from disk to simulate an orphaned link.
	entries, _ := os.ReadDir(ts.UploadDir)
	for _, entry := range entries {
		os.Remove(filepath.Join(ts.UploadDir, entry.Name()))
	}

	// Prune.
	resp, err := ts.apiPost(map[string]any{
		"action": "prune_unused",
		"token":  token,
	}, nil)
	if err != nil {
		t.Fatalf("prune_unused failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result apiPruneResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Pruned != 1 {
		t.Errorf("expected pruned 1, got %d", result.Pruned)
	}
}

func TestAPIBadlyFormattedRequest(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Post(ts.URL+"/api", "application/json",
		strings.NewReader(`{"action":"count"}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestAPIAlternativeUpload_BearerAuth(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	resp, err := ts.apiPostWithAuth(token, nil, map[string]io.Reader{
		"file": strings.NewReader("bearer upload test"),
	})
	if err != nil {
		t.Fatalf("bearer upload failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	link, _ := io.ReadAll(resp.Body)
	if len(link) == 0 {
		t.Error("expected non-empty link in response body")
	}
}

func TestAPIAlternativeUpload_InvalidBearer(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.apiPostWithAuth("invalid-token", nil, map[string]io.Reader{
		"file": strings.NewReader("test"),
	})
	if err != nil {
		t.Fatalf("bearer upload failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestAPIGetThumbnail(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	uploadResult := ts.uploadPNG(t, token, "test.png")

	resp, err := ts.apiPost(map[string]any{
		"action": "get_thumbnail",
		"token":  token,
		"uid":    uploadResult.UID,
	}, nil)
	if err != nil {
		t.Fatalf("get_thumbnail failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result apiThumbnailResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if result.Data == "" {
		t.Error("expected non-empty base64 thumbnail data")
	}
	if result.Width <= 0 || result.Height <= 0 {
		t.Errorf("expected positive dimensions, got %dx%d", result.Width, result.Height)
	}
	if _, err := base64.StdEncoding.DecodeString(result.Data); err != nil {
		t.Errorf("thumbnail data is not valid base64: %v", err)
	}
}

func TestAPIGetThumbnail_NonImage(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	uploadResult := ts.upload(t, token, "text.txt", "not an image")

	resp, err := ts.apiPost(map[string]any{
		"action": "get_thumbnail",
		"token":  token,
		"uid":    uploadResult.UID,
	}, nil)
	if err != nil {
		t.Fatalf("get_thumbnail failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 202 {
		t.Errorf("expected 202 for non-image, got %d", resp.StatusCode)
	}
}

func TestAPIMultipleUploads(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	for i := range 5 {
		ts.upload(t, token, fmt.Sprintf("file%d.txt", i), fmt.Sprintf("content %d", i))
	}

	resp, err := ts.apiPost(map[string]any{
		"action": "count",
		"token":  token,
	}, nil)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	var cr apiCountResponse
	json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()

	if cr.Count != 5 {
		t.Errorf("expected count 5, got %d", cr.Count)
	}
}

func TestAPIGetLinks_Pagination(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	for i := range 10 {
		ts.upload(t, token, fmt.Sprintf("file%d.txt", i), fmt.Sprintf("%d", i))
	}

	// Get page 1 (first 3).
	resp, err := ts.apiPost(map[string]any{
		"action": "get_links",
		"token":  token,
		"limit":  3,
		"offset": 0,
	}, nil)
	if err != nil {
		t.Fatalf("get_links page 1 failed: %v", err)
	}
	var result apiLinksResponse
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if len(result.Links) != 3 {
		t.Errorf("expected 3 links, got %d", len(result.Links))
	}

	// Get page 2 (next 3).
	resp, err = ts.apiPost(map[string]any{
		"action": "get_links",
		"token":  token,
		"limit":  3,
		"offset": 3,
	}, nil)
	if err != nil {
		t.Fatalf("get_links page 2 failed: %v", err)
	}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if len(result.Links) != 3 {
		t.Errorf("expected 3 links, got %d", len(result.Links))
	}
}

func TestAPIUpload_SameName(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	for i := range 3 {
		ts.upload(t, token, "same.txt", fmt.Sprintf("content %d", i))
	}

	resp, err := ts.apiPost(map[string]any{
		"action": "count",
		"token":  token,
	}, nil)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	var cr apiCountResponse
	json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()

	if cr.Count != 3 {
		t.Errorf("expected count 3, got %d", cr.Count)
	}
}

func TestAPIContentServing(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	uploadResult := ts.upload(t, token, "hello.txt", "hello world content")

	resp, err := http.Get(ts.URL + "/" + uploadResult.UID + ".raw")
	if err != nil {
		t.Fatalf("content fetch failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello world content" {
		t.Errorf("expected 'hello world content', got %q", string(body))
	}
}

func TestAPIURLRedirect(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	resp, err := ts.apiPost(map[string]any{
		"action": "shorten_url",
		"token":  token,
		"url":    "https://example.com/redirect-target",
	}, nil)
	if err != nil {
		t.Fatalf("shorten_url failed: %v", err)
	}
	var sr apiShortenResponse
	json.NewDecoder(resp.Body).Decode(&sr)
	resp.Body.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err = client.Get(ts.URL + "/" + sr.UID)
	if err != nil {
		t.Fatalf("redirect fetch failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 303 {
		t.Errorf("expected 303, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "https://example.com/redirect-target" {
		t.Errorf("expected redirect to 'https://example.com/redirect-target', got %q", loc)
	}
}

func TestAPIContent404(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/nonexistent")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAPIStaticAssetServing(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/static/delfticons/none.svg")
	if err != nil {
		t.Fatalf("static asset request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 500 {
		t.Errorf("unexpected 500 for static asset")
	}
}

func TestAPIUpload_DifferentMimeTypes(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	tests := []struct {
		name     string
		filename string
		data     []byte
		expMime  string
	}{
		{"html", "page.html", []byte("<html><body>test</body></html>"), "text/html"},
		{"json", "data.json", []byte(`{"key":"value"}`), "application/json"},
		{"css", "style.css", []byte("body { color: red; }"), "text/css"},
		{"javascript", "script.js", []byte("console.log('hi');"), "application/javascript"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := ts.apiPost(map[string]any{
				"action":   "upload",
				"token":    token,
				"filename": tt.filename,
			}, map[string]io.Reader{
				"data": bytes.NewReader(tt.data),
			})
			if err != nil {
				t.Fatalf("upload failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 201 {
				t.Errorf("expected 201, got %d", resp.StatusCode)
			}

			var result apiUploadResponse
			json.NewDecoder(resp.Body).Decode(&result)
			if result.Mime != tt.expMime {
				t.Errorf("expected mime %q, got %q", tt.expMime, result.Mime)
			}
		})
	}
}

func TestAPIPasteUpload(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	result := ts.upload(t, token, "paste.go", `func main() {
	fmt.Println("Hello, World!")
}`)

	if result.Name != "paste.go" {
		t.Errorf("expected name 'paste.go', got %q", result.Name)
	}
	if result.Mime != "text/plain" {
		t.Errorf("expected mime 'text/plain', got %q", result.Mime)
	}
}

func TestAuthLogin(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.PostForm(ts.URL+"/login", map[string][]string{
		"login[username]": {"testuser"},
		"login[password]": {"testpass"},
	})
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 302 && resp.StatusCode != 303 {
		t.Errorf("expected 200 or redirect, got %d", resp.StatusCode)
	}
}

func TestAuthLogin_BadCredentials(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.PostForm(ts.URL+"/login", map[string][]string{
		"login[username]": {"testuser"},
		"login[password]": {"wrongpass"},
	})
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("expected 403 for bad login, got %d", resp.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	ts := newTestServer(t)
	ts.getToken(t) // ensure a session exists

	resp, err := http.PostForm(ts.URL+"/logout", map[string][]string{})
	if err != nil {
		t.Fatalf("logout request failed: %v", err)
	}
	defer resp.Body.Close()

	// Logout may set a cookie and redirect; just verify it doesn't 5xx.
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("unexpected 5xx from logout: %d: %s", resp.StatusCode, body)
	}
}

func TestPanelPage(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: "token", Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("panel request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200 for panel, got %d", resp.StatusCode)
	}
}

func TestPanelPage_Unauthorized(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("panel request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200 (login page), got %d", resp.StatusCode)
	}
}

func TestInstallerRedirect_WhenConfigured(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		t.Errorf("expected 200 or 302, got %d", resp.StatusCode)
	}
}

func TestContentRawExtension(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	const code = "package main\n\nfunc main() {}"
	uploadResult := ts.upload(t, token, "test.go", code)

	resp, err := http.Get(ts.URL + "/" + uploadResult.UID + ".raw")
	if err != nil {
		t.Fatalf("raw fetch failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != code {
		t.Errorf("expected raw content, got %q", string(body))
	}
}

func TestAPILinkGeneration_UniqueIDs(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	// 50 uploads with length=4 → ~62^4 ≅ 14.7M combinations.  Collision
	// probability is vanishingly small but non-zero.  If this ever flakes,
	// increase length or use a deterministic UID generator stub.
	ids := make(map[string]bool, 50)
	for i := range 50 {
		result := ts.upload(t, token, fmt.Sprintf("file%d.txt", i), "content")
		if ids[result.UID] {
			t.Errorf("duplicate UID generated: %s", result.UID)
		}
		ids[result.UID] = true
	}

	if len(ids) != 50 {
		t.Errorf("expected 50 unique IDs, got %d", len(ids))
	}
}

func TestAPISessionPersistence(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	for i := range 3 {
		resp, err := ts.apiPost(map[string]any{
			"action": "count",
			"token":  token,
		}, nil)
		if err != nil {
			t.Fatalf("count %d failed: %v", i, err)
		}
		if resp.StatusCode != 200 {
			t.Errorf("count %d: expected 200, got %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestAPIUpload_LargeFile(t *testing.T) {
	ts := newTestServer(t)
	token := ts.getToken(t)

	data := bytes.Repeat([]byte("A"), 1024*1024)

	ts.uploadData(t, token, "large.bin", data)

	// Verify the file size on disk by reading the actual stored file.
	files, _ := os.ReadDir(ts.UploadDir)
	if len(files) == 0 {
		t.Fatal("expected a file in upload dir")
	}
	info, err := files[0].Info()
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if info.Size() != 1024*1024 {
		t.Errorf("expected 1048576 bytes, got %d", info.Size())
	}
}

func (ts *testServer) uploadPNG(t *testing.T, token, filename string) apiUploadResponse {
	t.Helper()
	return ts.uploadData(t, token, filename, tinyPNG)
}

// uploadData is like upload but accepts raw bytes instead of a string.
func (ts *testServer) uploadData(t *testing.T, token, filename string, data []byte) apiUploadResponse {
	t.Helper()
	resp, err := ts.apiPost(map[string]any{
		"action":   "upload",
		"token":    token,
		"filename": filename,
	}, map[string]io.Reader{
		"data": bytes.NewReader(data),
	})
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	var result apiUploadResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}
