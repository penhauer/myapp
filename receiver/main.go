// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// pion-to-pion is an example of two pion instances communicating directly!
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h265writer"
)

func signalCandidate(addr string, candidate *webrtc.ICECandidate) error {
	payload := []byte(candidate.ToJSON().Candidate)
	resp, err := http.Post(fmt.Sprintf("http://%s/candidate", addr), // nolint:noctx
		"application/json; charset=utf-8", bytes.NewReader(payload))
	if err != nil {
		return err
	}

	return resp.Body.Close()
}

// nolint:gocognit, cyclop
func main() {
	offerAddr := flag.String("offer-address", "localhost:50000", "Address that the Offer HTTP server is hosted on.")
	answerAddr := flag.String("answer-address", "0.0.0.0:60000", "Address that the Answer HTTP server is hosted on.")
	flag.Parse()

	var err error
	interceptorRegistry := &interceptor.Registry{}
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		panic(err)
	}

	webrtc.ConfigureCongestionControlFeedback(mediaEngine, interceptorRegistry)

	// if err = webrtc.ConfigureTWCCHeaderExtensionSender(mediaEngine, interceptorRegistry); err != nil {
	// 	panic(err)
	// }

	if err = webrtc.RegisterDefaultInterceptors(mediaEngine, interceptorRegistry); err != nil {
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
		webrtc.WithInterceptorRegistry(interceptorRegistry), webrtc.WithMediaEngine(mediaEngine),
	).NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	defer func() {
		if err := peerConnection.Close(); err != nil {
			fmt.Printf("cannot close peerConnection: %v\n", err)
		}
	}()

	var candidatesMux sync.Mutex
	pendingCandidates := make([]*webrtc.ICECandidate, 0)

	// When an ICE candidate is available send to the other Pion instance
	// the other Pion instance will add this candidate by calling AddICECandidate
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}

		candidatesMux.Lock()
		defer candidatesMux.Unlock()

		desc := peerConnection.RemoteDescription()
		if desc == nil {
			pendingCandidates = append(pendingCandidates, candidate)
		} else if onICECandidateErr := signalCandidate(*offerAddr, candidate); onICECandidateErr != nil {
			panic(onICECandidateErr)
		}
	})

	// A HTTP handler that allows the other Pion instance to send us ICE candidates
	// This allows us to add ICE candidates faster, we don't have to wait for STUN or TURN
	// candidates which may be slower
	http.HandleFunc("/candidate", func(res http.ResponseWriter, req *http.Request) { //nolint: revive
		candidate, candidateErr := io.ReadAll(req.Body)
		if candidateErr != nil {
			panic(candidateErr)
		}
		if candidateErr := peerConnection.AddICECandidate(
			webrtc.ICECandidateInit{Candidate: string(candidate)},
		); candidateErr != nil {
			panic(candidateErr)
		}
	})

	// A HTTP handler that processes a SessionDescription given to us from the other Pion process
	http.HandleFunc("/sdp", func(res http.ResponseWriter, req *http.Request) { // nolint: revive
		sdp := webrtc.SessionDescription{}
		if err := json.NewDecoder(req.Body).Decode(&sdp); err != nil {
			panic(err)
		}

		if err := peerConnection.SetRemoteDescription(sdp); err != nil {
			panic(err)
		}

		// Create an answer to send to the other process
		answer, err := peerConnection.CreateAnswer(nil)
		if err != nil {
			panic(err)
		}

		// Send our answer to the HTTP server listening in the other process
		payload, err := json.Marshal(answer)
		if err != nil {
			panic(err)
		}
		resp, err := http.Post( //nolint:noctx
			fmt.Sprintf("http://%s/sdp", *offerAddr),
			"application/json; charset=utf-8",
			bytes.NewReader(payload),
		) // nolint:noctx
		if err != nil {
			panic(err)
		} else if closeErr := resp.Body.Close(); closeErr != nil {
			panic(closeErr)
		}

		// Sets the LocalDescription, and starts our UDP listeners
		err = peerConnection.SetLocalDescription(answer)
		if err != nil {
			panic(err)
		}

		candidatesMux.Lock()
		for _, c := range pendingCandidates {
			onICECandidateErr := signalCandidate(*offerAddr, c)
			if onICECandidateErr != nil {
				panic(onICECandidateErr)
			}
		}
		candidatesMux.Unlock()
	})

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", state.String())

		if state == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure.
			// It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Peer Connection has gone to failed exiting")
			os.Exit(0)
		}

		if state == webrtc.PeerConnectionStateClosed {
			// PeerConnection was explicitly closed. This usually happens from a DTLS CloseNotify
			fmt.Println("Peer Connection has gone to closed exiting")
			os.Exit(0)
		}
	})

	handle_video(peerConnection)

	// Start HTTP server that accepts requests from the offer process to exchange SDP and Candidates
	// nolint: gosec
	panic(http.ListenAndServe(*answerAddr, nil))
}

func handle_video(peerConnection *webrtc.PeerConnection) {
	outputFile := getExperimentDirEnvVar() + "/received.h265"
	writer, err := h265writer.New(outputFile)
	if err != nil {
		panic(err)
	}

	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) { //nolint: revive
		codec := track.Codec()
		if strings.EqualFold(codec.MimeType, webrtc.MimeTypeH265) {
			fmt.Println("Got H265 track, saving to disk as recevied.265")
			saveToDisk(writer, track)
		}
	})

}

func getExperimentDirEnvVar() string {
	dir := os.Getenv("EXPERIMENT_DIR")
	if dir == "" {
		dir = "."
		// log.Fatalf("required environment variable EXPERIMENT_DIR is not set")
	}
	return dir
}

func saveToDisk(writer media.Writer, track *webrtc.TrackRemote) {
	defer func() {
		if err := writer.Close(); err != nil {
			panic(err)
		}
	}()

	loggerFactory := logging.NewDefaultLoggerFactory()
	fmt.Println(loggerFactory.DefaultLogLevel.Get())
	loggerFactory.DefaultLogLevel.Set(logging.LogLevelInfo)
	logger := loggerFactory.NewLogger("receiver")
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
