package initx

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/x-mesh/gk/internal/config"
)

// --- YAML 구조체 (Project_Config 전용, Global_Config 필드 배제) ---

type gkConfig struct {
	BaseBranch string       `yaml:"base_branch"`
	Branch     branchCfg    `yaml:"branch"`
	Commit     commitCfg    `yaml:"commit"`
	Preflight  preflightCfg `yaml:"preflight"`
	Ship       *shipCfg     `yaml:"ship,omitempty"`
	AI         aiCfg        `yaml:"ai"`
}

// shipCfg is emitted only when the analyzer found a version-bearing manifest,
// so tag-only projects (e.g. Go) keep a clean ship-less config.
type shipCfg struct {
	VersionFiles []string `yaml:"version_files"`
}

type branchCfg struct {
	Protected []string `yaml:"protected"`
	Patterns  []string `yaml:"patterns"`
}

type commitCfg struct {
	Types            []string `yaml:"types,flow"`
	ScopeRequired    bool     `yaml:"scope_required"`
	MaxSubjectLength int      `yaml:"max_subject_length"`
}

type preflightCfg struct {
	Steps []preflightStepCfg `yaml:"steps"`
}

type preflightStepCfg struct {
	Name    string `yaml:"name"`
	Command string `yaml:"command"`
}

type aiCfg struct {
	Commit aiCommitCfg `yaml:"commit"`
}

type aiCommitCfg struct {
	DenyPaths []string `yaml:"deny_paths"`
	Trailer   bool     `yaml:"trailer"`
	Audit     bool     `yaml:"audit"`
}

// defaultDenyPaths는 ai.commit.deny_paths의 기본 목록이다.
// config.DefaultDenyPaths를 단일 출처로 사용한다 — gk 코드 default와
// gk init 생성 default가 같은 리스트를 가리키게 해야 운영 시 혼란이 없다.
func defaultDenyPaths() []string {
	return config.DefaultDenyPaths()
}

// policiesComment는 commented-out policies 블록이다.
// gk guard init template을 흡수한 opt-in 설정.
const policiesComment = `
# policies: (commented-out, opt-in)
#   secret_patterns:
#     enabled: true
#     mode: git
#     redact: true
`

// configHeader는 .gk.yaml 파일 상단 주석이다.
const configHeader = "# .gk.yaml — gk 프로젝트 설정 (팀 공유)\n"

// GenerateConfig는 분석 결과를 기반으로 .gk.yaml 내용을 생성한다.
// 개인 설정 필드(ai.provider, ai.lang, ui.color 등)는 포함하지 않는다.
func GenerateConfig(result *AnalysisResult) string {
	cfg := buildConfig(result)

	data, err := yaml.Marshal(cfg)
	if err != nil {
		// AnalysisResult에서 생성한 구조체이므로 marshal 실패는 발생하지 않아야 함
		return fmt.Sprintf("# error: %v\n", err)
	}

	var b strings.Builder
	b.WriteString(configHeader)
	b.Write(data)
	b.WriteString(policiesComment)
	return b.String()
}

// buildConfig는 AnalysisResult에서 gkConfig 구조체를 구성한다.
func buildConfig(result *AnalysisResult) gkConfig {
	cfg := gkConfig{
		BaseBranch: baseBranchOrDefault(result),
		Branch: branchCfg{
			Protected: protectedOrDefault(result),
			Patterns:  patternsOrDefault(result),
		},
		Commit: commitCfg{
			Types:            typesOrDefault(result),
			ScopeRequired:    false,
			MaxSubjectLength: 72,
		},
		Preflight: preflightCfg{
			Steps: buildPreflightSteps(result),
		},
		AI: aiCfg{
			Commit: aiCommitCfg{
				DenyPaths: defaultDenyPaths(),
				Trailer:   false,
				Audit:     false,
			},
		},
	}
	if len(result.VersionFiles) > 0 {
		cfg.Ship = &shipCfg{VersionFiles: append([]string(nil), result.VersionFiles...)}
	}
	return cfg
}

