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
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/interceptor"
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
	answerAddr := flag.String("answer-address", ":60000", "Address that the Answer HTTP server is hosted on.")
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

		// jj, _ := json.Marshal(candidate)
		// println("Signalling ICE candidate ", string(jj))

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
		println("Received candidate: ", string(candidate))
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
	writer, err := h265writer.New("received.h265")
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

func saveToDisk(writer media.Writer, track *webrtc.TrackRemote) {
	defer func() {
		if err := writer.Close(); err != nil {
			panic(err)
		}
	}()

	// create logger file with current date like 16_12_42 (day_hour_minute)
	now := time.Now()
	filename := fmt.Sprintf("frame_reception_%s.log", now.Format("02_15_04"))
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		// fallback to stderr if file can't be created
		log.Printf("failed to open log file %s: %v", filename, err)
	} else {
		defer f.Close()
	}
	logger := log.New(f, "", log.LstdFlags)

	frameCnt := 0

	// print the start time
	logger.Printf("Start reception at %s (log file: %s)", now.Format(time.StampMilli), filename)

	for {
		p, a, err := track.ReadRTP()

		if err != nil {
			logger.Printf("error reading RTP: %v", err)
			return
		}

		var ecn rtcp.ECN
		if e, hasECN := a["ECN"]; hasECN {
			if ecnT, ok := e.(byte); ok {
				ecn = rtcp.ECN(ecnT)
			}
		}

		if p.Header.Marker {
			frameCnt++
			// print the time for reception of the frame
			logger.Printf("Frame %d received marker at %s", frameCnt, time.Now().Format(time.StampMicro))
		}
		fmt.Printf("Received RTP Packet seq=%d marker=%t ecn=%v marked_count=%d\n",
			p.SequenceNumber, p.Header.Marker, ecn, frameCnt)

		if err := writer.WriteRTP(p); err != nil {
			logger.Printf("error writing RTP to file: %v", err)
			return
		}

	}
}
