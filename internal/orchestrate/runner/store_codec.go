package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

func scanLastRunnerSeq(p string) (int64, bool, error) {
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("orchestrate: JSONRunnerStore read %q: %w", p, err)
	}
	defer f.Close()

	records, err := decodeRunnerRecords(f, p)
	if err != nil {
		return 0, false, err
	}
	var last int64
	hasInit := false
	for _, rec := range records {
		if rec.Seq > last {
			last = rec.Seq
		}
		if rec.Kind == RunnerRecordInit {
			hasInit = true
		}
	}
	return last, hasInit, nil
}

func decodeRunnerRecords(r io.Reader, p string) ([]RunnerRecord, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var (
		out      []RunnerRecord
		pending  RunnerRecord
		pendOK   bool
		pendLine int
		lineNo   int
	)
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if pendOK {
			out = append(out, pending)
		} else if pendLine > 0 {
			return nil, fmt.Errorf("orchestrate: JSONRunnerStore decode %q: corrupt record at line %d", p, pendLine)
		}
		pending = RunnerRecord{}
		if err := json.Unmarshal(line, &pending); err != nil {
			pendOK = false
			pendLine = lineNo
			continue
		}
		pendOK = true
		pendLine = lineNo
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("orchestrate: JSONRunnerStore scan %q: %w", p, err)
	}
	if pendOK {
		out = append(out, pending)
	}
	return out, nil
}
