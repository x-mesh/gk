package initx

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// --- YAML 구조체 (Project_Config 전용, Global_Config 필드 배제) ---

type gkConfig struct {
	BaseBranch string       `yaml:"base_branch"`
	Branch     branchCfg    `yaml:"branch"`
	Commit     commitCfg    `yaml:"commit"`
	Preflight  preflightCfg `yaml:"preflight"`
	AI         aiCfg        `yaml:"ai"`
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
var defaultDenyPaths = []string{
	".env",
	".env.*",
	"*.pem",
	"id_rsa*",
	"credentials.json",
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
				DenyPaths: append([]string(nil), defaultDenyPaths...),
				Trailer:   false,
				Audit:     false,
			},
		},
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
		steps = append(steps, preflightStepCfg{Name: s.Name, Command: s.Command})
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
	var existMap map[string]any
	if err := yaml.Unmarshal(existing, &existMap); err != nil {
		return nil, nil, fmt.Errorf("gk init: parse existing config: %w", err)
	}
	if existMap == nil {
		existMap = make(map[string]any)
	}

	var genMap map[string]any
	if err := yaml.Unmarshal(generated, &genMap); err != nil {
		return nil, nil, fmt.Errorf("gk init: parse generated config: %w", err)
	}
	if genMap == nil {
		genMap = make(map[string]any)
	}

	added = deepMerge(existMap, genMap, "")

	out, err := yaml.Marshal(existMap)
	if err != nil {
		return nil, nil, fmt.Errorf("gk init: marshal merged config: %w", err)
	}
	return out, added, nil
}

// deepMerge는 src의 키를 dst에 재귀적으로 병합한다.
// dst에 이미 존재하는 키의 값은 변경하지 않는다.
// 새로 추가된 키의 경로를 반환한다.
func deepMerge(dst, src map[string]any, prefix string) []string {
	var added []string
	for key, srcVal := range src {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		dstVal, exists := dst[key]
		if !exists {
			dst[key] = srcVal
			added = append(added, path)
			continue
		}

		// 양쪽 모두 map이면 재귀 병합
		dstMap, dstIsMap := toStringMap(dstVal)
		srcMap, srcIsMap := toStringMap(srcVal)
		if dstIsMap && srcIsMap {
			added = append(added, deepMerge(dstMap, srcMap, path)...)
			dst[key] = dstMap
			continue
		}
		// dst에 이미 값이 있으면 변경하지 않음
	}
	return added
}

// toStringMap은 any를 map[string]any로 변환한다.
// yaml.Unmarshal은 map[string]any를 반환하므로 대부분 성공한다.
func toStringMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	return nil, false
}
