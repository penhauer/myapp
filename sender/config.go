package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type VideoSenderConfig struct {
	ConfigDir     string
	AnswerAddress *string       `json:"answer_address"`
	OfferAddress  *string       `json:"offer_address"`
	Video         *string       `json:"video"`
	Duration      *int          `json:"duration"`
	EncoderConfig EncoderConfig `json:"encoder"`
}

type EncoderConfig struct {
	FrameRate      *int `json:"frame_rate"`
	InitialBitrate *int `json:"initial_bitrate"`
	LoopVideo      bool `json:"loop_video"`
}

func readSenderConfigFile() (*VideoSenderConfig, error) {
	configPath := flag.String("config", "", "path to video_sender.json")
	flag.Parse()

	if *configPath == "" {
		return nil, fmt.Errorf("missing -config path argument")
	}

	b, err := os.ReadFile(*configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", *configPath, err)
	}

	cfg := &VideoSenderConfig{}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", *configPath, err)
	}

	// Set ConfigDir to the directory containing the config file for any
	// downstream users that might expect it.
	cfg.ConfigDir = filepath.Dir(*configPath)

	return cfg, nil
}

func intptr(i int) *int { return &i }

// getConfig returns a VideoSenderConfig populated from config.json, overridden by
// any command-line flags, and filled with defaults for any remaining unset fields.
func getConfig() (*VideoSenderConfig, error) {
	// Load configuration from the single JSON filepath argument.
	config, err := readSenderConfigFile()
	if err != nil {
		return nil, err
	}
	return config, nil
}
