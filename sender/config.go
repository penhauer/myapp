package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

var errExperimentDirNotSet = errors.New("environment variable EXPERIMENT_DIR not set")

func getExperimentDirEnvVar() (*VideoSenderConfig, error) {
	config := &VideoSenderConfig{}
	config.ConfigDir = os.Getenv("EXPERIMENT_DIR")
	if config.ConfigDir == "" {
		fmt.Println("EXPERIMENT_DIR not set. Using current directory ")
		config.ConfigDir = "."
		return config, errExperimentDirNotSet
	}
	return config, nil
}

type ConfigJsonSchema struct {
	VideoSender VideoSenderConfig `json:"video_sender"`
}

// VideoSenderConfig models the "video_sender" object in config.json
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

// readSenderConfigFile loads the "video_sender" key from the config file and decodes it into a struct
// without relying on any external util package.
func readSenderConfigFile() *VideoSenderConfig {
	config, err := getExperimentDirEnvVar()
	if err != nil {
		return config
	}
	configFile := filepath.Join(config.ConfigDir, "config.json")

	b, err := os.ReadFile(configFile)
	if err != nil {
		return config
	}

	wrapper := &ConfigJsonSchema{
		VideoSender: *config,
	}
	if err := json.Unmarshal(b, &wrapper); err != nil {
		return config
	}
	unmarshalled := &wrapper.VideoSender
	return unmarshalled
}

func strptr(s string) *string { return &s }
func intptr(i int) *int       { return &i }

// getConfig returns a VideoSenderConfig populated from config.json, overridden by
// any command-line flags, and filled with defaults for any remaining unset fields.
func getConfig() (*VideoSenderConfig, error) {
	// 1) Load from file (if available). If file not present, start with empty config.
	config := readSenderConfigFile()

	// 2) Define flags. We rely on flag.Visit to know which flags were actually provided.
	offerPtr := flag.String("offer-address", "", "Address that the Offer HTTP server is hosted on.")
	answerPtr := flag.String("answer-address", "", "Address that the Answer HTTP server is hosted on.")
	videoPtr := flag.String("video", "", "video path")
	durationPtr := flag.Int("duration", -1, "streaming duration in seconds")

	// Parse flags (uses os.Args)
	flag.Parse()

	// Which flags were set?
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// 3) Merge: start with file values, override with CLI flags if provided.

	if set["offer-address"] {
		config.OfferAddress = strptr(*offerPtr)
	}
	if set["answer-address"] {
		config.AnswerAddress = strptr(*answerPtr)
	}
	if set["video"] {
		config.Video = strptr(*videoPtr)
	}
	if set["duration"] {
		// durationPtr may be -1 if user supplied -1; we treat presence as authoritative
		config.Duration = intptr(*durationPtr)
	}

	// 4) Apply defaults for any still-nil fields.
	if config.OfferAddress == nil {
		config.OfferAddress = strptr("0.0.0.0:50000")
	}
	if config.AnswerAddress == nil {
		config.AnswerAddress = strptr("127.0.0.1:60000")
	}
	if config.Duration == nil {
		// default streaming duration 0 = infinite
		config.Duration = intptr(60)
	}

	return config, nil
}
