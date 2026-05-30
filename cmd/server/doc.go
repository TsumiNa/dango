/*
Copyright © 2026 NAME HERE <EMAIL ADDRESS>
*/

// Package server provides the HTTP and Unix socket API server for the Dango application.
//
// It manages the complete lifecycle of the web server through the App type, which
// configures routes, binds handlers, and handles graceful shutdown across multiple
// listeners simultaneously (e.g., both TCP ports and local Unix sockets).
// The primary entry point is New() to create an application instance, followed by Start()
// to begin listening for incoming requests.
package server
