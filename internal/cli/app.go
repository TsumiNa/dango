package cli

import (
	"context"
	"io"
)

type App struct {
	ctx context.Context
}

func New(stdout, stderr io.Writer) *App {
	return &App{}
}

func (a *App) Start(ctx context.Context, args []string) error {
	a.ctx = ctx
	return nil
}
