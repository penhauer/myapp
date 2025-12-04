package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/interceptor/pkg/scream"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"myapp/transcoder"
)

type bitrateEstimator func(SSRC uint32) int

type sessionSetup struct {
	logger logging.LeveledLogger

	config *VideoSenderConfig

	iceConnectedCtx       context.Context
	iceConnectedCtxCancel context.CancelFunc

	candidatesMux     sync.Mutex
	pendingCandidates []*webrtc.ICECandidate

	mediaEngine         *webrtc.MediaEngine
	interceptorRegistry *interceptor.Registry
	peerConnection      *webrtc.PeerConnection

	estimatorChan chan bitrateEstimator
	estimator     bitrateEstimator

	fsCtx *transcoder.FrameServingContext
}

func setup_logger() logging.LeveledLogger {
	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel.Set(logging.LogLevelDebug)
	logger := loggerFactory.NewLogger("sender")
	return logger
}

//nolint:gocognit, cyclop
func main() {
	var err error
	ss := &sessionSetup{}

	ss.config, err = getConfig()
	if err != nil {
		panic(err)
	}

	checkVideoFileExists(*ss.config.Video)

	ss.logger = setup_logger()

	ss.pendingCandidates = make([]*webrtc.ICECandidate, 0)
	ss.iceConnectedCtx, ss.iceConnectedCtxCancel = context.WithCancel(context.Background())
	setupPeerConnection(ss)
	defer func() {
		if cErr := ss.peerConnection.Close(); cErr != nil {
			fmt.Printf("cannot close peerConnection: %v\n", cErr)
		}
	}()

	setupICECandidateHandler(ss)
	setupCandidateHandler(ss)
	setupSDPHandler(ss)
	go func() { panic(http.ListenAndServe(*ss.config.OfferAddress, nil)) }()
	handle_video(ss)
	setupConnectionStateHandler(ss)
	sendOffer(ss)
	select {}
}

func checkVideoFileExists(videoFileName string) {
	_, err := os.Stat(videoFileName)
	videoFileExists := !os.IsNotExist(err)

	if !videoFileExists {
		panic(fmt.Sprintf("Could not find `%s`", videoFileName))
	}
}

func setupPeerConnection(ss *sessionSetup) error {
	err := registerInterceptors(ss)
	if err != nil {
		return err
	}

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	ss.peerConnection, err = webrtc.NewAPI(
		webrtc.WithInterceptorRegistry(ss.interceptorRegistry), webrtc.WithMediaEngine(ss.mediaEngine),
	).NewPeerConnection(config)
	if err != nil {
		return err
	}

	return nil
}

func registerInterceptors(s *sessionSetup) error {
	s.interceptorRegistry = &interceptor.Registry{}
	s.mediaEngine = &webrtc.MediaEngine{}

	if err := s.mediaEngine.RegisterDefaultCodecs(); err != nil {
		return err
	}

	check_encoder_config(s)
	initialBitrate := *s.config.EncoderConfig.InitialBitrate
	s.estimatorChan = make(chan bitrateEstimator, 1)
	switch s.config.CCA {
	case GCC:
		ccInterceptor, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
			return gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(initialBitrate))
		})
		if err != nil {
			return err
		}

		ccInterceptor.OnNewPeerConnection(func(id string, estimator cc.BandwidthEstimator) { //nolint: revive
			f := func(ssrc uint32) int {
				return estimator.GetTargetBitrate()
			}
			s.estimatorChan <- f
		})
		s.interceptorRegistry.Add(ccInterceptor)
	case SCREAM:
		screamInterceptor, err := scream.NewSenderInterceptor(scream.InitialBitrate(float64(initialBitrate)))
		if err != nil {
			return err
		}
		screamInterceptor.OnNewPeerConnection(func(id string, estimator scream.BandwidthEstimator) { //nolint: revive
			f := func(ssrc uint32) int {
				bitrate, err := estimator.GetTargetBitrate(ssrc)
				if err != nil {
					panic(err)
				}
				return bitrate
			}
			s.estimatorChan <- f
		})
		s.interceptorRegistry.Add(screamInterceptor)
	}

	return nil
}

