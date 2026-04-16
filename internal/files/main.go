package main

import (
	"context"
	"fmt"
	"os"

	"github.com/terrai/skillframe/executor"
	"github.com/terrai/skillframe/llm"
	"github.com/terrai/skillframe/registry"
	"go.uber.org/zap"
)

func main() {
	log, _ := zap.NewDevelopment()
	defer log.Sync()

	// 1. Create LLM client
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "set ANTHROPIC_API_KEY")
		os.Exit(1)
	}
	client := llm.NewAnthropic(apiKey)

	// 2. Load skills from directory
	reg := registry.New(nil, log) // nil = use default keyword matcher
	if err := reg.LoadDir("./skills"); err != nil {
		log.Fatal("load skills", zap.Error(err))
	}

	// 3. Create executor
	exec := executor.New(client, reg, executor.Config{
		Model:        "claude-sonnet-4-20250514",
		MaxTokens:    4096,
		SystemPrompt: "You are a helpful AI assistant with specialized skills.",
	}, log)

	// 4. Run a query (auto-matches relevant skills)
	query := "地質データのCSVを分析して、岩盤の強度分布をグラフにしてください"
	result, err := exec.Run(context.Background(), query)
	if err != nil {
		log.Fatal("run", zap.Error(err))
	}

	fmt.Println("=== Response ===")
	fmt.Println(result.Response.Content)
	fmt.Printf("\n=== Used %d skill(s) ===\n", len(result.UsedSkills))
	for _, s := range result.UsedSkills {
		fmt.Printf("  - %s\n", s.Name)
	}
}
