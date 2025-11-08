package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/pion/webrtc/v4"
)

func signalCandidate(addr string, candidate *webrtc.ICECandidate) error {
	payload := []byte(candidate.ToJSON().Candidate)

	x, _ := json.Marshal(candidate.ToJSON())
	println(getTime(), "signalling candidate ", string(x))

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
		candidate, candidateErr := io.ReadAll(req.Body)
		if candidateErr != nil {
			panic(candidateErr)
		}
		println("Received candidate", string(candidate))
		if candidateErr := ss.peerConnection.AddICECandidate(
			webrtc.ICECandidateInit{Candidate: string(candidate)},
		); candidateErr != nil {
			panic(candidateErr)
		}
	})
}

// Sets up a HTTP handler that processes a SessionDescription given to us from the other peer
func setupSDPHandler(ss *sessionSetup) {
	http.HandleFunc("/sdp", func(res http.ResponseWriter, req *http.Request) {
		sdp := webrtc.SessionDescription{}
		if sdpErr := json.NewDecoder(req.Body).Decode(&sdp); sdpErr != nil {
			panic(sdpErr)
		}

		println(getTime(), "Receieved SDP ")

		if sdpErr := ss.peerConnection.SetRemoteDescription(sdp); sdpErr != nil {
			panic(sdpErr)
		}

		ss.candidatesMux.Lock()
		defer ss.candidatesMux.Unlock()

		for _, c := range ss.pendingCandidates {
			if onICECandidateErr := signalCandidate(*ss.answerAddr, c); onICECandidateErr != nil {
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
		fmt.Sprintf("http://%s/sdp", *ss.answerAddr),
		"application/json; charset=utf-8",
		bytes.NewReader(payload),
	)
	if err != nil {
		panic(err)
	} else if err := resp.Body.Close(); err != nil {
		panic(err)
	}
}
