// Package frontmatter parses Dango markdown front matter.
//
// The package supports the front matter formats Dango accepts in repository
// markdown: YAML blocks delimited by --- or ---yaml/---, and TOML blocks
// delimited by +++ or ---toml/---. Parse returns the remaining markdown body
// after a leading front matter block, or the original document when no leading
// front matter is present.
package frontmatter
