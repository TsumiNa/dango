/*
Copyright © 2026 NAME HERE <EMAIL ADDRESS>
*/

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSetupRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		path         string
		expectedCode int
		expectedBody map[string]interface{}
	}{
		{
			name:         "Ping Endpoint",
			path:         "/api/v1/ping",
			expectedCode: http.StatusOK,
			expectedBody: map[string]interface{}{
				"message": "pong",
			},
		},
		{
			name:         "Hello Endpoint",
			path:         "/api/v1/hello",
			expectedCode: http.StatusOK,
			expectedBody: map[string]interface{}{
				"message": "hello from dango",
			},
		},
		{
			name:         "Not Found",
			path:         "/api/v1/unknown",
			expectedCode: http.StatusNotFound,
			expectedBody: nil,
		},
	}

	r := gin.New()
	SetupRouter(r)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tt.path, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.expectedCode {
				t.Errorf("Expected status code %d, got %d", tt.expectedCode, w.Code)
			}

			if tt.expectedBody != nil {
				var response map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatalf("Failed to unmarshal response: %v", err)
				}

				if len(response) != len(tt.expectedBody) {
					t.Errorf("Expected body %v, got %v", tt.expectedBody, response)
				}

				for k, v := range tt.expectedBody {
					if response[k] != v {
						t.Errorf("Expected key %q to be %v, got %v", k, v, response[k])
					}
				}
			}
		})
	}
}
