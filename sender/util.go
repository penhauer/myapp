package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/pion/webrtc/v4"
)

func signalCandidate(addr string, candidate *webrtc.ICECandidate) error {
	payload := []byte(candidate.ToJSON().Candidate)

	resp, err := http.Post( //nolint:noctx
		fmt.Sprintf("http://%s/candidate", addr),
		"application/json; charset=utf-8",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}

	return resp.Body.Close()
}

// A HTTP handler that allows the other Pion instance to send us ICE candidates
// This allows us to add ICE candidates faster, we don't have to wait for STUN or TURN
// candidates which may be slower
func setupCandidateHandler(ss *sessionSetup) {
	http.HandleFunc("/candidate", func(res http.ResponseWriter, req *http.Request) { //nolint: revive
		candidate, err := io.ReadAll(req.Body)
		if err != nil {
			panic(err)
		}
		if err := ss.peerConnection.AddICECandidate(
			webrtc.ICECandidateInit{Candidate: string(candidate)},
		); err != nil {
			panic(err)
		}
	})
}

// Sets up a HTTP handler that processes a SessionDescription given to us from the other peer
func setupSDPHandler(ss *sessionSetup) {
	http.HandleFunc("/sdp", func(res http.ResponseWriter, req *http.Request) {
		sdp := webrtc.SessionDescription{}
		if err := json.NewDecoder(req.Body).Decode(&sdp); err != nil {
			panic(err)
		}

		if err := ss.peerConnection.SetRemoteDescription(sdp); err != nil {
			panic(err)
		}

		ss.candidatesMux.Lock()
		defer ss.candidatesMux.Unlock()

		for _, c := range ss.pendingCandidates {
			if onICECandidateErr := signalCandidate(*ss.config.AnswerAddress, c); onICECandidateErr != nil {
				panic(onICECandidateErr)
			}
		}
	})
}

func sendOffer(ss *sessionSetup) {
	// Create an offer to send to the other process
	offer, err := ss.peerConnection.CreateOffer(nil)
	if err != nil {
		panic(err)
	}

	// Sets the LocalDescription, and starts our UDP listeners
	// Note: this will start the gathering of ICE candidates
	if err = ss.peerConnection.SetLocalDescription(offer); err != nil {
		panic(err)
	}

	// Send our offer to the HTTP server listening in the other process
	payload, err := json.Marshal(offer)
	if err != nil {
		panic(err)
	}
	resp, err := http.Post( //nolint:noctx
		fmt.Sprintf("http://%s/sdp", *ss.config.AnswerAddress),
		"application/json; charset=utf-8",
		bytes.NewReader(payload),
	)
	if err != nil {
		panic(err)
	} else if err := resp.Body.Close(); err != nil {
		panic(err)
	}
}

func setupConnectionStateHandler(ss *sessionSetup) {

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	ss.peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
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

		if state == webrtc.PeerConnectionStateConnected {
			fmt.Println("Peer Connection has been established")
			ss.iceConnectedCtxCancel()
		}
	})
}

func setupICECandidateHandler(ss *sessionSetup) {
	// When an ICE candidate is available send to the other Pion instance
	// the other Pion instance will add this candidate by calling AddICECandidate
	ss.peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}

		ss.candidatesMux.Lock()
		defer ss.candidatesMux.Unlock()

		desc := ss.peerConnection.RemoteDescription()
		if desc == nil {
			ss.pendingCandidates = append(ss.pendingCandidates, candidate)
		} else if err := signalCandidate(*ss.config.AnswerAddress, candidate); err != nil {
			panic(err)
		}
	})
}
