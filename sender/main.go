package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"myapp/transcoder"
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

type sessionSetup struct {
	offerAddr  *string
	answerAddr *string

	peerConnection *webrtc.PeerConnection
	estimatorChan  chan *cc.BandwidthEstimator

	candidatesMux     sync.Mutex
	pendingCandidates []*webrtc.ICECandidate

	iceConnectedCtx       context.Context
	iceConnectedCtxCancel context.CancelFunc
}

func getTime() string {
	now := time.Now()
	return fmt.Sprintf("%02d:%02d:%03d -- ", now.Minute(), now.Second(), now.Nanosecond()/1e6)
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

// A HTTP handler that allows the other Pion instance to send us ICE candidates
// This allows us to add ICE candidates faster, we don't have to wait for STUN or TURN
// candidates which may be slower
func setupCandidateHandler(ss *sessionSetup) {
	http.HandleFunc("/candidate", func(res http.ResponseWriter, req *http.Request) { //nolint: revive
		candidate, candidateErr := io.ReadAll(req.Body)
		if candidateErr != nil {
			panic(candidateErr)
		}
		if candidateErr := ss.peerConnection.AddICECandidate(
			webrtc.ICECandidateInit{Candidate: string(candidate)},
		); candidateErr != nil {
			panic(candidateErr)
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
		} else if onICECandidateErr := signalCandidate(*ss.answerAddr, candidate); onICECandidateErr != nil {
			panic(onICECandidateErr)
		}
	})
}

//nolint:gocognit, cyclop
func main() {
	ss := &sessionSetup{}

	ss.offerAddr = flag.String("offer-address", ":50000", "Address that the Offer HTTP server is hosted on.")
	ss.answerAddr = flag.String("answer-address", "127.0.0.1:60000", "Address that the Answer HTTP server is hosted on.")
	flag.Parse()

	ss.pendingCandidates = make([]*webrtc.ICECandidate, 0)
	ss.iceConnectedCtx, ss.iceConnectedCtxCancel = context.WithCancel(context.Background())
	ss.estimatorChan, ss.peerConnection = setupPeerConnection()
	defer func() {
		if cErr := ss.peerConnection.Close(); cErr != nil {
			fmt.Printf("cannot close peerConnection: %v\n", cErr)
		}
	}()

	setupICECandidateHandler(ss)
	setupCandidateHandler(ss)
	setupSDPHandler(ss)
	go func() { panic(http.ListenAndServe(*ss.offerAddr, nil)) }()
	handle_video(ss)
	setupConnectionStateHandler(ss)
	sendOffer(ss)
	select {}
}

func setupPeerConnection() (chan *cc.BandwidthEstimator, *webrtc.PeerConnection) {
	interceptorRegistry := &interceptor.Registry{}
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		panic(err)
	}

	var lowBitrate = 1_000_000
	congestionController, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(lowBitrate))
	})
	if err != nil {
		panic(err)
	}

	estimatorChan := make(chan *cc.BandwidthEstimator, 1)
	congestionController.OnNewPeerConnection(func(id string, estimator cc.BandwidthEstimator) { //nolint: revive
		estimatorChan <- &estimator
	})

	interceptorRegistry.Add(congestionController)
	if err = webrtc.ConfigureTWCCHeaderExtensionSender(mediaEngine, interceptorRegistry); err != nil {
		panic(err)
	}

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

	return estimatorChan, peerConnection
}

const (
	videoFileName = "input.y4m"
)

func handle_video(ss *sessionSetup) {
	_, err := os.Stat(videoFileName)
	haveVideoFile := !os.IsNotExist(err)

	if !haveVideoFile {
		panic("Could not find `" + videoFileName + "`")
	}

	tCtx := transcoder.NewTranscodingCtx(
		2560, 1440,
		"hevc_nvenc",
		"input.y4m",
		false,
	)
	fsCtx := &transcoder.FrameServingContext{}
	fsCtx.Init(tCtx)

	trackCodec := webrtc.MimeTypeH265

	// Create a video track
	videoTrack, videoTrackErr := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: trackCodec}, "video", "pion",
	)
	if videoTrackErr != nil {
		panic(videoTrackErr)
	}

	rtpSender, videoTrackErr := ss.peerConnection.AddTrack(videoTrack)
	if videoTrackErr != nil {
		panic(videoTrackErr)
	}

	// Read incoming RTCP packets
	// Before these packets are returned they are processed by interceptors. For things
	// like NACK this needs to be called.
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	estimator := <-ss.estimatorChan

	go func() {
		<-ss.iceConnectedCtx.Done()

		println("herere")
		duration := time.Millisecond * time.Duration(1000/30)
		ticker := time.NewTicker(duration)

		var fcnt int = 0
		defer ticker.Stop()
		for ; true; <-ticker.C {

			fcnt += 1

			targetBitrate := (*estimator).GetTargetBitrate()
			fmt.Println("target bitrate is ", targetBitrate)
			frame := fsCtx.GetNextFrame()

			if err := videoTrack.WriteSample(media.Sample{Data: frame, Duration: 24 * time.Millisecond}); err != nil {
				panic(err)
			}
		}
	}()
}
