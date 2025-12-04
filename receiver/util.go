package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/pion/webrtc/v4"
)

// signalCandidate posts a single ICE candidate to the remote HTTP server.
func signalCandidate(addr string, candidate *webrtc.ICECandidate) error {
	payload := []byte(candidate.ToJSON().Candidate)

	resp, err := http.Post(fmt.Sprintf("http://%s/candidate", addr), // nolint:noctx
		"application/json; charset=utf-8", bytes.NewReader(payload))
	if err != nil {
		return err
	}

	return resp.Body.Close()
}

// registerSignalingHandlers wires up ICE candidate handling, SDP exchange and connection-state handling.
// It registers HTTP handlers for /candidate and /sdp but does not start the HTTP server.
func registerSignalingHandlers(peerConnection *webrtc.PeerConnection, offerAddr string) {

	var candidatesMux sync.Mutex
	pendingCandidates := make([]*webrtc.ICECandidate, 0)

	// When an ICE candidate is available send to the other Pion instance
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}

		candidatesMux.Lock()
		defer candidatesMux.Unlock()

		desc := peerConnection.RemoteDescription()
		if desc == nil {
			pendingCandidates = append(pendingCandidates, candidate)
		} else if err := signalCandidate(offerAddr, candidate); err != nil {
			panic(err)
		}
	})

	// HTTP handler to receive ICE candidates from remote
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

	// HTTP handler to process remote SDP and send our answer
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
			fmt.Sprintf("http://%s/sdp", offerAddr),
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
			if onICECandidateErr := signalCandidate(offerAddr, c); onICECandidateErr != nil {
				panic(onICECandidateErr)
			}
		}
		candidatesMux.Unlock()
	})

	// Connection state handler
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", state.String())

		if state == webrtc.PeerConnectionStateFailed {
			fmt.Println("Peer Connection has gone to failed exiting")
			os.Exit(0)
		}

		if state == webrtc.PeerConnectionStateClosed {
			fmt.Println("Peer Connection has gone to closed exiting")
			os.Exit(0)
		}
	})
}
