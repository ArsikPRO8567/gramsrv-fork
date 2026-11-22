package files

import (
        "context"
        "encoding/json"
        "fmt"
        "os"
        "os/exec"
        "path/filepath"
        "time"
)

const (
        videoProbeTimeout = 10 * time.Second
        videoProbeSlots   = 2
)

// FFmpegVideoProber 用 ffprobe 读取视频容器（webm/mp4/mov）的宽高与时长，
// 供两条路径投影客户端未带属性的视频：
//  1. 管理台上传的 webm 视频贴纸（AdminUploadStickerMaterial 需要合成 video 属性）；
//  2. 用户以 document 形式上传但没有 documentAttributeVideo 的视频（正确投影到界面）。
//
// 探测失败一律降级（调用方决定拒绝还是保持无属性），绝不让上传链路崩溃。
type FFmpegVideoProber struct {
        ffprobe string
        timeout time.Duration
        slots   chan struct{}
}

func NewFFmpegVideoProber() (*FFmpegVideoProber, error) {
        ffmpeg, err := exec.LookPath("ffmpeg")
        if err != nil {
                return nil, err
        }
        ffprobeName := "ffprobe"
        if ext := filepath.Ext(ffmpeg); ext != "" {
                ffprobeName += ext
        }
        ffprobe := filepath.Join(filepath.Dir(ffmpeg), ffprobeName)
        if _, err := os.Stat(ffprobe); err != nil {
                ffprobe, err = exec.LookPath("ffprobe")
                if err != nil {
                        return nil, err
                }
        }
        return &FFmpegVideoProber{
                ffprobe: ffprobe,
                timeout: videoProbeTimeout,
                slots:   make(chan struct{}, videoProbeSlots),
        }, nil
}

// Probe 返回视频流的宽高与时长。Data 为空表示探测未产出可用元数据。
func (p *FFmpegVideoProber) Probe(ctx context.Context, data []byte, mimeType string) (GIFVideo, error) {
        if p == nil || p.ffprobe == "" {
                return GIFVideo{}, fmt.Errorf("video prober unavailable")
        }
        if len(data) == 0 || len(data) > videoThumbnailMaxInputBytes {
                return GIFVideo{}, fmt.Errorf("video probe input size out of range: %d", len(data))
        }
        select {
        case p.slots <- struct{}{}:
                defer func() { <-p.slots }()
        case <-ctx.Done():
                return GIFVideo{}, ctx.Err()
        }
        runCtx, cancel := context.WithTimeout(ctx, p.timeout)
        defer cancel()

        input, err := os.CreateTemp("", "telesrv-video-probe-*"+videoTempExt(mimeType))
        if err != nil {
                return GIFVideo{}, fmt.Errorf("create video probe input: %w", err)
        }
        inputPath := input.Name()
        defer os.Remove(inputPath)
        if _, err := input.Write(data); err != nil {
                input.Close()
                return GIFVideo{}, fmt.Errorf("write video probe input: %w", err)
        }
        if err := input.Close(); err != nil {
                return GIFVideo{}, fmt.Errorf("close video probe input: %w", err)
        }

        cmd := exec.CommandContext(runCtx, p.ffprobe, "-v", "error", "-select_streams", "v:0",
                "-show_entries", "stream=width,height,duration:format=duration", "-of", "json", inputPath)
        out, err := cmd.CombinedOutput()
        if runCtx.Err() != nil {
                return GIFVideo{}, runCtx.Err()
        }
        if err != nil {
                return GIFVideo{}, commandError("ffprobe video output", err, out)
        }
        return parseVideoProbeJSON(out)
}

// parseVideoProbeJSON 解析 ffprobe 的 JSON 输出（stream 宽高 + format/stream 时长），
// GIFTranscoder.probe 与 FFmpegVideoProber.Probe 共用同一格式。
func parseVideoProbeJSON(out []byte) (GIFVideo, error) {
        var result struct {
                Streams []struct {
                        Width, Height int
                        Duration      string
                } `json:"streams"`
                Format struct {
                        Duration string `json:"duration"`
                } `json:"format"`
        }
        if err := json.Unmarshal(out, &result); err != nil || len(result.Streams) != 1 {
                return GIFVideo{}, fmt.Errorf("invalid ffprobe video metadata")
        }
        duration := parsePositiveFloat(result.Streams[0].Duration)
        if duration == 0 {
                duration = parsePositiveFloat(result.Format.Duration)
        }
        stream := result.Streams[0]
        if stream.Width <= 0 || stream.Height <= 0 || duration <= 0 {
                return GIFVideo{}, fmt.Errorf("incomplete video metadata")
        }
        return GIFVideo{Width: stream.Width, Height: stream.Height, Duration: duration}, nil
}
