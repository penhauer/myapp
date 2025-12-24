package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
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

type VideoReceiverConfig struct {
	AnswerAddress string  `json:"answer_address"`
	OfferAddress  string  `json:"offer_address"`
	OutputDir     string  `json:"output_dir"`
	FrameRate     uint32  `json:"frame_rate"`
	CCA           CCAType `json:"cca"`
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

	return cfg, nil
}
