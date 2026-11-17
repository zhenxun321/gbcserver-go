package imageutil

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"strings"
)

// Options 控制上传和历史资源压缩的尺寸与 JPEG 质量。
type Options struct {
	MaxDimension int
	JPEGQuality  int
}

// Compress 将可解码的 PNG/JPEG 图片缩放并重新编码；WebP 和 GIF 原样保留，避免改变动画或引入额外编解码器。
// 当压缩结果不更小时返回原始数据，调用方可以直接安全覆盖原文件。
func Compress(data []byte, extension string, options Options) ([]byte, bool, error) {
	if len(data) == 0 {
		return nil, false, errors.New("图片为空")
	}
	extension = strings.ToLower(strings.TrimSpace(extension))
	if extension == ".webp" || extension == ".gif" {
		return data, false, nil
	}
	if extension != ".png" && extension != ".jpg" && extension != ".jpeg" {
		return nil, false, errors.New("不支持的图片格式")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 16*1024*1024 {
		return nil, false, errors.New("图片分辨率过高")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	limit := options.MaxDimension
	for {
		var output bytes.Buffer
		resized := resizeNearest(decoded, limit)
		switch extension {
		case ".jpg", ".jpeg":
			quality := options.JPEGQuality
			if quality < 1 || quality > 100 {
				quality = 82
			}
			if err := jpeg.Encode(&output, resized, &jpeg.Options{Quality: quality}); err != nil {
				return nil, false, err
			}
		case ".png":
			encoder := png.Encoder{CompressionLevel: png.BestCompression}
			if err := encoder.Encode(&output, resized); err != nil {
				return nil, false, err
			}
		}
		if output.Len() > 0 && output.Len() < len(data) {
			return output.Bytes(), true, nil
		}
		bounds := decoded.Bounds()
		if (bounds.Dx() <= limit && bounds.Dy() <= limit) || limit <= 32 {
			return data, false, nil
		}
		limit = max(32, limit*3/4)
	}
}

// DecodeDimensions 读取图片尺寸，用于命令输出和压缩统计。
func DecodeDimensions(data []byte) (width, height int, err error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	return config.Width, config.Height, nil
}

func resizeNearest(src image.Image, maxDimension int) image.Image {
	if maxDimension <= 0 {
		return src
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= maxDimension && height <= maxDimension {
		return src
	}
	scale := float64(maxDimension) / float64(width)
	if height > width {
		scale = float64(maxDimension) / float64(height)
	}
	newWidth := max(1, int(float64(width)*scale+0.5))
	newHeight := max(1, int(float64(height)*scale+0.5))
	result := image.NewNRGBA(image.Rect(0, 0, newWidth, newHeight))
	for y := 0; y < newHeight; y++ {
		sourceY := clampFloat((float64(y)+0.5)*float64(height)/float64(newHeight)-0.5, 0, float64(height-1))
		top := int(math.Floor(sourceY))
		bottom := min(top+1, height-1)
		weightY := sourceY - float64(top)
		for x := 0; x < newWidth; x++ {
			sourceX := clampFloat((float64(x)+0.5)*float64(width)/float64(newWidth)-0.5, 0, float64(width-1))
			left := int(math.Floor(sourceX))
			right := min(left+1, width-1)
			weightX := sourceX - float64(left)
			c00 := color.NRGBAModel.Convert(src.At(bounds.Min.X+left, bounds.Min.Y+top)).(color.NRGBA)
			c10 := color.NRGBAModel.Convert(src.At(bounds.Min.X+right, bounds.Min.Y+top)).(color.NRGBA)
			c01 := color.NRGBAModel.Convert(src.At(bounds.Min.X+left, bounds.Min.Y+bottom)).(color.NRGBA)
			c11 := color.NRGBAModel.Convert(src.At(bounds.Min.X+right, bounds.Min.Y+bottom)).(color.NRGBA)
			result.SetNRGBA(x, y, color.NRGBA{
				R: bilinearChannel(c00.R, c10.R, c01.R, c11.R, weightX, weightY),
				G: bilinearChannel(c00.G, c10.G, c01.G, c11.G, weightX, weightY),
				B: bilinearChannel(c00.B, c10.B, c01.B, c11.B, weightX, weightY),
				A: bilinearChannel(c00.A, c10.A, c01.A, c11.A, weightX, weightY),
			})
		}
	}
	return result
}

func bilinearChannel(topLeft, topRight, bottomLeft, bottomRight uint8, x, y float64) uint8 {
	top := float64(topLeft)*(1-x) + float64(topRight)*x
	bottom := float64(bottomLeft)*(1-x) + float64(bottomRight)*x
	return uint8(math.Round(top*(1-y) + bottom*y))
}

func clampFloat(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
