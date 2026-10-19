// Command xwebfm serves a web-based file manager for a server's filesystem.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alanaktion/xwebfm/internal/api"
	"github.com/alanaktion/xwebfm/internal/fsops"
	"github.com/alanaktion/xwebfm/web"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address to listen on")
	root := flag.String("root", "/", "filesystem root exposed by the file manager")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, *listen, *root); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, listen, root string) error {
	fsys, err := fsops.New(root, nil)
	if err != nil {
		return err
	}
	defer fsys.Close()
	if os.Geteuid() != 0 {
		log.Warn("not running as root; ownership changes and access to other users' files will fail")
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           (&api.Server{FS: fsys, Log: log, Static: web.Handler()}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", listen, "root", fsys.Root())
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
