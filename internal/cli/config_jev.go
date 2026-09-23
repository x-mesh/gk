package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/x-mesh/gk/internal/config"
	"gopkg.in/yaml.v3"
)

func maskJevYAML(cfg *config.Config) ([]byte, error) {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := yaml.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	maskJevMap(value)
	return yaml.Marshal(value)
}

func maskJevMap(value map[string]any) {
	ai, ok := value["ai"].(map[string]any)
	if !ok {
		return
	}
	jev, ok := ai["jev"].(map[string]any)
	if !ok {
		return
	}
	if key, ok := jev["api_key"].(string); ok && key != "" {
		jev["api_key"] = maskSecret(key)
	}
}

func maskJevValue(key string, value any) any {
	if key == "ai.jev.api_key" {
		if secret, ok := value.(string); ok && secret != "" {
			return maskSecret(secret)
		}
	}
	if key == "ai" || key == "ai.jev" {
		if m, ok := value.(map[string]any); ok {
			copyMap := cloneStringMap(m)
			if key == "ai" {
				if jev, ok := copyMap["jev"].(map[string]any); ok {
					maskJevMap(map[string]any{"ai": map[string]any{"jev": jev}})
				}
			} else if secret, ok := copyMap["api_key"].(string); ok && secret != "" {
				copyMap["api_key"] = maskSecret(secret)
			}
			return copyMap
		}
	}
	return value
}

func cloneStringMap(value map[string]any) map[string]any {
	copyMap := make(map[string]any, len(value))
	for key, item := range value {
		copyMap[key] = item
	}
	return copyMap
}

func jevSetupChanges(cmd *cobra.Command, ctx context.Context, cur *config.Config, changes map[string]string) error {
	if v, ok, err := wizardValue(cmd, ctx, "jev-endpoint", "Jev endpoint", "https://api.typesafe.ai/v1/systemone", cur.AI.Jev.Endpoint); err != nil {
		return err
	} else if ok && v != "" {
		changes["ai.jev.endpoint"] = v
	}
	if v, ok, err := wizardValue(cmd, ctx, "jev-model", "Jev 모델", "예: jev-latest", cur.AI.Jev.Model); err != nil {
		return err
	} else if ok && v != "" {
		changes["ai.jev.model"] = v
	}
	if v, ok, err := wizardOptional(cmd, ctx, "jev-api-key", "Jev API 키를 지금 저장할까요?", "환경변수 GK_AI_JEV_API_KEY를 사용할 수도 있습니다", "Jev API 키", "ts-...", ""); err != nil {
		return err
	} else if ok && v != "" {
		changes["ai.jev.api_key"] = v
	}
	if v, ok, err := wizardBool(cmd, ctx, "jev-suggest", "chat 명령 추천에 Jev를 사용할까요?", cur.AI.Jev.Suggest); err != nil {
		return err
	} else if ok {
		changes["ai.jev.suggest"] = fmt.Sprintf("%t", v)
	}
	if v, ok, err := wizardBool(cmd, ctx, "jev-find-rerank", "find 결과 재정렬에 Jev를 사용할까요?", cur.AI.Jev.FindRerank); err != nil {
		return err
	} else if ok {
		changes["ai.jev.find_rerank"] = fmt.Sprintf("%t", v)
	}
	return nil
}

func isJevKey(key string) bool {
	return key == "ai.jev" || strings.HasPrefix(key, "ai.jev.") || key == "ai"
}

func validateConfigWriteScope(key string, local bool) error {
	if local && isJevKey(key) {
		return fmt.Errorf("gk config set --local: Jev settings can only be stored in the user config")
	}
	return nil
}
