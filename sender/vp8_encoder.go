// package main

/*
#cgo LDFLAGS: -lvpx
#include <vpx/vpx_encoder.h>
#include <vpx/vp8cx.h>
#include <stdlib.h>

static int set_rc_bitrate(vpx_codec_ctx_t* ctx, vpx_codec_enc_cfg_t* cfg, unsigned int kbps) {
  cfg->rc_target_bitrate = kbps; // kbps
  return vpx_codec_enc_config_set(ctx, cfg);
}

// static int force_kf(vpx_codec_ctx_t* ctx) {
//   return vpx_codec_control(ctx, VP8E_SET_FORCE_KF, 1);

*/
import "C"
import (
	"fmt"
	"unsafe"
)

func LibvpxVersion() string {
	return "sdfasdf" + C.GoString(C.vpx_codec_version_str())
}

type VP8 struct {
	ctx C.vpx_codec_ctx_t
	cfg C.vpx_codec_enc_cfg_t
	// width, height, timebase, etc.
}

// Init (one-time)
func (e *VP8) Init(w, h int, fps int, kbps uint) error {
	iface := C.vpx_codec_vp8_cx()
	if C.vpx_codec_enc_config_default(iface, &e.cfg, 0) != C.VPX_CODEC_OK {
		return fmt.Errorf("config_default")
	}
	e.cfg.g_w = C.uint(w)
	e.cfg.g_h = C.uint(h)
	e.cfg.g_timebase.num = 1
	e.cfg.g_timebase.den = C.uint(fps)
	e.cfg.rc_target_bitrate = C.uint(kbps)
	if C.vpx_codec_enc_init_ver(&e.ctx, iface, &e.cfg, 0, C.VPX_ENCODER_ABI_VERSION) != C.VPX_CODEC_OK {
		return fmt.Errorf("enc_init")
	}
	return nil
}

// Change bitrate live
func (e *VP8) SetBitrate(kbps uint) error {
	if C.set_rc_bitrate(&e.ctx, &e.cfg, C.uint(kbps)) != C.VPX_CODEC_OK {
		return fmt.Errorf("reconfig failed")
	}
	return nil
}

// func (e *VP8) ForceKeyframe() { _ = C.force_kf(&e.ctx) }

// Encode one raw frame (I420)
func (e *VP8) Encode(y, u, v []byte, pts uint64) ([][]byte, error) {
	// wrap planes into vpx_image_t (omitted for brevity), then:
	flags := C.vpx_enc_frame_flags_t(0)
	if C.vpx_codec_encode(&e.ctx, img, C.vpx_codec_pts_t(pts), 1, flags, C.VPX_DL_REALTIME) != C.VPX_CODEC_OK {
		return nil, fmt.Errorf("encode failed")
	}
	// pull packets
	var iter C.vpx_codec_iter_t
	var pkts [][]byte
	for {
		pkt := C.vpx_codec_get_cx_data(&e.ctx, &iter)
		if pkt == nil {
			break
		}
		if (*pkt).kind == C.VPX_CODEC_CX_FRAME_PKT {
			data := C.GoBytes(unsafe.Pointer((*pkt).data.frame.buf), C.int((*pkt).data.frame.sz))
			pkts = append(pkts, data) // compressed VP8 frame
		}
	}
	return pkts, nil
}
