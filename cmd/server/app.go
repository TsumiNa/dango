package server

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/gin-gonic/gin"
)

// App represents the API server application.
type App struct {
	router *gin.Engine
}

// New creates a new fully configured App instance.
func New() *App {
	r := gin.Default()
	SetupRouter(r)

	return &App{
		router: r,
	}
}

// Start listens on both the specified HTTP address and Unix socket path.
// It continues running until the provided context is canceled.
func (a *App) Start(ctx context.Context, httpAddr, socketPath string) error {
	var wg sync.WaitGroup
	errChan := make(chan error, 2)

	// Setup shared HTTP server config
	server := &http.Server{
		Handler: a.router,
	}

	// Ensure socket path directory exists and clean up old socket
	if socketPath != "" {
		if err := os.RemoveAll(socketPath); err != nil {
			return err
		}
		unixListener, err := net.Listen("unix", socketPath)
		if err != nil {
			return err
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Printf("Listening on Unix socket: %s", socketPath)
			if err := server.Serve(unixListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errChan <- err
			}
		}()
	}

	// Setup HTTP (TCP) listener
	if httpAddr != "" {
		tcpListener, err := net.Listen("tcp", httpAddr)
		if err != nil {
			return err
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Printf("Listening on HTTP: %s", httpAddr)
			if err := server.Serve(tcpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errChan <- err
			}
		}()
	}

	// Wait for context cancellation to gracefully shut down
	select {
	case <-ctx.Done():
		log.Println("Shutting down servers...")
		if err := server.Shutdown(context.Background()); err != nil {
			return err
		}
	case err := <-errChan:
		log.Printf("Server error: %v", err)
		server.Shutdown(context.Background()) // attempt cleanup
		return err
	}

	wg.Wait()
	return nil
}
