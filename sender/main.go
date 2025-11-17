package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"myapp/transcoder"
)

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

		// jj, _ := json.Marshal(candidate)
		// println("Signalling ICE candidate ", string(jj))

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

var videoFileName string

//nolint:gocognit, cyclop
func main() {
	ss := &sessionSetup{}

	ss.offerAddr = flag.String("offer-address", "0.0.0.0:50000", "Address that the Offer HTTP server is hosted on.")
	ss.answerAddr = flag.String("answer-address", "127.0.0.1:60000", "Address that the Answer HTTP server is hosted on.")
	videoPathPtr := flag.String("video", "./input.y4m", "video path")
	flag.Parse()
	videoFileName = *videoPathPtr

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

func handle_video(ss *sessionSetup) {
	_, err := os.Stat(videoFileName)
	haveVideoFile := !os.IsNotExist(err)

	if !haveVideoFile {
		panic("Could not find `" + videoFileName + "`")
	}

	framRate := 30
	ctx := &transcoder.Config{
		Codec:            "hevc_nvenc",
		TargetW:          3840,
		TargetH:          2160,
		InputFile:        videoFileName,
		LoopVideo:        true,
		InitialBitrate:   9_500_000,
		EncoderFrameRate: framRate, // whether we send the frames with this frame rate is another not a business of encoder
	}

	fsCtx := &transcoder.FrameServingContext{}
	fsCtx.Init(ctx)

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
		readRTCP(rtpSender)
	}()

	estimator := <-ss.estimatorChan

	go func() {
		<-ss.iceConnectedCtx.Done()

		duration := time.Millisecond * time.Duration(1000/framRate)
		ticker := time.NewTicker(duration)
		defer ticker.Stop()

		start := time.Now()
		var fcnt int

		for range ticker.C {
			fcnt++

			targetBitrate := (*estimator).GetTargetBitrate()
			frame := fsCtx.GetNextFrame()
			if len(frame) == 0 {
				continue
			}

			elapsed := time.Since(start)
			fmt.Printf("%s elapsed=%v target bitrate=%d fcnt=%d\n", getTime(), elapsed, targetBitrate, fcnt)
			if err := videoTrack.WriteSample(media.Sample{Data: frame, Duration: 24 * time.Millisecond}); err != nil {
				panic(err)
			}
		}
	}()
}

func readRTCP(rtpSender *webrtc.RTPSender) {
	rtcpBuf := make([]byte, 1500)
	for {
		n, _, rtcpErr := rtpSender.Read(rtcpBuf)
		continue
		if rtcpErr == nil {
			// keep a visible log for received RTCP packets
			fmt.Println("RTCP packet received")

			f := &rtcp.CCFeedbackReport{}
			if err := f.Unmarshal(rtcpBuf[:n]); err != nil {
				fmt.Println("failed to unmarshal CCFeedbackReport:", err)
			}

			pkts, uErr := rtcp.Unmarshal(rtcpBuf[:n])
			if uErr != nil {
				fmt.Println("failed to unmarshal RTCP packets:", uErr)
			} else {
				for _, p := range pkts {
					if fb, ok := p.(*rtcp.CCFeedbackReport); ok {
						fmt.Printf("Feedback: %v\n", fb)
					}
				}
			}
		} else {
			fmt.Println("oh oh ", rtcpErr)
		}
	}
}
