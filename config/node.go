package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxConfigFileBytes bounds a configuration file: it is read whole and parsed
// into a tree, and no real configuration comes close.
const maxConfigFileBytes = 8 << 20

// readYAMLFile parses path into its document root with ${VAR} expanded and the
// top-level "x-" keys removed. A missing file is nil unless mustExist.
func readYAMLFile(path string, mustExist bool) (*yaml.Node, error) {
	// #nosec G304 -- this path is explicitly selected by the operator as configuration input.
	f, err := os.Open(path) //nolint:gosec // the path is the operator's configuration
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !mustExist {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigFileBytes {
		return nil, fmt.Errorf("the file is larger than %d bytes", maxConfigFileBytes)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if err := expandEnvNodes(&root); err != nil {
		return nil, err
	}
	doc := document(&root)
	if doc == nil {
		return nil, nil
	}
	if doc.Kind != yaml.MappingNode {
		return nil, errors.New("the configuration must be a mapping of sections")
	}
	kept := doc.Content[:0]
	seen := map[string]bool{}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i].Value
		if strings.HasPrefix(key, "x-") {
			continue
		}
		if seen[key] {
			return nil, fmt.Errorf("line %d: section %q appears twice", doc.Content[i].Line, key)
		}
		seen[key] = true
		kept = append(kept, doc.Content[i], doc.Content[i+1])
	}
	doc.Content = kept
	return doc, nil
}

func document(root *yaml.Node) *yaml.Node {
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		return root.Content[0]
	}
	if root.Kind == 0 {
		return nil
	}
	return root
}

// topLevel yields the sections of a mapping root, in file order.
func topLevel(root *yaml.Node) iter.Seq2[string, *yaml.Node] {
	return func(yield func(string, *yaml.Node) bool) {
		if root == nil {
			return
		}
		for i := 0; i+1 < len(root.Content); i += 2 {
			if !yield(root.Content[i].Value, root.Content[i+1]) {
				return
			}
		}
	}
}

// decodeNode decodes value into dst rejecting unknown keys. yaml.v3 enforces
// KnownFields only through a Decoder, so the node is re-encoded first; the
// line numbers of an error are therefore relative to the section.
func decodeNode(name string, value *yaml.Node, dst any) error {
	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		return nil // "section:" with nothing under it
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// envRefRe matches ${VAR} and ${VAR:-default}. The default ends at the first
// "}" and may be empty (${VAR:-}), which is how a variable is declared optional.
var envRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandEnvNodes replaces ${VAR} with the environment variable's value in all
// YAML scalars. A variable that is not set is an error (sorted, once each):
// an empty password or secret that slips through is worse than a refusal to
// start. ${VAR:-default} supplies a value when VAR is unset or empty, and
// ${VAR:-} makes the variable optional. Expansion is done per node (not on
// the raw text) so that values with special YAML characters do not break
// parsing.
func expandEnvNodes(n *yaml.Node) error {
	unset := map[string]bool{}
	expandEnv(n, unset)
	if len(unset) == 0 {
		return nil
	}
	names := make([]string, 0, len(unset))
	for name := range unset {
		names = append(names, name)
	}
	slices.Sort(names)
	return fmt.Errorf("environment variable(s) %s referenced in the configuration are not set; set them or write ${NAME:-default} (${NAME:-} for an empty default)", strings.Join(names, ", "))
}

func expandEnv(n *yaml.Node, unset map[string]bool) {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "${") {
		n.Value = envRefRe.ReplaceAllStringFunc(n.Value, func(ref string) string {
			m := envRefRe.FindStringSubmatch(ref)
			v, ok := os.LookupEnv(m[1])
			if strings.Contains(ref, ":-") {
				if v == "" {
					return m[2]
				}
				return v
			}
			if !ok {
				unset[m[1]] = true
			}
			return v
		})
		if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) == 0 {
			n.Tag = "" // re-resolve: `port: ${DB_PORT}` must become int, not !!str
		}
		return
	}
	for _, c := range n.Content {
		expandEnv(c, unset)
	}
}
