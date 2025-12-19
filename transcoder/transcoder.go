package transcoder

/*
#include <libavutil/avutil.h>
#include <libavutil/hwcontext.h>
#include <libavutil/error.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avstring.h>
#include <libavformat/avformat.h>
#include <libavutil/channel_layout.h>
#include <libavutil/dict.h>
#include <libavutil/pixdesc.h>
#include <libavutil/opt.h>
#include <libavutil/frame.h>
#include <libavutil/parseutils.h>
#include <libavutil/common.h>
#include <libavutil/eval.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>
#include <libswscale/swscale.h>


#cgo pkg-config: libavcodec libavutil libavformat libavcodec libavfilter libswscale
*/
import (
	"C"
)
import (
	"fmt"
	"log"
	"os"
	"unsafe"
)

type KeyFrameCallbackType func() int

var LOOKAHEAD = 2

type Config struct {
	Codec            string
	TargetW          int
	TargetH          int
	InputFile        string
	InitialBitrate   int
	KeyFrameCallback KeyFrameCallbackType
	GoPSize          int
	LoopVideo        bool
	EncoderFrameRate int
	OutputPath       string
	RawOutputPath    string
}

type EncoderFrame struct {
	KeyFrame bool
	Data     []byte
	Bitrate  int
}

type transcodingCtx struct {

	// Conig
	config *Config

	//params
	srcW int
	srcH int

	// todo: remove later
	rawOutputFile *os.File

	// decoding
	decFmt    *FormatContext
	decStream *Stream
	decCodec  *CodecContext
	decPkt    *Packet
	decFrame  *Frame
	srcFilter *FilterContext

	// encoding
	encFmt     *FormatContext
	encStream  *Stream
	encCodec   *CodecContext
	encIO      *IOContext
	encPkt     *Packet
	encFrame   *Frame
	sinkFilter *FilterContext

	// frames
	frameChan    chan *Frame
	frameCnt     int
	nextKeyFrame int
	frameBitrate int

	realFrameCnt  int
	droppedFrames int

	//sws
	swsCtx *C.struct_SwsContext
	swNV12 *Frame

	// hw
	hwframeCtx *C.AVBufferRef
}

func setupDecoder(ctx *transcodingCtx) error {
	var err error
	ctx.decFmt, err = NewContextForInput()
	if err != nil {
		return fmt.Errorf("failed to open input context: %v", err)
	}

	options := NewDictionary()
	defer options.Free()

	if err := ctx.decFmt.OpenInput(ctx.config.InputFile, nil, options); err != nil {
		return fmt.Errorf("failed to open input file: %v", err)
	}

	if err := ctx.decFmt.FindStreamInfo(nil); err != nil {
		return fmt.Errorf("could not find stream info: %v", err)
	}

	ctx.decFmt.Dump(0, ctx.config.InputFile, false)
	ctx.decStream = ctx.decFmt.Streams()[0]

	codecID := int(ctx.decStream.CAVStream.codecpar.codec_id)
	codec := NewCodecFromC(unsafe.Pointer(C.avcodec_find_decoder((C.enum_AVCodecID)(codecID))))

	if ctx.decCodec, err = NewContextWithCodec(codec); err != nil {
		return fmt.Errorf("failed to create codec context: %v", err)
	}

	C.avcodec_parameters_to_context(ctx.decCodec.CAVCodecContext, ctx.decStream.CAVStream.codecpar)

	if err = ctx.decCodec.OpenWithCodec(codec, options); err != nil {
		log.Fatalf("Failed to open decoder: %v\n", err)
		return err
	}

	if ctx.decFrame, err = NewFrame(); err != nil {
		log.Fatalf("", err)
		return fmt.Errorf("failed to create a frame: %w", err)
	}

	ctx.srcW = int(ctx.decCodec.CAVCodecContext.width)
	ctx.srcH = int(ctx.decCodec.CAVCodecContext.height)

	ctx.swNV12, _ = NewFrame()
	ctx.swNV12.CAVFrame.format = C.AV_PIX_FMT_NV12
	ctx.swNV12.CAVFrame.width = C.int(ctx.srcW)
	ctx.swNV12.CAVFrame.height = C.int(ctx.srcH)

	if int(C.av_frame_get_buffer(ctx.swNV12.CAVFrame, 0)) < 0 {
		return fmt.Errorf("failed to allocate buffer for FMT_NV12 frame")
	}

	return nil
}

