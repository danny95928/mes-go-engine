package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"time"
)

// === 1. 任务定义 ===
type SnapshotJob struct {
	DeviceCode string
	RTSPUrl    string
	ResultChan chan SnapshotResult // 用于返回结果给 API (可选)
}

type SnapshotResult struct {
	ImgUrl string
	Error  error
}

// === 2. 全局队列与配置 ===
var (
	// 任务队列 (缓冲区设为 100，超过则 API 需等待或丢弃)
	JobQueue = make(chan SnapshotJob, 100)
)

// InitVideoProcessor 初始化视频处理模块
// workerCount: 并发工作的协程数量 (例如 3 表示同时只允许 3 个 FFmpeg 运行)
func InitVideoProcessor(workerCount int) {
	log.Printf("🎥 启动视频处理引擎 | Worker数量: %d", workerCount)

	for i := 0; i < workerCount; i++ {
		go worker(i)
	}
}

// worker 消费者协程
func worker(id int) {
	for job := range JobQueue {
		// 1. 执行抓拍
		url, err := streamSnapshot(job.RTSPUrl)

		// 2. 处理结果返回给 API (如果有等待者)
		if job.ResultChan != nil {
			job.ResultChan <- SnapshotResult{ImgUrl: url, Error: err}
		}

		// 3. [新增] 核心逻辑：存入 MySQL 并通知 AI (仅在成功时)
		if err == nil {
			// A. 存入 MySQL (获取身份证号)
			// 这里的 "visual_alarm" 对应数据库里的 Type
			sourceID := SaveToMySQL("visual_alarm", job.DeviceCode, url)

			// B. 发送 Redis (通知 Python)
			sendToRedis("ai_task_queue", map[string]interface{}{
				"id":     sourceID,
				"type":   "image",
				"path":   url, // Python 将下载此 URL
				"device": job.DeviceCode,
				"desc":   "监控抓拍异常画面",
			})

			log.Printf("📸 [Video] 抓拍完成并已推送到 AI 队列: ID=%d", sourceID)
		} else {
			log.Printf("❌ Worker-%d 失败: %v", id, err)
		}
	}
}

// streamSnapshot 核心逻辑：内存管道流处理
func streamSnapshot(rtspUrl string) (string, error) {
	filename := fmt.Sprintf("snap_%d_%d.jpg", time.Now().Unix(), time.Now().Nanosecond())

	// 1. 创建管道：FFmpeg 写 -> pipeWriter | pipeReader -> MinIO 读
	pipeReader, pipeWriter := io.Pipe()

	// 2. 启动 FFmpeg (异步写)
	// 使用 context 防止 FFmpeg 僵死
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// -f image2 - : 指定输出格式为图片，输出到 stdout (-)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-rtsp_transport", "tcp", "-i", rtspUrl, "-frames:v", "1", "-f", "image2", "-")

	// 将 FFmpeg 的标准输出连接到管道的写入端
	cmd.Stdout = pipeWriter
	// 错误输出定向到空，或者 log (调试时可改为 os.Stderr)
	// cmd.Stderr = os.Stderr

	// 创建一个错误通道来捕获 Goroutine 里的错误
	ffmpegErrChan := make(chan error, 1)

	go func() {
		// 命令执行完毕后，必须关闭 pipeWriter，否则 MinIO 会一直等待读取
		defer pipeWriter.Close()

		if err := cmd.Run(); err != nil {
			// log.Printf("FFmpeg cmd error: %v", err)
			ffmpegErrChan <- err
		} else {
			ffmpegErrChan <- nil
		}
	}()

	// 3. 启动 MinIO 上传 (同步读)
	// 这一步会阻塞，直到 FFmpeg 写完数据并关闭管道，或者发生错误
	// size=-1 让 MinIO 自动处理流大小
	url, uploadErr := GlobalStorage.UploadStream(filename, pipeReader, -1, "image/jpeg")

	// 4. 等待 FFmpeg 结果
	ffmpegErr := <-ffmpegErrChan

	if ffmpegErr != nil {
		return "", fmt.Errorf("FFmpeg error: %v", ffmpegErr)
	}
	if uploadErr != nil {
		return "", fmt.Errorf("Upload error: %v", uploadErr)
	}

	return url, nil
}

// AsyncCaptureRequest 对外暴露的非阻塞接口
func AsyncCaptureRequest(deviceCode string, rtspUrl string) <-chan SnapshotResult {
	resultChan := make(chan SnapshotResult, 1)

	select {
	case JobQueue <- SnapshotJob{
		DeviceCode: deviceCode,
		RTSPUrl:    rtspUrl,
		ResultChan: resultChan,
	}:
		// 成功入队
	default:
		// 队列满了 (背压保护)
		resultChan <- SnapshotResult{Error: fmt.Errorf("系统繁忙(Queue Full)，请稍后重试")}
	}

	return resultChan
}
