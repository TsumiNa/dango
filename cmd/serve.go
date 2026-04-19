/*
Copyright © 2026 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tsumina/dango/internal/server"
)

// serveCmd represents the serve command
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Dango API server",
	Long:  `Start the API server, listening on the specified IP, port, and/or unix socket.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ip, _ := cmd.Flags().GetString("ip")
		port, _ := cmd.Flags().GetInt("port")
		socketPath, _ := cmd.Flags().GetString("unix-socket")

		httpAddr := fmt.Sprintf("%s:%d", ip, port)

		fmt.Printf("Starting server on HTTP %s and Unix Socket %q\n", httpAddr, socketPath)

		app := server.New()
		return app.Start(cmd.Context(), httpAddr, socketPath)
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)

	serveCmd.Flags().String("ip", "127.0.0.1", "IP address to listen on")
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
	serveCmd.Flags().String("unix-socket", "", "Path to unix socket file (e.g. /tmp/dango.sock)")
}