func baseBranchOrDefault(r *AnalysisResult) string {
	if r.BaseBranch != "" {
		return r.BaseBranch
	}
	return "main"
}

func protectedOrDefault(r *AnalysisResult) []string {
	if len(r.Protected) > 0 {
		return r.Protected
	}
	return []string{"main", "develop"}
}

func patternsOrDefault(r *AnalysisResult) []string {
	if len(r.BranchPats) > 0 {
		return r.BranchPats
	}
	return []string{DefaultBranchPattern}
}

func typesOrDefault(r *AnalysisResult) []string {
	if len(r.CommitInfo.Types) > 0 {
		return r.CommitInfo.Types
	}
	return append([]string(nil), DefaultCommitTypes...)
}

func buildPreflightSteps(r *AnalysisResult) []preflightStepCfg {
	var steps []preflightStepCfg
	for _, s := range r.Preflight {
		steps = append(steps, preflightStepCfg(s))
	}
	if len(steps) == 0 {
		// 기본 steps
		steps = []preflightStepCfg{
			{Name: "commit-lint", Command: "commit-lint"},
			{Name: "branch-check", Command: "branch-check"},
			{Name: "no-conflict", Command: "no-conflict"},
		}
	}
	return steps
}

// MergeConfig는 기존 .gk.yaml에 빠진 필드만 추가한다.
// 기존 필드의 값은 변경하지 않는다.
// force=true 시 generated를 그대로 반환한다 (완전 덮어쓰기).
func MergeConfig(existing []byte, generated []byte) (merged []byte, added []string, err error) {
	var existDoc yaml.Node
	if err := yaml.Unmarshal(existing, &existDoc); err != nil {
		return nil, nil, fmt.Errorf("gk init: parse existing config: %w", err)
	}
	existMap := ensureDocumentMapping(&existDoc)

	var genDoc yaml.Node
	if err := yaml.Unmarshal(generated, &genDoc); err != nil {
		return nil, nil, fmt.Errorf("gk init: parse generated config: %w", err)
	}
	genMap := documentMapping(&genDoc)
	if genMap == nil {
		genMap = &yaml.Node{Kind: yaml.MappingNode}
	}

	added = mergeMappingNodes(existMap, genMap, "")

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&existDoc); err != nil {
		_ = enc.Close()
		return nil, nil, fmt.Errorf("gk init: marshal merged config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, nil, fmt.Errorf("gk init: marshal merged config: %w", err)
	}
	return out.Bytes(), added, nil
}

func ensureDocumentMapping(doc *yaml.Node) *yaml.Node {
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

func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 && doc.Content[0].Kind == yaml.MappingNode {
		return doc.Content[0]
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

func mergeMappingNodes(dst, src *yaml.Node, prefix string) []string {
	var added []string
	for i := 0; i+1 < len(src.Content); i += 2 {
		srcKey := src.Content[i]
		srcVal := src.Content[i+1]
		if srcKey.Kind != yaml.ScalarNode {
			continue
		}
		key := srcKey.Value
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		dstVal := mappingValue(dst, key)
		if dstVal == nil {
			dst.Content = append(dst.Content, cloneYAMLNode(srcKey), cloneYAMLNode(srcVal))
			added = append(added, path)
			continue
		}
		if dstVal.Kind == yaml.MappingNode && srcVal.Kind == yaml.MappingNode {
			added = append(added, mergeMappingNodes(dstVal, srcVal, path)...)
		}
	}
	return added
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
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

func cloneYAMLNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	if len(n.Content) > 0 {
		cp.Content = make([]*yaml.Node, len(n.Content))
		for i, child := range n.Content {
			cp.Content[i] = cloneYAMLNode(child)
		}
	}
	return &cp
}

// toStringMap은 any를 map[string]any로 변환한다.
// yaml.Unmarshal은 map[string]any를 반환하므로 대부분 성공한다.
func toStringMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	return nil, false
}
