package frontmatter

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

type format struct {
	start     string
	end       string
	unmarshal func([]byte, any) error
}

var formats = []format{
	{start: "---", end: "---", unmarshal: yaml.Unmarshal},
	{start: "---yaml", end: "---", unmarshal: yaml.Unmarshal},
	{start: "+++", end: "+++", unmarshal: unmarshalTOML},
	{start: "---toml", end: "---", unmarshal: unmarshalTOML},
}

// Parse decodes leading YAML or TOML front matter from r into v and returns the
// remaining markdown body. If r has no leading front matter block, Parse returns
// the original data and leaves v unchanged.
func Parse(r io.Reader, v any) ([]byte, error) {
	p := &parser{
		reader: bufio.NewReader(r),
		output: bytes.NewBuffer(nil),
	}
	return p.parse(v)
}

type parser struct {
	reader *bufio.Reader
	output *bytes.Buffer
	read   int
	start  int
	end    int
}

func (p *parser) parse(v any) ([]byte, error) {
	f, found, err := p.detect()
	if err != nil {
		return nil, err
	}
	if !found {
		if _, err := p.output.ReadFrom(p.reader); err != nil {
			return nil, err
		}
		return p.output.Bytes(), nil
	}
	if err := p.extract(f, v); err != nil {
		return nil, err
	}
	if _, err := p.output.ReadFrom(p.reader); err != nil {
		return nil, err
	}
	return p.output.Bytes()[p.end:], nil
}

func (p *parser) detect() (format, bool, error) {
	for {
		lineStart := p.read
		line, atEOF, err := p.readLine()
		if err != nil {
			return format{}, false, err
		}
		if line == "" {
			if atEOF {
				return format{}, false, nil
			}
			continue
		}
		for _, f := range formats {
			if f.start == line {
				p.start = p.read
				return f, true, nil
			}
		}
		p.start = lineStart
		return format{}, false, nil
	}
}

func (p *parser) extract(f format, v any) error {
	for {
		lineEnd := p.read
		line, atEOF, err := p.readLine()
		if err != nil {
			return err
		}
		if line == f.end {
			if err := f.unmarshal(p.output.Bytes()[p.start:lineEnd], v); err != nil {
				return err
			}
			p.end = p.read
			return nil
		}
		if atEOF {
			return fmt.Errorf("front matter block starting with %q is not closed", f.start)
		}
	}
}

func (p *parser) readLine() (string, bool, error) {
	line, err := p.reader.ReadBytes('\n')
	atEOF := err == io.EOF
	if err != nil && !atEOF {
		return "", false, err
	}
	p.read += len(line)
	if _, err := p.output.Write(line); err != nil {
		return "", false, err
	}
	return string(bytes.TrimSpace(line)), atEOF, nil
}

func unmarshalTOML(data []byte, v any) error {
	var meta map[string]any
	if _, err := toml.Decode(string(data), &meta); err != nil {
		return err
	}
	yamlData, err := yaml.Marshal(meta)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(yamlData, v)
}
