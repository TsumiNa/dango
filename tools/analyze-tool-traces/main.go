package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	jsonOut := flag.String("json", "", "optional path to write a JSON sidecar with the full report")
	mdOut := flag.String("out", "", "optional path to write the markdown report (default: stdout)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: analyze-tool-traces [flags] <stream_events.jsonl>\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	source := flag.Arg(0)

	f, err := os.Open(source)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", source, err)
		os.Exit(1)
	}
	defer f.Close()

	rep, err := Analyze(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analyze %s: %v\n", source, err)
		os.Exit(1)
	}

	md := FormatMarkdown(rep, source)
	if err := writeOrStdout(*mdOut, []byte(md)); err != nil {
		fmt.Fprintf(os.Stderr, "write markdown: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut != "" {
		buf, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal JSON: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*jsonOut, append(buf, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write JSON: %v\n", err)
			os.Exit(1)
		}
	}
}

func writeOrStdout(path string, payload []byte) error {
	if path == "" {
		_, err := os.Stdout.Write(payload)
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}
