// 静态资源存储服务：优先返回本地文件，缺失时从上游 TARGET 下载，支持 imageMogr2 图片处理。
// 环境变量：PORT（监听端口，默认 8080）、TARGET（上游地址，必填）、LOCAL_DIR（本地存储目录，必填）
package main

import (
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nfnt/resize"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	target := os.Getenv("TARGET")
	localDir := os.Getenv("LOCAL_DIR")
	if target == "" || localDir == "" {
		log.Fatal("TARGET 和 LOCAL_DIR 环境变量必填")
	}
	if _, err := url.Parse(target); err != nil {
		log.Fatalf("TARGET 不是有效 URL: %v", err)
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.NoRoute(func(c *gin.Context) {
		handleStaticStore(c, target, localDir)
	})

	log.Printf("静态资源服务启动，监听端口 %s，本地目录 %s，上游 %s", port, localDir, target)
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

func setCORSHeaders(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
	c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	c.Header("Access-Control-Max-Age", "86400")
}

func handleStaticStore(c *gin.Context, target, localDir string) {
	setCORSHeaders(c)
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusNoContent)
		return
	}

	urlPath := c.Request.URL.Path
	if urlPath == "/" || urlPath == "" {
		c.String(http.StatusNotFound, "not found")
		return
	}

	// 清理路径，防止目录遍历
	cleanPath := path.Clean(urlPath)
	if strings.HasPrefix(cleanPath, "..") {
		c.String(http.StatusNotFound, "not found")
		return
	}

	localFile := filepath.Join(localDir, cleanPath)

	fileInfo, err := os.Stat(localFile)
	fileExists := err == nil && !fileInfo.IsDir()

	if !fileExists {
		if err := downloadAndSave(c.Request, target, localFile); err != nil {
			log.Printf("下载文件失败: %v", err)
			c.String(http.StatusNotFound, "not found")
			return
		}
	}

	// 判断是否有 imageMogr2 参数且文件是图片
	queryRaw := c.Request.URL.RawQuery
	if strings.Contains(queryRaw, "imageMogr2") && isImageFile(localFile) {
		handleImageMogr2(c, localFile, queryRaw)
		return
	}

	c.File(localFile)
}

func downloadAndSave(req *http.Request, target, localFile string) error {
	// 确保目录存在
	dir := filepath.Dir(localFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	targetURL, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("解析目标地址失败: %w", err)
	}

	destURL := *targetURL
	destURL.Path = path.Join(destURL.Path, req.URL.Path)

	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, destURL.String(), req.Body)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	newReq.Header = req.Header.Clone()

	// 移除可能导致 304 Not Modified 的缓存相关 header，确保每次都能获取到最新资源
	newReq.Header.Del("If-Modified-Since")
	newReq.Header.Del("If-None-Match")
	newReq.Header.Del("If-Unmodified-Since")
	newReq.Header.Del("If-Match")
	newReq.Header.Del("If-Range")

	client := &http.Client{}
	resp, err := client.Do(newReq)
	if err != nil {
		return fmt.Errorf("请求目标失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("目标返回非200状态码: %d", resp.StatusCode)
	}

	file, err := os.Create(localFile)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	return nil
}

func isImageFile(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp":
		return true
	}
	return false
}

func handleImageMogr2(c *gin.Context, localFile string, imageMogr2 string) {
	// 解析 imageMogr2 参数
	// 支持: thumbnail/!50p, cut/600x800x100, quality/70, thumbnail/!50p/cut/600x800x100x200 组合操作
	imgFile, err := os.Open(localFile)
	if err != nil {
		c.String(http.StatusInternalServerError, "open image error")
		return
	}
	defer imgFile.Close()

	img, format, err := image.Decode(imgFile)
	if err != nil {
		c.String(http.StatusInternalServerError, "decode image error")
		return
	}

	resultImg, quality := parseImageMogr2Pipeline(img, imageMogr2)
	if resultImg == nil {
		c.File(localFile)
		return
	}

	c.Header("Content-Type", "image/"+format)
	c.Status(http.StatusOK)
	switch format {
	case "jpeg":
		if quality > 0 {
			jpeg.Encode(c.Writer, resultImg, &jpeg.Options{Quality: quality})
		} else {
			jpeg.Encode(c.Writer, resultImg, nil)
		}
	case "png":
		png.Encode(c.Writer, resultImg)
	case "gif":
		gif.Encode(c.Writer, resultImg, nil)
	default:
		png.Encode(c.Writer, resultImg)
	}
}

// parseImageMogr2Pipeline 解析 imageMogr2 管道操作
// 支持多个操作组合，如 thumbnail/!50p/cut/600x800x100x200/quality/70
// 返回处理后的图片和质量参数
func parseImageMogr2Pipeline(img image.Image, imageMogr2 string) (image.Image, int) {
	parts := strings.Split(imageMogr2, "/")
	if len(parts) < 2 {
		return nil, 0
	}

	resultImg := img
	quality := 0
	i := 0
	for i < len(parts)-1 {
		operation := parts[i]
		if !isImageOperation(parts[i]) {
			i++
			continue
		}

		params := parts[i+1]

		switch operation {
		case "thumbnail":
			resultImg = parseThumbnail(resultImg, params)
		case "cut":
			resultImg = parseAndCut(resultImg, params)
		case "crop":
			resultImg = parseAndCrop(resultImg, params)
		case "iradius":
			resultImg = parseAndIRadius(resultImg, params)
		case "rradius":
			resultImg = parseAndRRadius(resultImg, params)
		case "scrop":
			resultImg = parseAndSCrop(resultImg, params)
		case "quality":
			q, err := strconv.Atoi(params)
			if err == nil && q > 0 && q <= 100 {
				quality = q
			}
		default:
			return nil, 0
		}

		if resultImg == nil {
			return nil, 0
		}
		i += 2
	}

	return resultImg, quality
}

