package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/fpt/klein-cli/internal/researcher/model"
)

type Config struct {
	Sources []model.Source `json:"sources"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
		cfg, err = parseYAMLConfig(string(b))
		if err != nil {
			return Config{}, err
		}
	} else {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return Config{}, err
		}
	}
	if len(cfg.Sources) == 0 {
		return Config{}, fmt.Errorf("config has no sources")
	}
	for i, src := range cfg.Sources {
		if src.Name == "" {
			return Config{}, fmt.Errorf("source %d has no name", i)
		}
		if src.URL == "" {
			return Config{}, fmt.Errorf("source %q has no url", src.Name)
		}
		cfg.Sources[i] = model.NormalizeSource(src)
	}
	return cfg, nil
}

func parseYAMLConfig(content string) (Config, error) {
	var cfg Config
	var current *model.Source
	inSources := false

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "sources:" {
			inSources = true
			continue
		}
		if !inSources {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			if current != nil {
				cfg.Sources = append(cfg.Sources, *current)
			}
			current = &model.Source{}
			line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if line == "" {
				continue
			}
		}
		if current == nil {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = trimYAMLScalar(value)
		switch strings.TrimSpace(key) {
		case "name":
			current.Name = value
		case "type":
			current.Type = value
		case "url":
			current.URL = value
		case "intake":
			current.Intake = value
		case "role":
			current.Role = value
		case "trust_tier", "trust-tier", "tier":
			current.TrustTier = value
		case "weight":
			weight, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return Config{}, fmt.Errorf("invalid weight %q", value)
			}
			current.Weight = weight
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, err
	}
	if current != nil {
		cfg.Sources = append(cfg.Sources, *current)
	}
	return cfg, nil
}

func trimYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"'")
	return value
}
