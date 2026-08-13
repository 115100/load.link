// Package db provides the database layer for load.link.
package db

import (
	"crypto/rand"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/115100/load.link/internal/config"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// DB wraps the SQL database connection and provides load.link-specific operations.
type DB struct {
	conn   *sql.DB
	cfg    *config.Config
	dbType string
}

// New creates a new DB connection based on the config.
func New(cfg *config.Config) (*DB, error) {
	dbType := cfg.Database.Type
	dbName := cfg.Database.Name

	var driver, dsn string

	switch dbType {
	case "sqlite":
		driver = "sqlite"
		if strings.HasPrefix(dbName, "file:") {
			dsn = dbName
		} else if dbName == ":memory:" {
			dsn = "file::memory:?cache=shared&_texttotime"
		} else {
			dsn = dbName
		}
	case "pgsql":
		driver = "pgx"
		dsn = buildPostgresDSN(cfg)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}

	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("could not open database: %w", err)
	}

	if dbType == "sqlite" {
		conn.SetMaxOpenConns(1) // SQLite needs this for concurrency safety
	}

	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("could not ping database: %w", err)
	}

	return &DB{
		conn:   conn,
		cfg:    cfg,
		dbType: dbType,
	}, nil
}

func buildPostgresDSN(cfg *config.Config) string {
	host := cfg.Database.Host
	port := cfg.Database.Port
	username := cfg.Database.Username
	password := cfg.Database.Password
	name := cfg.Database.Name

	u := &url.URL{Scheme: "postgres", Path: name}
	if username != "" {
		if password != "" {
			u.User = url.UserPassword(username, password)
		} else {
			u.User = url.User(username)
		}
	}

	q := make(url.Values)
	if host != "" {
		q.Set("host", host)
	}
	if port != "" && port != "5432" {
		q.Set("port", port)
	}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// rebind converts ? placeholders to $1, $2 for PostgreSQL.
// SQLite queries are returned unchanged.
func (db *DB) rebind(query string) string {
	if db.dbType == "sqlite" {
		return query
	}
	var result strings.Builder
	n := 1
	for _, c := range query {
		if c == '?' {
			result.WriteString(fmt.Sprintf("$%d", n))
			n++
		} else {
			result.WriteRune(c)
		}
	}
	return result.String()
}

// Link represents an uploaded item or shortened URL.
type Link struct {
	UID  string    `json:"uid"`
	Path string    `json:"path"`
	Name string    `json:"name"`
	Ext  string    `json:"ext"`
	Mime string    `json:"mime"`
	Date time.Time `json:"date"`
}

// Thumbnail represents a generated thumbnail stored in the DB.
type Thumbnail struct {
	Data   []byte `json:"data"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Mime   string `json:"mime"`
}

// Install creates the database tables if they don't exist.
func (db *DB) Install() error {
	length := db.cfg.Link.Length
	if length < 1 {
		length = 4
	}
	lengthStr := strconv.Itoa(length)

	blobType := "BLOB"
	if db.dbType != "sqlite" {
		blobType = "BYTEA"
	}

	queries := []string{
		`CREATE TABLE IF NOT EXISTS links (
			uid VARCHAR(` + lengthStr + `) PRIMARY KEY,
			path TEXT,
			name VARCHAR(255),
			ext VARCHAR(255),
			mime VARCHAR(255),
			date TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS thumbnails (
			uid VARCHAR(` + lengthStr + `) PRIMARY KEY,
			data ` + blobType + `,
			mime VARCHAR(255),
			width INTEGER,
			height INTEGER
		)`,

		`CREATE TABLE IF NOT EXISTS sessions (
			token VARCHAR(128) PRIMARY KEY
		)`,
	}

	for _, q := range queries {
		if _, err := db.conn.Exec(q); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}
	return nil
}

// GenerateUID creates a unique ID for a link.
func (db *DB) GenerateUID() (string, error) {
	characters := db.cfg.Link.Characters
	length := db.cfg.Link.Length
	if length < 1 {
		length = 8
	}

	id := make([]byte, length)
	for i := range id {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(characters))))
		if err != nil {
			return "", err
		}
		id[i] = characters[n.Int64()]
	}
	return string(id), nil
}

// AddLink inserts a new link into the database.
func (db *DB) AddLink(path, name, mime string) (string, error) {
	uid, err := db.GenerateUID()
	if err != nil {
		return "", err
	}

	ext := ""
	if path != "" {
		ext = filepath.Ext(name)
		if len(ext) > 0 {
			ext = ext[1:] // Remove leading dot
		}
	}

	_, err = db.conn.Exec(
		db.rebind("INSERT INTO links (uid, path, name, ext, mime) VALUES (?, ?, ?, ?, ?)"),
		uid, path, name, ext, mime,
	)
	if err != nil {
		return "", fmt.Errorf("failed to insert link: %w", err)
	}

	return uid, nil
}

// GetLink retrieves a link by UID.
func (db *DB) GetLink(uid string) (*Link, error) {
	row := db.conn.QueryRow(
		db.rebind("SELECT uid, path, name, ext, mime, date FROM links WHERE uid = ?"),
		uid,
	)

	var link Link
	err := row.Scan(&link.UID, &link.Path, &link.Name, &link.Ext, &link.Mime, &link.Date)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &link, nil
}

// searchWhere returns the WHERE clause fragment and the LIKE pattern
// arguments for a case-insensitive search across name, extension and MIME
// type. An empty (or blank) search yields no filter.
func (db *DB) searchWhere(search string) (string, []string) {
	search = strings.TrimSpace(search)
	if search == "" {
		return "", nil
	}
	// Escape LIKE wildcards so user input matches literally.
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	).Replace(search)
	pattern := "%" + escaped + "%"
	where := "WHERE LOWER(name) LIKE LOWER(?) ESCAPE '\\' " +
		"OR LOWER(ext) LIKE LOWER(?) ESCAPE '\\' " +
		"OR LOWER(mime) LIKE LOWER(?) ESCAPE '\\'"
	return where, []string{pattern, pattern, pattern}
}

// GetLinks retrieves links with optional pagination, optionally filtered by a
// case-insensitive search across name, extension and MIME type.
func (db *DB) GetLinks(limit, offset int, search string) ([]Link, error) {
	query := "SELECT uid, path, name, ext, mime, date FROM links"

	var args []any
	if search != "" {
		where, whereArgs := db.searchWhere(search)
		if where != "" {
			query += " " + where
			for _, arg := range whereArgs {
				args = append(args, arg)
			}
		}
	}
	query += " ORDER BY date DESC"

	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	if offset > 0 {
		query += " OFFSET ?"
		args = append(args, offset)
	}

	rows, err := db.conn.Query(db.rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []Link
	for rows.Next() {
		var link Link
		if err := rows.Scan(&link.UID, &link.Path, &link.Name, &link.Ext, &link.Mime, &link.Date); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

// CountLinks returns the total number of links, optionally filtered by a
// case-insensitive search across name, extension and MIME type.
func (db *DB) CountLinks(search string) (int, error) {
	query := "SELECT COUNT(*) FROM links"

	var args []any
	if search != "" {
		where, whereArgs := db.searchWhere(search)
		if where != "" {
			query += " " + where
			for _, arg := range whereArgs {
				args = append(args, arg)
			}
		}
	}

	var count int
	err := db.conn.QueryRow(db.rebind(query), args...).Scan(&count)
	return count, err
}

// DelLink deletes a link and its associated thumbnail.
func (db *DB) DelLink(uid string) error {
	_, err := db.conn.Exec(
		db.rebind("DELETE FROM links WHERE uid = ?"),
		uid,
	)
	if err != nil {
		return err
	}
	db.conn.Exec(
		db.rebind("DELETE FROM thumbnails WHERE uid = ?"),
		uid,
	)
	return nil
}

// GetLinksAll returns all links (for prune operation).
func (db *DB) GetLinksAll() ([]Link, error) {
	return db.GetLinks(0, 0, "")
}

// StoreThumbnail stores a thumbnail in the database.
func (db *DB) StoreThumbnail(uid string, thumb *Thumbnail) error {
	if db.dbType == "sqlite" {
		_, err := db.conn.Exec(
			db.rebind("INSERT OR REPLACE INTO thumbnails (uid, data, mime, width, height) VALUES (?, ?, ?, ?, ?)"),
			uid, thumb.Data, thumb.Mime, thumb.Width, thumb.Height,
		)
		return err
	}
	// PostgreSQL upsert
	_, err := db.conn.Exec(
		db.rebind(`INSERT INTO thumbnails (uid, data, mime, width, height)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (uid) DO UPDATE SET
				data = EXCLUDED.data,
				mime = EXCLUDED.mime,
				width = EXCLUDED.width,
				height = EXCLUDED.height`),
		uid, thumb.Data, thumb.Mime, thumb.Width, thumb.Height,
	)
	return err
}

// GetThumbnail retrieves a thumbnail from the database.
func (db *DB) GetThumbnail(uid string) (*Thumbnail, error) {
	row := db.conn.QueryRow(
		db.rebind("SELECT data, mime, width, height FROM thumbnails WHERE uid = ?"),
		uid,
	)

	var thumb Thumbnail
	err := row.Scan(&thumb.Data, &thumb.Mime, &thumb.Width, &thumb.Height)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &thumb, nil
}

// AddSession creates a new auth session and returns the token.
func (db *DB) AddSession() (string, error) {
	b := make([]byte, 64)
	rand.Read(b)
	token := hex.EncodeToString(b)
	hashed := fmt.Sprintf("%x", sha512.Sum512([]byte(token)))

	_, err := db.conn.Exec(
		db.rebind("INSERT INTO sessions (token) VALUES (?)"),
		hashed,
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

// GetSession checks if a token is valid.
func (db *DB) GetSession(token string) (bool, error) {
	hashed := fmt.Sprintf("%x", sha512.Sum512([]byte(token)))

	var count int
	err := db.conn.QueryRow(
		db.rebind("SELECT COUNT(*) FROM sessions WHERE token = ?"),
		hashed,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// DelSession removes a session token.
func (db *DB) DelSession(token string) error {
	hashed := fmt.Sprintf("%x", sha512.Sum512([]byte(token)))
	_, err := db.conn.Exec(
		db.rebind("DELETE FROM sessions WHERE token = ?"),
		hashed,
	)
	return err
}

// DelAllSessions removes all sessions.
func (db *DB) DelAllSessions() error {
	_, err := db.conn.Exec(
		db.rebind("DELETE FROM sessions"),
	)
	return err
}