// isImageOperation 判断是否为图片处理操作名称
func isImageOperation(s string) bool {
	switch s {
	case "thumbnail", "cut", "crop", "iradius", "rradius", "scrop", "quality":
		return true
	}
	return false
}

// parseThumbnail 解析 thumbnail 缩放操作
// 支持: !50p(百分比), 300x(指定宽), x300(指定高), 300x400(指定宽高)
func parseThumbnail(img image.Image, params string) image.Image {
	params = strings.TrimSpace(params)
	if params == "" {
		return img
	}

	bounds := img.Bounds()
	origW := uint(bounds.Dx())
	origH := uint(bounds.Dy())

	if strings.HasSuffix(params, "p") {
		percentStr := strings.TrimPrefix(params, "!")
		percentStr = strings.TrimSuffix(percentStr, "p")
		percent, err := strconv.ParseFloat(percentStr, 64)
		if err != nil || percent <= 0 {
			return img
		}
		width := uint(float64(origW) * percent / 100)
		height := uint(float64(origH) * percent / 100)
		return resize.Resize(width, height, img, resize.Lanczos3)
	}

	if strings.Contains(params, "x") {
		parts := strings.Split(params, "x")
		if len(parts) == 2 {
			var width, height uint
			if parts[0] != "" {
				w, _ := strconv.Atoi(parts[0])
				width = uint(w)
			}
			if parts[1] != "" {
				h, _ := strconv.Atoi(parts[1])
				height = uint(h)
			}
			if width == 0 && height == 0 {
				return img
			}
			return resize.Resize(width, height, img, resize.Lanczos3)
		}
	}

	return img
}

func parseAndCrop(img image.Image, params string) image.Image {
	var width, height uint
	if strings.Contains(params, "x") {
		parts := strings.Split(params, "x")
		if len(parts) == 2 {
			if parts[0] != "" {
				w, _ := strconv.Atoi(parts[0])
				width = uint(w)
			}
			if parts[1] != "" {
				h, _ := strconv.Atoi(parts[1])
				height = uint(h)
			}
		}
	}
	if width == 0 && height == 0 {
		return img
	}
	return resize.Resize(width, height, img, resize.Lanczos3)
}

func parseAndCut(img image.Image, params string) image.Image {
	parts := strings.Split(params, "x")
	if len(parts) < 2 {
		return img
	}
	width, _ := strconv.Atoi(parts[0])
	height, _ := strconv.Atoi(parts[1])
	dx, dy := 0, 0
	if len(parts) >= 3 {
		dx, _ = strconv.Atoi(parts[2])
	}
	if len(parts) >= 4 {
		dy, _ = strconv.Atoi(parts[3])
	}
	if width <= 0 || height <= 0 {
		return img
	}
	return cutImage(img, width, height, dx, dy)
}

func cutImage(img image.Image, width, height, dx, dy int) image.Image {
	bounds := img.Bounds()
	maxX := bounds.Max.X
	maxY := bounds.Max.Y

	if dx < 0 {
		dx = 0
	}
	if dy < 0 {
		dy = 0
	}
	if dx+width > maxX {
		width = maxX - dx
	}
	if dy+height > maxY {
		height = maxY - dy
	}
	if width <= 0 || height <= 0 {
		return img
	}

	result := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			result.Set(x, y, img.At(dx+x, dy+y))
		}
	}
	return result
}

func parseAndIRadius(img image.Image, params string) image.Image {
	radius, _ := strconv.Atoi(params)
	if radius <= 0 {
		return img
	}
	return cutCircle(img, radius)
}

func cutCircle(img image.Image, radius int) image.Image {
	size := radius * 2
	result := image.NewRGBA(image.Rect(0, 0, size, size))
	centerX, centerY := radius, radius

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := x - centerX
			dy := y - centerY
			if dx*dx+dy*dy <= radius*radius {
				result.Set(x, y, img.At(x, y))
			}
		}
	}
	return result
}

func parseAndRRadius(img image.Image, params string) image.Image {
	radius, _ := strconv.Atoi(params)
	if radius <= 0 {
		return img
	}
	return roundCorner(img, radius)
}

func roundCorner(img image.Image, radius int) image.Image {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	result := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x < radius && y < radius && !inCircle(x, y, radius-1, radius-1, radius)) ||
				(x >= w-radius && y < radius && !inCircle(x, y, w-radius, radius-1, radius)) ||
				(x < radius && y >= h-radius && !inCircle(x, y, radius-1, h-radius, radius)) ||
				(x >= w-radius && y >= h-radius && !inCircle(x, y, w-radius, h-radius, radius)) {
				continue
			}
			result.Set(x, y, img.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return result
}

func inCircle(x, y, cx, cy, r int) bool {
	dx := x - cx
	dy := y - cy
	return dx*dx+dy*dy <= r*r
}

func parseAndSCrop(img image.Image, params string) image.Image {
	// scrop/widthxheight - 人脸智能裁剪（简化实现为居中缩放裁剪）
	return parseAndCrop(img, params)
}
