package captcha

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"

	"spanel-go/internal/pkg/database"
)

// localCache 降级单机内存缓存 (当 Redis 不可用时兜底使用)
var localCache sync.Map

// GenerateRandomCode 生成 4 位随机数字验证码
func GenerateRandomCode() string {
	digits := "0123456789"
	code := make([]byte, 4)
	for i := 0; i < 4; i++ {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		code[i] = digits[idx.Int64()]
	}
	return string(code)
}

// GenerateRandomUUID 生成随机 CaptchaID
func GenerateRandomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// GenerateCaptchaSVG 生成漂亮的自定义数学/数字验证码 SVG 代码
func GenerateCaptchaSVG(code string) string {
	width := 120
	height := 45

	// 随机背景干扰线
	lineCount := 4
	linesSvg := ""
	for i := 0; i < lineCount; i++ {
		x1, _ := rand.Int(rand.Reader, big.NewInt(int64(width)))
		y1, _ := rand.Int(rand.Reader, big.NewInt(int64(height)))
		x2, _ := rand.Int(rand.Reader, big.NewInt(int64(width)))
		y2, _ := rand.Int(rand.Reader, big.NewInt(int64(height)))
		linesSvg += fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="rgba(99, 102, 241, 0.25)" stroke-width="1.5"/>`, x1, y1, x2, y2)
	}

	// 遍历并绘制 4 个扭曲放大的数字
	textSvg := ""
	colors := []string{"#818cf8", "#a78bfa", "#f472b6", "#fb7185"}
	for i, char := range code {
		x := 15 + i*25
		y := 30
		angle, _ := rand.Int(rand.Reader, big.NewInt(40))
		rotateAngle := angle.Int64() - 20 // 旋转 -20度 到 +20度
		color := colors[i%len(colors)]
		textSvg += fmt.Sprintf(`<text x="%d" y="%d" fill="%s" font-size="24" font-weight="bold" font-family="Outfit, sans-serif" transform="rotate(%d, %d, %d)">%c</text>`, x, y, color, rotateAngle, x, y, char)
	}

	// 最终拼接出完整的毛玻璃卡片风格 SVG
	return fmt.Sprintf(
		`<svg width="%d" height="%d" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" style="background: rgba(15,23,42,0.6);">%s%s</svg>`,
		width, height, width, height, linesSvg, textSvg,
	)
}

// StoreCaptchaCode 将验证码缓存到 Redis (优先) 或内存中，设定 5 分钟失效
func StoreCaptchaCode(id, code string) {
	if database.Redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = database.Redis.Set(ctx, "captcha:"+id, code, 5*time.Minute).Err()
		return
	}

	// 降级使用单机内存缓存
	localCache.Store(id, code)
	time.AfterFunc(5*time.Minute, func() {
		localCache.Delete(id)
	})
}

// VerifyCaptchaCode 校验验证码正确性 (校验通过后立即销毁，防单凭证暴力破解)
func VerifyCaptchaCode(id, submittedCode string) bool {
	if id == "" || submittedCode == "" {
		return false
	}

	storedCode := ""
	if database.Redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		val, err := database.Redis.Get(ctx, "captcha:"+id).Result()
		if err == nil {
			storedCode = val
			// 校验通过后立即销毁 (防重用)
			_ = database.Redis.Del(ctx, "captcha:"+id).Err()
		}
	} else {
		// 降级使用单机内存
		if val, ok := localCache.Load(id); ok {
			storedCode = val.(string)
			localCache.Delete(id)
		}
	}

	return storedCode != "" && storedCode == submittedCode
}
