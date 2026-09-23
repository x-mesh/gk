package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

var errJevConfig = errors.New("invalid ai.jev configuration")

func loadGlobalJev(path string, defaults JevConfig) (JevConfig, error) {
	cfg := defaults
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok || isNoSuchFile(err) {
			return applyJevEnv(cfg)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || !strings.Contains(string(data), "jev") {
			return applyJevEnv(cfg)
		}
		return cfg, fmt.Errorf("%w: global config: %v", errJevConfig, err)
	}
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"ai.jev.endpoint", &cfg.Endpoint},
		{"ai.jev.api_key", &cfg.APIKey},
		{"ai.jev.model", &cfg.Model},
	} {
		if !v.IsSet(field.key) {
			continue
		}
		raw := v.Get(field.key)
		value, ok := raw.(string)
		if !ok {
			return cfg, fmt.Errorf("%w: %s must be a string", errJevConfig, field.key)
		}
		*field.dst = value
	}
	for _, field := range []struct {
		key string
		dst *bool
	}{
		{"ai.jev.suggest", &cfg.Suggest},
		{"ai.jev.find_rerank", &cfg.FindRerank},
	} {
		if !v.IsSet(field.key) {
			continue
		}
		raw := v.Get(field.key)
		value, ok := raw.(bool)
		if !ok {
			return cfg, fmt.Errorf("%w: %s must be a boolean", errJevConfig, field.key)
		}
		*field.dst = value
	}
	return applyJevEnv(cfg)
}

func applyJevEnv(cfg JevConfig) (JevConfig, error) {
	for _, field := range []struct {
		name string
		dst  *string
	}{
		{"GK_AI_JEV_ENDPOINT", &cfg.Endpoint},
		{"GK_AI_JEV_API_KEY", &cfg.APIKey},
		{"GK_AI_JEV_MODEL", &cfg.Model},
	} {
		if value, ok := os.LookupEnv(field.name); ok {
			*field.dst = value
		}
	}
	for _, field := range []struct {
		name string
		dst  *bool
	}{
		{"GK_AI_JEV_SUGGEST", &cfg.Suggest},
		{"GK_AI_JEV_FIND_RERANK", &cfg.FindRerank},
	} {
		if value, ok := os.LookupEnv(field.name); ok {
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err != nil {
				return cfg, fmt.Errorf("%w: %s must be a boolean", errJevConfig, field.name)
			}
			*field.dst = parsed
		}
	}
	return cfg, nil
}
