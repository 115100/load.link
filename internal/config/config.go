// Package config manages the load.link configuration (TOML).
package config

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

type Config struct {
	mu       sync.RWMutex
	Database DatabaseConfig `toml:"database"`
	Link     LinkConfig     `toml:"link"`
	UI       UIConfig       `toml:"ui"`
	Routing  RoutingConfig  `toml:"routing"`
	Login    LoginConfig    `toml:"login"`
}

type DatabaseConfig struct {
	Type     string `toml:"type"`
	Name     string `toml:"name"`
	Host     string `toml:"host"`
	Port     string `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

type LinkConfig struct {
	Characters     string `toml:"characters"`
	Length         int    `toml:"length"`
	UploadDir      string `toml:"upload_dir"`
	SameNameSuffix string `toml:"same_name_suffix"`
	ShowExtension  bool   `toml:"show_extension"`
}

type UIConfig struct {
	WaitTime             int  `toml:"wait_time"`
	HistoryLength        int  `toml:"history_length"`
	AutostartUpload      bool `toml:"autostart_upload"`
	DisplayThumbnail     bool `toml:"display_thumbnail"`
	SyntaxHighlighter    bool `toml:"syntax_highlighter"`
	MediaPlayer          bool `toml:"media_player"`
	MessageTimeout       int  `toml:"message_timeout"`
	DeletionConfirmation bool `toml:"deletion_confirmation"`
	GalleryItems         int  `toml:"gallery_items"`
}

type RoutingConfig struct {
	Mode     string `toml:"mode"`
	BaseURL  string `toml:"baseurl"`
	Panel    string `toml:"panel"`
	Homepage string `toml:"homepage"`
}

type LoginConfig struct {
	Username string `toml:"username"`
	Password string `toml:"password"`
	Salt     string `toml:"salt"`
}

// SettingsPayload is the typed request body for the edit_settings API.
// Every field is a pointer — nil means "not present in the request".
type SettingsPayload struct {
	Database *DatabaseSettings `json:"database,omitempty"`
	Link     *LinkSettings     `json:"link,omitempty"`
	UI       *UISettings       `json:"ui,omitempty"`
	Routing  *RoutingSettings  `json:"routing,omitempty"`
	Login    *LoginSettings    `json:"login,omitempty"`
}

type DatabaseSettings struct {
	Type     *string `json:"type,omitempty"`
	Name     *string `json:"name,omitempty"`
	Host     *string `json:"host,omitempty"`
	Port     *string `json:"port,omitempty"`
	Username *string `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
}

type LinkSettings struct {
	Characters     *string `json:"characters,omitempty"`
	Length         *int    `json:"length,omitempty"`
	UploadDir      *string `json:"upload_dir,omitempty"`
	SameNameSuffix *string `json:"same_name_suffix,omitempty"`
	ShowExtension  *bool   `json:"show_extension,omitempty"`
}

type UISettings struct {
	WaitTime             *int  `json:"wait_time,omitempty"`
	HistoryLength        *int  `json:"history_length,omitempty"`
	AutostartUpload      *bool `json:"autostart_upload,omitempty"`
	DisplayThumbnail     *bool `json:"display_thumbnail,omitempty"`
	SyntaxHighlighter    *bool `json:"syntax_highlighter,omitempty"`
	MediaPlayer          *bool `json:"media_player,omitempty"`
	MessageTimeout       *int  `json:"message_timeout,omitempty"`
	DeletionConfirmation *bool `json:"deletion_confirmation,omitempty"`
	GalleryItems         *int  `json:"gallery_items,omitempty"`
}

type RoutingSettings struct {
	Mode     *string `json:"mode,omitempty"`
	BaseURL  *string `json:"baseurl,omitempty"`
	Panel    *string `json:"panel,omitempty"`
	Homepage *string `json:"homepage,omitempty"`
}