func check_encoder_config(ss *sessionSetup) {
	initialBitrate := &ss.config.EncoderConfig.InitialBitrate
	if *initialBitrate == nil {
		*initialBitrate = intptr(2_500_000)
		ss.logger.Warn("Initial bitrate not set. Using 2500000 as the default value")
	}

	frameRate := &ss.config.EncoderConfig.FrameRate
	if *frameRate == nil {
		*frameRate = intptr(30)
		ss.logger.Warn("Framerate not set. Using 30 as the default value")
	}
}

func handle_video(ss *sessionSetup) {
	// Create a video track
	trackCodec := webrtc.MimeTypeH265
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: trackCodec}, "video", "pion",
	)
	if err != nil {
		panic(err)
	}

	rtpSender, err := ss.peerConnection.AddTrack(videoTrack)
	ssrc := uint32(rtpSender.GetParameters().Encodings[0].SSRC)

	check_encoder_config(ss)
	configure_transcoder(ss, ssrc)

	if err != nil {
		panic(err)
	}

	go func() {
		// Read incoming RTCP packets
		// Before these packets are returned they are processed by interceptors. For things
		// like NACK this needs to be called.
		readRTCP(rtpSender)
	}()

	go func() {
		<-ss.iceConnectedCtx.Done()
		stream_video(ss, videoTrack, ssrc)
	}()
}

func configure_transcoder(ss *sessionSetup, ssrc uint32) {
	ec := ss.config.EncoderConfig
	ss.estimator = <-ss.estimatorChan
	keyFrameCallback := func() int {
		bitrate := ss.estimator(ssrc)
		ss.logger.Infof("Encoder's bitrate set to %v at %v\n", bitrate, time.Now())
		return bitrate
	}
	// keyFrameCallback = nil

	ctx := &transcoder.Config{
		Codec:            "hevc_nvenc",
		TargetW:          3840,
		TargetH:          2160,
		InputFile:        *ss.config.Video,
		LoopVideo:        ss.config.EncoderConfig.LoopVideo,
		InitialBitrate:   *ec.InitialBitrate,
		GoPSize:          30,
		EncoderFrameRate: *ec.FrameRate, // whether we send the frames with this frame rate is not a business of the encoder
		KeyFrameCallback: keyFrameCallback,
		OutputPath:       filepath.Join(ss.config.ConfigDir, "raw.x265"),
		RawOutputPath:    filepath.Join(ss.config.ConfigDir, "output.mp4"),
	}

	ss.fsCtx = &transcoder.FrameServingContext{}
	ss.fsCtx.Init(ctx)
}

func stream_video(ss *sessionSetup, videoTrack *webrtc.TrackLocalStaticSample, ssrc uint32) {
	ec := ss.config.EncoderConfig
	streamingDuration := *ss.config.Duration
	tickDuration := time.Millisecond * time.Duration(1000/(*ec.FrameRate))
	ticker := time.NewTicker(tickDuration)
	defer ticker.Stop()

	start := time.Now()
	var fcnt int
	var done <-chan time.Time
	var sizeSum int = 0
	var lastSetBitrate int = 1
	if streamingDuration > 0 {
		done = time.After(time.Duration(streamingDuration) * time.Second)
	}

	for {
		select {
		case <-ticker.C:
			fcnt++

			targetBitrate := ss.estimator(ssrc)
			frame := ss.fsCtx.GetNextFrame()

			sizeSum += len(frame.Data)
			if frame.KeyFrame {
				diff := 100.0 * (sizeSum*8.0 - lastSetBitrate) / lastSetBitrate
				ss.logger.Infof("Keyframe seen. sizeSumBits=%d lastSetBitrate=%d diff=%v\n", sizeSum*8, lastSetBitrate, diff)
				lastSetBitrate = frame.Bitrate
				sizeSum = 0
			}

			if len(frame.Data) == 0 {
				continue
			}

			elapsed := time.Since(start)
			ss.logger.Infof("elapsed=%v target bitrate=%d frame_bitrate= %d frame_size_bits=%d fcnt=%d \n",
				elapsed,
				targetBitrate,
				frame.Bitrate,
				len(frame.Data)*8,
				fcnt,
			)
			if err := videoTrack.WriteSample(media.Sample{Data: frame.Data, Duration: tickDuration}); err != nil {
				panic(err)
			}
		case <-done:
			ss.logger.Infof("treaming duration of %d seconds reached, exiting\n", streamingDuration)
			os.Exit(0)
		}
	}
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
