// load.link - File upload, paste, and URL shortening server.
package main

import (
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/115100/load.link/internal/config"
	"github.com/115100/load.link/internal/db"
	"github.com/115100/load.link/internal/handler"
)

func main() {
	addr := flag.String("addr", "./load.link.sock", "listen address (unix socket path or host:port)")
	configPath := flag.String("config", ".config.toml", "path to config file")
	flag.Parse()

	slog.Info("load.link starting", "addr", *addr, "config", *configPath)

	if err := run(*addr, *configPath); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run(addr, configPath string) error {
	cfgPath, err := config.MaybeMigrateINI(configPath)
	if err != nil {
		return err
	}

	var h *handler.Handler
	if _, err := os.Stat(cfgPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		slog.Info("no config found, starting web installer")
		h, err = handler.New(nil, nil, "", configPath)
		if err != nil {
			return err
		}
	} else {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return err
		}

		uploadDir := cfg.Link.UploadDir
		if uploadDir == "" || uploadDir == "." {
			uploadDir = "uploads"
		}
		absUploadDir, err := filepath.Abs(uploadDir)
		if err != nil {
			return err
		}

		database, err := db.New(cfg)
		if err != nil {
			return err
		}
		if err := database.Install(); err != nil {
			database.Close()
			return err
		}

		h, err = handler.New(cfg, database, absUploadDir, configPath)
		if err != nil {
			return err
		}
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := &http.Server{Handler: loggingMiddleware(mux)}

	ln, err := listen(addr)
	if err != nil {
		return err
	}

	slog.Info("listening", "addr", addr)
	return srv.Serve(ln)
}

func listen(addr string) (net.Listener, error) {
	if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "/") && !strings.HasPrefix(addr, ".") {
		return net.Listen("tcp", addr)
	}

	os.Remove(addr)
	ln, err := net.Listen("unix", addr)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(addr, 0666); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}
