package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type CCAType string

const (
	CCAUnknown CCAType = ""
	GCC        CCAType = "gcc"
	SCREAM     CCAType = "scream"
)

func (c CCAType) String() string { return string(c) }

// UnmarshalJSON validates that the input is a known CCA value.
func (c *CCAType) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	s = strings.ToLower(s)
	switch s {
	case "", string(GCC), string(SCREAM):
		*c = CCAType(s)
		return nil
	default:
		return fmt.Errorf("unknown CCA value %q", s)
	}
}

type VideoSenderConfig struct {
	ConfigDir     string
	AnswerAddress *string       `json:"answer_address"`
	OfferAddress  *string       `json:"offer_address"`
	OutputDir     string        `json:"output_dir"`
	Video         *string       `json:"video"`
	Duration      *int          `json:"duration"`
	EncoderConfig EncoderConfig `json:"encoder"`
	CCA           CCAType       `json:"cca"`
	GCC           *GCCConfig    `json:"gcc"`
	Scream        *ScreamConfig `json:"scream"`
}

type EncoderConfig struct {
	FrameRate       *int    `json:"frame_rate"`
	InitialBitrate  *int    `json:"initial_bitrate"`
	AdaptiveBitrate bool    `json:"adaptive_bitrate"`
	LoopVideo       bool    `json:"loop_video"`
	MP4OutputFile   *string `json:"mp4_output_file"`
	HEVCOutputFile  *string `json:"hevc_output_file"`
}

type GCCConfig struct {
	InitialBitrate *int `json:"initial_bitrate"`
}

type ScreamConfig struct {
	InitialBitrate *int `json:"initial_bitrate"`
	IsL4S          bool `json:"isL4s"`
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

	if cfg.OutputDir == "" {
		cfg.OutputDir = "."
	}

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