func setupEncoder(ctx *transcodingCtx) error {
	var err error

	codec := FindEncoderByName(ctx.config.Codec)
	if codec == nil {
		log.Fatalf("Could not find encoder %s", ctx.config.Codec)
		return fmt.Errorf("encoder not found")
	}

	// --- 2) Build options (NVENC-friendly; drop libvpx ones)
	params := map[string]string{
		// NVENC options
		"preset":         "p5",  // p1..p7 (p5 good balance)
		"tune":           "ull", // ultra-low-latency
		"rc":             "cbr", // cbr|vbr|constqp
		"zerolatency":    "1",
		"repeat_headers": "1",
		"aud":            "1",

		// For 10-bit: use P010 and consider profile=1
		// "profile":   "1",
	}

	// --- 3) Create encoder context
	if ctx.encCodec, err = NewContextWithCodec(codec); err != nil {
		return fmt.Errorf("NewContextWithCodec: %w", err)
	}

	// --- 4) Create & attach CUDA hw device (auto-upload from SW frames)
	var hwDev *C.AVBufferRef
	if C.av_hwdevice_ctx_create(&hwDev, C.AV_HWDEVICE_TYPE_CUDA, nil, nil, 0) < 0 || hwDev == nil {
		return fmt.Errorf("failed to create CUDA hw device")
	}
	println("hwDev: ", hwDev)
	ctx.encCodec.CAVCodecContext.hw_device_ctx = C.av_buffer_ref(hwDev)

	println("ctx.encCodec.CAVCodecContext.hw_device_ctx: ", ctx.encCodec.CAVCodecContext.hw_device_ctx)

	// Create hw frames ctx (GPU surface pool)
	hwframeCtx := C.av_hwframe_ctx_alloc(hwDev)
	if unsafe.Pointer(hwframeCtx) == nil {
		return fmt.Errorf("av_hwframe_ctx_alloc failed")
	}
	println("hwFrameCtx: ", hwframeCtx)

	framesCtx := (*C.AVHWFramesContext)(unsafe.Pointer(hwframeCtx.data))
	framesCtx.format = C.AV_PIX_FMT_CUDA
	framesCtx.sw_format = C.AV_PIX_FMT_NV12
	framesCtx.width = C.int(ctx.srcW)
	framesCtx.height = C.int(ctx.srcH)
	if int(C.av_hwframe_ctx_init(hwframeCtx)) < 0 {
		return fmt.Errorf("could not initiate hwframe context")
	}
	ctx.hwframeCtx = hwframeCtx
	ctx.encCodec.CAVCodecContext.hw_frames_ctx = C.av_buffer_ref(hwframeCtx)
	ctx.swsCtx = C.sws_getContext(C.int(ctx.srcW), C.int(ctx.srcH), ctx.decCodec.CAVCodecContext.pix_fmt,
		C.int(ctx.srcW), C.int(ctx.srcH), framesCtx.sw_format,
		C.SWS_BILINEAR, nil, nil, nil)

	// --- 5) Basic fields / time base / fps
	ctx.encCodec.CAVCodecContext.width = C.int(ctx.config.TargetW)
	ctx.encCodec.CAVCodecContext.height = C.int(ctx.config.TargetH)
	ctx.encCodec.SetTimeBase(NewRational(1, ctx.config.EncoderFrameRate))
	ctx.encCodec.SetFrameRate(NewRational(ctx.config.EncoderFrameRate, 1))

	// Low-latency: no B-frames
	ctx.encCodec.CAVCodecContext.max_b_frames = 0
	ctx.encCodec.CAVCodecContext.gop_size = C.int(ctx.config.GoPSize)
	ctx.encCodec.CAVCodecContext.bit_rate = C.long(ctx.config.InitialBitrate)
	ctx.frameBitrate = ctx.config.InitialBitrate
	ctx.encCodec.CAVCodecContext.pix_fmt = C.AV_PIX_FMT_CUDA

	// --- 6) Apply encoder options

	options := NewDictionary()
	defer options.Free()
	// Only pass actual encoder private opts through the OptionAccessor/Dictionary
	for k, v := range params {
		if err = options.Set(k, v); err != nil {
			fmt.Println(err.Error())
		}
	}

	if err := ctx.encCodec.OpenWithCodec(codec, options); err != nil {
		return fmt.Errorf("OpenWithCodec: %w", err)
	}

	// --- 7) Muxer: IVF with AV1 (works fine)
	ctx.encFmt = &FormatContext{}
	if C.avformat_alloc_output_context2(&ctx.encFmt.CAVFormatContext, nil, C.CString("mp4"), C.CString(ctx.config.OutputPath)) < 0 {
		return fmt.Errorf("alloc_output_context2 failed")
	}
	if C.avio_open(&ctx.encFmt.CAVFormatContext.pb, C.CString(ctx.config.OutputPath), C.AVIO_FLAG_WRITE) < 0 {
		return fmt.Errorf("avio_open failed")
	}

	// New stream
	if ctx.encStream, err = ctx.encFmt.NewStreamWithCodec(codec); err != nil {
		return fmt.Errorf("NewStreamWithCodec: %w", err)
	}

	// Copy parameters and align time bases
	C.avcodec_parameters_from_context(ctx.encStream.CAVStream.codecpar, ctx.encCodec.CAVCodecContext)
	ctx.encStream.TimeBase().SetDenominator(ctx.encCodec.TimeBase().Denominator())
	ctx.encStream.TimeBase().SetNumerator(ctx.encCodec.TimeBase().Numerator())
	ctx.encStream.SetAverageFrameRate(ctx.encCodec.FrameRate())

	C.av_dump_format(ctx.encFmt.CAVFormatContext, 0, C.CString(ctx.config.OutputPath), 1)

	if C.avformat_write_header(ctx.encFmt.CAVFormatContext, nil) < 0 {
		return fmt.Errorf("avformat_write_header failed")
	}

	return nil
}

