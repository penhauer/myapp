package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"myapp/transcoder"
)

type sessionSetup struct {
	answerAddr *string

	peerConnection *webrtc.PeerConnection
	estimatorChan  chan *cc.BandwidthEstimator

	candidatesMux     sync.Mutex
	pendingCandidates []*webrtc.ICECandidate

	iceConnectedCtx       context.Context
	iceConnectedCtxCancel context.CancelFunc
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

//nolint:gocognit, cyclop
func main() {
	ss := &sessionSetup{}

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

	var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("upgrade:", err)
			return
		}
		log.Println("Browser connected to signaling WS")

		offer, err := ss.peerConnection.CreateOffer(nil)
		if err != nil {
			panic(err)
		}
		ss.peerConnection.SetLocalDescription(offer)
		conn.WriteJSON(map[string]any{"type": "offer", "sdp": offer})

		ss.peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
			if candidate == nil {
				return
			}
			ss.candidatesMux.Lock()
			defer ss.candidatesMux.Unlock()

			desc := ss.peerConnection.RemoteDescription()
			if desc == nil {
				ss.pendingCandidates = append(ss.pendingCandidates, candidate)
			} else {
				conn.WriteJSON(map[string]any{"type": "ice", "candidate": candidate.ToJSON()})
			}
		})

		// Receive messages (answer + ICE)
		for {
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				log.Println("ws read:", err)
				return
			}
			switch msg["type"] {
			case "answer":
				b, _ := json.Marshal(msg["sdp"])
				var s webrtc.SessionDescription
				_ = json.Unmarshal(b, &s)
				if err := ss.peerConnection.SetRemoteDescription(s); err != nil {
					panic(err)
				}
				ss.candidatesMux.Lock()
				defer ss.candidatesMux.Unlock()

				for _, c := range ss.pendingCandidates {
					conn.WriteJSON(map[string]any{"type": "ice", "candidate": c.ToJSON()})
				}
			case "ice":
				b, _ := json.Marshal(msg["candidate"])
				var candidate webrtc.ICECandidateInit
				_ = json.Unmarshal(b, &candidate)
				if err := ss.peerConnection.AddICECandidate(candidate); err != nil {
					panic(err)
				}
			}
		}
	})
	go func() { panic(http.ListenAndServe("localhost:8080", nil)) }()

	setupConnectionStateHandler(ss)
	handle_video(ss)
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
		"h264_nvenc",
		"input.y4m",
		true,
	)
	fsCtx := &transcoder.FrameServingContext{}
	fsCtx.Init(tCtx)

	trackCodec := webrtc.MimeTypeH264

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
