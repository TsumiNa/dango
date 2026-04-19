package server

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func getFreePort() int {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestApp_Start(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tempDir, err := os.MkdirTemp("", "dango-socket-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbSocketPath := filepath.Join(tempDir, "dango.sock")
	port := getFreePort()
	if port == 0 {
		t.Fatalf("Failed to get free port")
	}
	httpAddrRaw := "127.0.0.1:" + strconv.Itoa(port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := New()
	errChan := make(chan error, 1)
	go func() {
		errChan <- app.Start(ctx, httpAddrRaw, dbSocketPath)
	}()

	time.Sleep(200 * time.Millisecond)

	resp, err := http.Get("http://" + httpAddrRaw + "/api/v1/ping")
	if err != nil {
		t.Fatalf("Failed to ping TCP server: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("App returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Start did not return in time after cancellation")
	}
}

func TestApp_Start_BadAddress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := app.Start(ctx, "256.256.256.256:0", "")
	if err == nil {
		t.Fatalf("Expected an error from mapping a bad TCP address")
	}
}
