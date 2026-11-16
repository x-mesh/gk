package cli

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// mergeGuardInitPolicies replaces or adds the top-level policies: key in
// existing with the policies value parsed from template. Returns false when
// either document is not valid YAML or template has no policies key.
func mergeGuardInitPolicies(existing, template []byte) ([]byte, bool) {
	var existDoc yaml.Node
	if err := yaml.Unmarshal(existing, &existDoc); err != nil {
		return nil, false
	}
	existMap := guardYAMLDocumentMapping(&existDoc)
	if existMap == nil {
		return nil, false
	}

	var tmplDoc yaml.Node
	if err := yaml.Unmarshal(template, &tmplDoc); err != nil {
		return nil, false
	}
	tmplMap := guardYAMLDocumentMapping(&tmplDoc)
	if tmplMap == nil {
		return nil, false
	}
	policies := guardYAMLMappingValue(tmplMap, "policies")
	if policies == nil {
		return nil, false
	}
	guardYAMLSetMappingValue(existMap, "policies", guardYAMLCloneNode(policies))

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&existDoc); err != nil {
		_ = enc.Close()
		return nil, false
	}
	if err := enc.Close(); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

func guardYAMLDocumentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
		return doc.Content[0]
	}
	if doc.Kind == yaml.MappingNode {
		cp := *doc
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{&cp}
		return doc.Content[0]
	}
	if doc.Kind != yaml.DocumentNode {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
		return doc.Content[0]
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
		return doc.Content[0]
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		doc.Content[0] = &yaml.Node{Kind: yaml.MappingNode}
	}
	return doc.Content[0]
}

func guardYAMLMappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func guardYAMLSetMappingValue(m *yaml.Node, key string, val *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
	m.Content = append(m.Content, keyNode, val)
}

func guardYAMLCloneNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	if len(n.Content) > 0 {
		cp.Content = make([]*yaml.Node, len(n.Content))
		for i, child := range n.Content {
			cp.Content[i] = guardYAMLCloneNode(child)
		}
	}
	return &cp
}