// returns false if EOF reached otherwise true
func decodeStream(ctx *transcodingCtx) bool {
	var err error
	if ctx.decPkt, err = NewPacket(); err != nil {
		println("unable to allocate new packet for decoding")
	}

	reading, err := ctx.decFmt.ReadFrame(ctx.decPkt)
	if err != nil {
		log.Fatalf("Failed to read packet: %v\n", err)
	}
	if !reading {
		return ctx.handleReadingStopped()
	}
	defer ctx.decPkt.Unref()

	if ctx.decPkt.StreamIndex() != ctx.decStream.Index() {
		return true
	}
	ctx.decPkt.RescaleTime(ctx.decStream.TimeBase(), ctx.decCodec.TimeBase())

	pkt := ctx.decPkt

	cPkt := (*C.AVPacket)(unsafe.Pointer(pkt.CAVPacket))
	code := C.avcodec_send_packet(ctx.decCodec.CAVCodecContext, cPkt)
	if int(code) < 0 {
		log.Fatal("error when sending packet to codec")
		return false
	}

	for int(code) >= 0 {
		frame := ctx.decFrame
		cFrame := (*C.AVFrame)(unsafe.Pointer(frame.CAVFrame))

		code = C.avcodec_receive_frame(ctx.decCodec.CAVCodecContext, cFrame)
		if code == AVERROR(C.EAGAIN) || code == C.AVERROR_EOF {
			break
		} else if int(code) < 0 {
			log.Fatal("error when receiving frame in the decoder")
			return false
		}

		if ret := C.av_frame_make_writable(ctx.swNV12.CAVFrame); ret < 0 {
			panic("make_writable swNV12")
		}

		C.sws_scale(ctx.swsCtx,
			(**C.uint8_t)(&frame.CAVFrame.data[0]), (*C.int)(&frame.CAVFrame.linesize[0]),
			0, C.int(ctx.srcH),
			(**C.uint8_t)(&ctx.swNV12.CAVFrame.data[0]), (*C.int)(&ctx.swNV12.CAVFrame.linesize[0]),
		)

		hwFrame, _ := NewFrame()
		if int(C.av_hwframe_get_buffer(ctx.hwframeCtx, hwFrame.CAVFrame, 0)) < 0 {
			panic("sadfasdf")
		}
		if int(C.av_hwframe_transfer_data(hwFrame.CAVFrame, ctx.swNV12.CAVFrame, 0)) < 0 {
			panic("pashm")
		}
		ctx.frameChan <- hwFrame

		frame.Unref()
	}
	return true
}

