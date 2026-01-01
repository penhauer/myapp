// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// pion-to-pion is an example of two pion instances communicating directly!
package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/scream"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

var logger logging.LeveledLogger = setup_logger()

func main() {
	receiverConfig, err := readReceiverConfigFile()
	if err != nil {
		panic(err)
	}

	mediaEngine, interceptorRegistry, err := registerInterceptors(receiverConfig)
	if err != nil {
		panic(err)
	}

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}
	peerConnection, err := webrtc.NewAPI(
		webrtc.WithInterceptorRegistry(interceptorRegistry),
		webrtc.WithMediaEngine(mediaEngine),
	).NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	defer func() {
		if err := peerConnection.Close(); err != nil {
			fmt.Printf("cannot close peerConnection: %v\n", err)
		}
	}()

	// register signaling, ICE and connection-state handlers (moved to util.go)
	registerSignalingHandlers(peerConnection, receiverConfig.OfferAddress)

	handle_video(peerConnection, receiverConfig)

	// Start HTTP server that accepts requests from the offer process to exchange SDP and Candidates
	panic(http.ListenAndServe(receiverConfig.AnswerAddress, nil))
}

func registerInterceptors(config *VideoReceiverConfig) (*webrtc.MediaEngine, *interceptor.Registry, error) {
	interceptorRegistry := &interceptor.Registry{}
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, nil, err
	}

	switch config.CCA {
	case GCC:
		if err := webrtc.ConfigureCongestionControlFeedback(mediaEngine, interceptorRegistry); err != nil {
			return nil, nil, err
		}
	case SCREAM:
		mediaEngine.RegisterFeedback(webrtc.RTCPFeedback{Type: webrtc.TypeRTCPFBACK, Parameter: "ccfb"}, webrtc.RTPCodecTypeVideo)
		mediaEngine.RegisterFeedback(webrtc.RTCPFeedback{Type: webrtc.TypeRTCPFBACK, Parameter: "ccfb"}, webrtc.RTPCodecTypeAudio)
		generator, err := scream.NewReceiverInterceptor()
		if err != nil {
			return nil, nil, err
		}
		interceptorRegistry.Add(generator)
	default:
		panic("Unsupported CCA")
	}

	return mediaEngine, interceptorRegistry, nil
}

func setup_logger() logging.LeveledLogger {
	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel.Set(logging.LogLevelInfo)
	logger := loggerFactory.NewLogger("receiver")
	return logger
}

func handle_video(peerConnection *webrtc.PeerConnection, config *VideoReceiverConfig) {
	if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
		fmt.Println("Failed to create output dir")
		os.Exit(1)
	}

	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) { //nolint: revive
		codec := track.Codec()
		if strings.EqualFold(codec.MimeType, webrtc.MimeTypeH265) {
			logger.Info("Got H265 track, saving to disk as recevied.265")
			saveToDisk(track, config)
		}
	})
}

func saveToDisk(track *webrtc.TrackRemote, config *VideoReceiverConfig) {
	depayloader := NewH265Depayloader(config.FrameRate)
	tracker := NewRtpTracker(logger, config)
	dec, err := NewDecoder(
		&DecoderConfig{
			// Codec: "hevc", // CPU hecv decoder
			Codec:     "hevc_cuvid", // NVIDIA GPU hevc decoder
			FrameRate: config.FrameRate,
			tracker:   tracker,
		},
	)
	if err != nil {
		panic(err)
	}

	dumper, err := NewHEVCWriter(
		filepath.Join(config.OutputDir, "received.hevc"),
		filepath.Join(config.OutputDir, "timecodes.txt"),
		filepath.Join(config.OutputDir, "received_raw.hevc"),
		tracker,
	)
	if err != nil {
		panic(err)
	}

	decodingChan := make(chan *depayloadedUnit, 100)

	defer func() {
		close(decodingChan)
	}()

	go decoderRoutine(dec, decodingChan)

	for {
		p, a, err := track.ReadRTP()
		if err != nil {
			logger.Errorf("error reading RTP: %v\n", err)
			return
		}
		tracker.OnRtp(p)
		// fr.OnRtp(p)

		var ecn rtcp.ECN
		if e, hasECN := a["ECN"]; hasECN {
			if ecnT, ok := e.(byte); ok {
				ecn = rtcp.ECN(ecnT)
			}
		}

		logger.Debugf("Received RTP Packet seq=%d ts=%v marker=%t ecn=%v\n",
			p.SequenceNumber, p.Header.Timestamp, p.Header.Marker, ecn)

		if p.Marker {
			frameNum, _, tsDiff := tracker.GetDiff(p.Timestamp)
			logger.Infof("Last RTP Packet of frame %d was received at %v\n", frameNum, tsDiff.Milliseconds())
		}

		depayloaded, err := depayloader.WriteRTP(p)
		if err != nil {
			if err != ErrNoNALUParsed {
				logger.Warnf("error writing RTP to file: %v", err)
			}
			continue
		}
		decodingChan <- depayloaded
		if err := dumper.PushNALU(depayloaded); err != nil {
			panic(err)
		}
		fmt.Println("len is ", len(decodingChan))
	}
}

func decoderRoutine(dec *Decoder, decodingChan <-chan *depayloadedUnit) {
	for d := range decodingChan {
		dec.FeedUnit(d)
	}
}