type LoginSettings struct {
	Username *string `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
}

func Default() *Config {
	return &Config{
		Database: DatabaseConfig{
			Type: "sqlite",
			Name: ".db.sqlite",
			Host: "localhost",
		},
		Link: LinkConfig{
			Characters:     "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ",
			Length:         8,
			UploadDir:      ".",
			SameNameSuffix: ".1",
		},
		UI: UIConfig{
			WaitTime:             3,
			HistoryLength:        10,
			DisplayThumbnail:     true,
			SyntaxHighlighter:    true,
			MediaPlayer:          true,
			MessageTimeout:       10000,
			DeletionConfirmation: true,
			GalleryItems:         30,
		},
		Routing: RoutingConfig{
			Mode: "path",
		},
	}
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := Default()
	if err := toml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return c, nil
}

func (c *Config) Save(path string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func MaybeMigrateINI(tomlPath string) (string, error) {
	if _, err := os.Stat(tomlPath); err == nil {
		return tomlPath, nil
	}

	iniPath := strings.Replace(tomlPath, ".toml", ".ini", 1)
	if _, err := os.Stat(iniPath); err != nil {
		return tomlPath, nil
	}
	data, err := os.ReadFile(iniPath)
	if err != nil {
		return tomlPath, nil
	}
	c := loadINI(string(data))
	if err := c.Save(tomlPath); err != nil {
		return tomlPath, fmt.Errorf("migrating INI: %w", err)
	}
	return tomlPath, nil
}

func loadINI(data string) *Config {
	c := Default()
	ini := make(map[string]map[string]string)

	var section string
	for line := range strings.SplitSeq(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if ini[section] == nil {
				ini[section] = make(map[string]string)
			}
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
		if ini[section] == nil {
			ini[section] = make(map[string]string)
		}
		ini[section][key] = value
	}

	applyINITo(c, ini)
	return c
}

func applyINITo(c *Config, ini map[string]map[string]string) {
	// Apply each known section / key to the typed struct.
	for section, keys := range ini {
		switch section {
		case "database":
			for k, v := range keys {
				switch k {
				case "type":
					c.Database.Type = v
				case "name":
					c.Database.Name = v
				case "host":
					c.Database.Host = v
				case "port":
					c.Database.Port = v
				case "username":
					c.Database.Username = v
				case "password":
					c.Database.Password = v
				}
			}
		case "link":
			for k, v := range keys {
				switch k {
				case "characters":
					c.Link.Characters = v
				case "length":
					c.Link.Length = atoiOr(v, 8)
				case "upload_dir":
					c.Link.UploadDir = v
				case "same_name_suffix":
					c.Link.SameNameSuffix = v
				case "show_extension":
					c.Link.ShowExtension = strings.EqualFold(v, "true")
				}
			}
		case "ui":
			for k, v := range keys {
				switch k {
				case "wait_time":
					c.UI.WaitTime = atoiOr(v, 3)
				case "history_length":
					c.UI.HistoryLength = atoiOr(v, 10)
				case "autostart_upload":
					c.UI.AutostartUpload = strings.EqualFold(v, "true")
				case "display_thumbnail":
					c.UI.DisplayThumbnail = strings.EqualFold(v, "true")
				case "syntax_highlighter":
					c.UI.SyntaxHighlighter = strings.EqualFold(v, "true")
				case "media_player":
					c.UI.MediaPlayer = strings.EqualFold(v, "true")
				case "message_timeout":
					c.UI.MessageTimeout = atoiOr(v, 10000)
				case "deletion_confirmation":
					c.UI.DeletionConfirmation = strings.EqualFold(v, "true")
				case "gallery_items":
					c.UI.GalleryItems = atoiOr(v, 30)
				}
			}
		case "routing":
			for k, v := range keys {
				switch k {
				case "mode":
					c.Routing.Mode = v
				case "baseurl":
					c.Routing.BaseURL = v
				case "panel":
					c.Routing.Panel = v
				case "homepage":
					c.Routing.Homepage = v
				}
			}
		case "login":
			for k, v := range keys {
				switch k {
				case "username":
					c.Login.Username = v
				case "password":
					c.Login.Password = v
				case "salt":
					c.Login.Salt = v
				}
			}
		}
	}
}

func (c *Config) SetPassword(password string) {
	b := make([]byte, 16)
	rand.Read(b)
	salt := hex.EncodeToString(b)

	mac := hmac.New(sha512.New, []byte(salt))
	mac.Write([]byte(password))
	hash := hex.EncodeToString(mac.Sum(nil))

	c.mu.Lock()
	c.Login.Password = hash
	c.Login.Salt = salt
	c.mu.Unlock()
}

func (c *Config) CheckPassword(password string) bool {
	c.mu.RLock()
	salt := c.Login.Salt
	expectedHash := c.Login.Password
	c.mu.RUnlock()

	mac := hmac.New(sha512.New, []byte(salt))
	mac.Write([]byte(password))
	hash := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(hash), []byte(expectedHash))
}

func (c *Config) GetAll() map[string]map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return map[string]map[string]any{
		"database": {
			"type":     c.Database.Type,
			"name":     c.Database.Name,
			"host":     c.Database.Host,
			"port":     c.Database.Port,
			"username": c.Database.Username,
			"password": c.Database.Password,
		},
		"link": {
			"characters":      c.Link.Characters,
			"length":          c.Link.Length,
			"upload_dir":      c.Link.UploadDir,
			"same_name_suffix": c.Link.SameNameSuffix,
			"show_extension":  c.Link.ShowExtension,
		},
		"ui": {
			"wait_time":             c.UI.WaitTime,
			"history_length":        c.UI.HistoryLength,
			"autostart_upload":      c.UI.AutostartUpload,
			"display_thumbnail":     c.UI.DisplayThumbnail,
			"syntax_highlighter":    c.UI.SyntaxHighlighter,
			"media_player":          c.UI.MediaPlayer,
			"message_timeout":       c.UI.MessageTimeout,
			"deletion_confirmation": c.UI.DeletionConfirmation,
			"gallery_items":         c.UI.GalleryItems,
		},
		"routing": {
			"mode":     c.Routing.Mode,
			"baseurl":  c.Routing.BaseURL,
			"panel":    c.Routing.Panel,
			"homepage": c.Routing.Homepage,
		},
		"login": {
			"username": c.Login.Username,
			"password": c.Login.Password,
			"salt":     c.Login.Salt,
		},
	}
}

func (c *Config) ApplySettings(s *SettingsPayload) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if s.Database != nil {
		if s.Database.Type != nil {
			c.Database.Type = *s.Database.Type
		}
		if s.Database.Name != nil {
			c.Database.Name = *s.Database.Name
		}
		if s.Database.Host != nil {
			c.Database.Host = *s.Database.Host
		}
		if s.Database.Port != nil {
			c.Database.Port = *s.Database.Port
		}
		if s.Database.Username != nil {
			c.Database.Username = *s.Database.Username
		}
		if s.Database.Password != nil {
			c.Database.Password = *s.Database.Password
		}
	}
	if s.Link != nil {
		if s.Link.Characters != nil {
			c.Link.Characters = *s.Link.Characters
		}
		if s.Link.Length != nil {
			c.Link.Length = *s.Link.Length
		}
		if s.Link.UploadDir != nil {
			c.Link.UploadDir = *s.Link.UploadDir
		}
		if s.Link.SameNameSuffix != nil {
			c.Link.SameNameSuffix = *s.Link.SameNameSuffix
		}
		if s.Link.ShowExtension != nil {
			c.Link.ShowExtension = *s.Link.ShowExtension
		}
	}
	if s.UI != nil {
		if s.UI.WaitTime != nil {
			c.UI.WaitTime = *s.UI.WaitTime
		}
		if s.UI.HistoryLength != nil {
			c.UI.HistoryLength = *s.UI.HistoryLength
		}
		if s.UI.AutostartUpload != nil {
			c.UI.AutostartUpload = *s.UI.AutostartUpload
		}
		if s.UI.DisplayThumbnail != nil {
			c.UI.DisplayThumbnail = *s.UI.DisplayThumbnail
		}
		if s.UI.SyntaxHighlighter != nil {
			c.UI.SyntaxHighlighter = *s.UI.SyntaxHighlighter
		}
		if s.UI.MediaPlayer != nil {
			c.UI.MediaPlayer = *s.UI.MediaPlayer
		}
		if s.UI.MessageTimeout != nil {
			c.UI.MessageTimeout = *s.UI.MessageTimeout
		}
		if s.UI.DeletionConfirmation != nil {
			c.UI.DeletionConfirmation = *s.UI.DeletionConfirmation
		}
		if s.UI.GalleryItems != nil {
			c.UI.GalleryItems = *s.UI.GalleryItems
		}
	}
	if s.Routing != nil {
		if s.Routing.Mode != nil {
			c.Routing.Mode = *s.Routing.Mode
		}
		if s.Routing.BaseURL != nil {
			c.Routing.BaseURL = *s.Routing.BaseURL
		}
		if s.Routing.Panel != nil {
			c.Routing.Panel = *s.Routing.Panel
		}
		if s.Routing.Homepage != nil {
			c.Routing.Homepage = *s.Routing.Homepage
		}
	}
	if s.Login != nil {
		if s.Login.Username != nil {
			c.Login.Username = *s.Login.Username
		}
	}
}

func (c *Config) Validate() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.Database.Name == "" {
		return fmt.Errorf("database name too short")
	}
	if c.Link.Characters == "" {
		return fmt.Errorf("no link characters specified")
	}
	if c.Link.Length < 1 {
		return fmt.Errorf("link length must be at least 1")
	}
	if c.Login.Username == "" {
		return fmt.Errorf("you must choose a username")
	}
	return nil
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