func (ctx *transcodingCtx) handleReadingStopped() bool {
	if !ctx.config.LoopVideo {
		close(ctx.frameChan)
		return false
	}

	ctx.restartPlayback()
	return true
}

func (ctx *transcodingCtx) restartPlayback() {
	C.av_seek_frame(ctx.decFmt.CAVFormatContext, C.int(ctx.decStream.CAVStream.index), 0, C.AVSEEK_FLAG_BACKWARD)
	C.avcodec_flush_buffers(ctx.decCodec.CAVCodecContext)
}

func getPacketBytes(p *Packet) []byte {
	frameBytes := C.GoBytes(unsafe.Pointer(p.CAVPacket.data), C.int(p.Size()))
	return frameBytes
}

func encodeStream(ctx *transcodingCtx) (bool, *EncoderFrame, error) {
	var data []byte
	var err error

	frame, ok := <-ctx.frameChan
	if !ok {
		frame = nil
	} else {
		frame.CAVFrame.pts = C.long(ctx.frameCnt)
	}
	ctx.frameCnt += 1

	if ctx.frameCnt+LOOKAHEAD == ctx.nextKeyFrame && ctx.config.KeyFrameCallback != nil {
		bitrate := ctx.config.KeyFrameCallback()
		ctx.encCodec.CAVCodecContext.bit_rate = C.long(bitrate)
	}

	err = ctx.encCodec.FeedFrame(frame)
	if err != nil {
		err = fmt.Errorf("error in feedframe %v", err)
		return false, nil, err
	}

	if ok {
		frame.Free()
	}

	if ctx.encPkt == nil {
		if ctx.encPkt, err = NewPacket(); err != nil {
			panic(err)
		}
	}
	keyFrame := false
	for {
		ret := ctx.encCodec.GetPacket(ctx.encPkt)
		if ret == int(AVERROR(C.EAGAIN)) {
			break
		} else if ret == 0 {
			ctx.encPkt.SetStreamIndex(ctx.encStream.Index())
			ctx.encPkt.RescaleTime(ctx.encCodec.TimeBase(), ctx.encStream.TimeBase())

			frameBytes := getPacketBytes(ctx.encPkt)

			isKeyFrame := ctx.encPkt.CAVPacket.flags & C.AV_PKT_FLAG_KEY
			if isKeyFrame != 0 {
				ctx.nextKeyFrame = ctx.frameCnt + ctx.config.GoPSize
				ctx.frameBitrate = int(ctx.encCodec.CAVCodecContext.bit_rate)
				keyFrame = true
			}

			// dropF := func() {
			// 	ctx.droppedFrames += 1
			// }
			saveF := func() {
				ctx.rawOutputFile.Write(frameBytes)
			}

			saveF()

			// if len(frameBytes) > 0 {
			// 	if ctx.realFrameCnt >= 60 && keyFrame {
			// 		dropF()
			// 		if keyFrame {
			// 			fmt.Println("Dropping a Keyframe")
			// 		}
			// 	} else {
			// 		if keyFrame {
			// 			// panic("noooooo")
			// 			fmt.Println("Saving a Keyframe")
			// 		}
			// 		saveF()
			// 	}
			// 	ctx.realFrameCnt += 1
			// }
			C.av_interleaved_write_frame(ctx.encFmt.CAVFormatContext, ctx.encPkt.CAVPacket)

			// fmt.Printf("frame: %v \n", ctx.frameCnt)
			// PrintHEVCNALs(frameBytes)
			data = append(data, frameBytes...)

			ctx.encPkt.Unref()
		} else if ret == int(C.AVERROR_EOF) {
			break
		} else {
			break
		}
	}

	if !ok {
		ctx.rawOutputFile.Close()
		// fmt.Printf("Total frames: %v   dropped: %v   saved: %v\n", ctx.realFrameCnt, ctx.droppedFrames, ctx.realFrameCnt-ctx.droppedFrames)

		C.av_write_trailer(ctx.encFmt.CAVFormatContext)

		// Close the output file handle
		if ctx.encFmt.CAVFormatContext.pb != nil {
			C.avio_closep(&ctx.encFmt.CAVFormatContext.pb)
		}

		// Free the format context
		C.avformat_free_context(ctx.encFmt.CAVFormatContext)
	}

	done := !ok
	return done, &EncoderFrame{
		KeyFrame: keyFrame,
		Data:     data,
		Bitrate:  ctx.frameBitrate,
	}, nil
}

