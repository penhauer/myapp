// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// pion-to-pion is an example of two pion instances communicating directly!
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h265writer"
)

var logger logging.LeveledLogger = setup_logger()

func main() {
	offerAddr := flag.String("offer-address", "localhost:50000", "Address that the Offer HTTP server is hosted on.")
	answerAddr := flag.String("answer-address", "0.0.0.0:60000", "Address that the Answer HTTP server is hosted on.")
	flag.Parse()

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
	registerSignalingHandlers(peerConnection, offerAddr)

	handle_video(peerConnection)

	// Start HTTP server that accepts requests from the offer process to exchange SDP and Candidates
	panic(http.ListenAndServe(*answerAddr, nil))
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
	loggerFactory.DefaultLogLevel.Set(logging.LogLevelDebug)
	logger := loggerFactory.NewLogger("receiver")
	return logger
}

func handle_video(peerConnection *webrtc.PeerConnection) {
	outputFile := filepath.Join(getExperimentDirEnvVar(), "/received.h265")
	writer, err := h265writer.New(outputFile)
	if err != nil {
		panic(err)
	}

	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) { //nolint: revive
		codec := track.Codec()
		if strings.EqualFold(codec.MimeType, webrtc.MimeTypeH265) {
			logger.Info("Got H265 track, saving to disk as recevied.265")
			saveToDisk(writer, track)
		}
	})

}

func getExperimentDirEnvVar() string {
	dir := os.Getenv("EXPERIMENT_DIR")
	if dir == "" {
		logger.Warn("required environment variable EXPERIMENT_DIR is not set")
		dir = "."
	}
	return dir
}

func saveToDisk(writer media.Writer, track *webrtc.TrackRemote) {
	defer func() {
		if err := writer.Close(); err != nil {
			panic(err)
		}
	}()

	logger.Debugf("Start reception at %s\n", time.Now().Format(time.StampMilli))

	frameCnt := 0
	var frameBytes int
	for {
		p, a, err := track.ReadRTP()

		if err != nil {
			logger.Errorf("error reading RTP: %v\n", err)
			return
		}

		packetSize := p.MarshalSize()
		frameBytes += packetSize

		var ecn rtcp.ECN
		if e, hasECN := a["ECN"]; hasECN {
			if ecnT, ok := e.(byte); ok {
				ecn = rtcp.ECN(ecnT)
			}
		}

		if p.Header.Marker {
			frameCnt++
			// print the time for reception of the frame to stdout and its total size
			logger.Debugf("Frame %d received marker at %s, size=%d bytes\n",
				frameCnt, time.Now().Format(time.StampMicro), frameBytes)
			// reset frame byte counter for next frame
			frameBytes = 0
		}

		logger.Debugf("Received RTP Packet seq=%d marker=%t ecn=%v pkt_size=%d marked_count=%d\n",
			p.SequenceNumber, p.Header.Marker, ecn, packetSize, frameCnt)

		if err := writer.WriteRTP(p); err != nil {
			logger.Errorf("error writing RTP to file: %v", err)
			return
		}

	}
}
