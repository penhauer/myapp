// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// pion-to-pion is an example of two pion instances communicating directly!
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/pion/interceptor"
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

	mediaEngine, interceptorRegistry, err := registerInterceptors()
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

func registerInterceptors() (*webrtc.MediaEngine, *interceptor.Registry, error) {
	interceptorRegistry := &interceptor.Registry{}
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, nil, err
	}

	if err := webrtc.ConfigureCongestionControlFeedback(mediaEngine, interceptorRegistry); err != nil {
		return nil, nil, err
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
		panic(err)
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
	dec := &Decoder{
		config: &DecoderConfig{
			Codec:     "hevc_cuvid",
			FrameRate: config.FrameRate,
		},
	}

	fr := NewFrameReceiver(logger, config)
	dec.config.callback = fr.OnFrameDecoded
	dec.setupDecoder()

	// Create a channel to send depayloaded bytes to the decoder
	decodingChan := make(chan *depayloadedUnit, 100)
	defer close(decodingChan)

	// Start decoder goroutine
	go decoderRoutine(dec, decodingChan)

	for {
		p, a, err := track.ReadRTP()
		if err != nil {
			logger.Errorf("error reading RTP: %v\n", err)
			return
		}
		fr.OnRtp(p)

		var ecn rtcp.ECN
		if e, hasECN := a["ECN"]; hasECN {
			if ecnT, ok := e.(byte); ok {
				ecn = rtcp.ECN(ecnT)
			}
		}

		logger.Debugf("Received RTP Packet seq=%d ts=%v marker=%t ecn=%v\n",
			p.SequenceNumber, p.Header.Timestamp, p.Header.Marker, ecn)

		depayloaded, err := depayloader.WriteRTP(p)
		if err != nil {
			if err != ErrNoNALUParsed {
				logger.Warnf("error writing RTP to file: %v", err)
			}
			continue
		}
		decodingChan <- depayloaded
	}
}

// decoderRoutine runs the decoder in a separate goroutine
func decoderRoutine(dec *Decoder, decodingChan <-chan *depayloadedUnit) {
	for depayloaded := range decodingChan {
		dec.FeedUnit(depayloaded)
	}
}