func AVERROR(e int) C.int {
	return -C.int(e)
}

func setup(ctx *transcodingCtx) error {
	ctx.frameChan = make(chan *Frame, 1000)
	ctx.frameCnt = 0
	if err := setupDecoder(ctx); err != nil {
		return err
	}
	if err := setupEncoder(ctx); err != nil {
		return err
	}
	return nil
}

type FrameServingContext struct {
	transcodingCtx *transcodingCtx

	bufferGap     int
	decodingEnded bool
	encodingEnded bool

	encChan chan int

	frameCnt       int
	frameSizes     int
	lastSetBitrate int
}

func (fsCtx *FrameServingContext) startDecoding() {
	for {
		for len(fsCtx.transcodingCtx.frameChan) < fsCtx.bufferGap && !fsCtx.decodingEnded {
			if !decodeStream(fsCtx.transcodingCtx) {
				fsCtx.decodingEnded = true
			}
		}
		<-fsCtx.encChan
	}
}

func (fsCtx *FrameServingContext) Init(ctx *Config) {
	tctx := &transcodingCtx{
		config:       ctx,
		nextKeyFrame: -1,
	}

	fsCtx.transcodingCtx = tctx
	fsCtx.transcodingCtx.rawOutputFile, _ = os.Create(ctx.RawOutputPath)
	fsCtx.bufferGap = 10
	if err := setup(fsCtx.transcodingCtx); err != nil {
		panic(err)
	}

	fsCtx.encChan = make(chan int, fsCtx.bufferGap+1)
	go fsCtx.startDecoding()

	// consume decoded frames that produce no encoded frames
	for i := 0; i < LOOKAHEAD; i++ {
		frame := fsCtx.GetNextFrame()
		if len(frame.Data) != 0 {
			panic("non empty buffer returned from a lookahead frame")
		}
	}
	fsCtx.frameCnt = 0
}

func (fsCtx *FrameServingContext) GetNextFrame() *EncoderFrame {
	if fsCtx.encodingEnded {
		return &EncoderFrame{
			Data: nil,
		}
	}

	fsCtx.frameCnt++
	done, frame, err := encodeStream(fsCtx.transcodingCtx)
	if err != nil {
		panic("encoder returned error (handle later)")
	}
	fsCtx.encChan <- 1
	if done {
		fsCtx.encodingEnded = true
	}

	return frame
}
