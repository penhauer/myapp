package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type VideoReceiverConfig struct {
	AnswerAddress string `json:"answer_address"`
	OfferAddress  string `json:"offer_address"`
	OutputDir     string `json:"output_dir"`

	// ConfigDir is set to the directory containing the config file.
	// It is not read from JSON.
	ConfigDir string `json:"-"`
}

func readReceiverConfigFile() (*VideoReceiverConfig, error) {
	configPath := flag.String("config", "", "path to video_receiver.json")
	flag.Parse()

	if *configPath == "" {
		return nil, fmt.Errorf("missing -config path argument")
	}

	b, err := os.ReadFile(*configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", *configPath, err)
	}

	cfg := &VideoReceiverConfig{}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", *configPath, err)
	}

	// Set ConfigDir to the directory containing the config file for downstream users.
	cfg.ConfigDir = filepath.Dir(*configPath)
	if cfg.OutputDir == "" {
		cfg.OutputDir = "."
	}

	return cfg, nil
}
