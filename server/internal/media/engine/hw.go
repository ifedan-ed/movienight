package engine

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"
)

// DefaultRenderNode is the GPU device VAAPI and QSV use unless
// MEDIA_HWACCEL_DEVICE names another.
const DefaultRenderNode = "/dev/dri/renderD128"

// CheckHW proves the chosen hardware encoder works by encoding one frame
// with it, so a wrong MEDIA_HWACCEL stops the media service at start with a
// message instead of failing every transcode later. "none" needs no check.
func CheckHW(ctx context.Context, ffmpeg string, hw HW) error {
	if !slices.Contains(HWKinds, hw.Kind) {
		return fmt.Errorf("MEDIA_HWACCEL must be one of none, vaapi, qsv, nvenc, not %q", hw.Kind)
	}
	if hw.Kind == "none" {
		return nil
	}
	if hw.Kind != "nvenc" {
		if _, err := os.Stat(hw.Device); err != nil {
			return fmt.Errorf("MEDIA_HWACCEL is %s but %s is not there (pass the device into the container)", hw.Kind, hw.Device)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	args = append(args, hwInit(hw)...)
	filter := "format=yuv420p"
	switch hw.Kind {
	case "vaapi":
		filter = "format=nv12,hwupload"
	case "qsv":
		filter = "format=nv12,hwupload=extra_hw_frames=64"
	}
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=0.2", "-vf", filter)
	args = append(args, videoEncoder(hw)...)
	args = append(args, "-frames:v", "1", "-f", "null", "-")
	e := &Engine{cfg: Config{FFmpeg: ffmpeg}}
	if err := e.ffmpeg(ctx, args...); err != nil {
		return fmt.Errorf("MEDIA_HWACCEL is %s but a test encode failed: %w", hw.Kind, err)
	}
	return nil
}

// FindGPU names a GPU render node when one is present, so the media
// service can say at start that MEDIA_HWACCEL could use it. It never turns
// anything on by itself.
func FindGPU() string {
	if _, err := os.Stat(DefaultRenderNode); err == nil {
		return DefaultRenderNode
	}
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		return "/dev/nvidia0"
	}
	return ""
}
