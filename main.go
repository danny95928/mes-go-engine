package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// === 全局变量 ===
var (
	currentStatus DeviceState
	wsClients     = make(map[*websocket.Conn]bool)
	wsMutex       sync.Mutex
	RedisCli      *redis.Client // 全局 Redis 客户端
)

// === 设备配置结构体 ===
type DeviceConfig struct {
	DeviceCode string // 设备编号 (如 R-101)
	IP         string // 设备 IP 地址
	Port       int    // Modbus 端口
}

func main() {
	// === 0. 环境自检 ===
	checkFFmpeg()

	// === 1. 初始化基础服务 ===
	// InitDBWriter 已经在 db_writer.go 中定义，它会初始化 MySQL, InfluxDB 和 Milvus
	InitDBWriter()
	InitStorage() // MinIO
	InitRedis()   // Redis
	InitVideoProcessor(3)

	// === 2. 模拟从 MES 获取设备列表并启动采集 ===
	log.Println("📡 [MES] 正在连接运维系统，拉取设备列表...")

	// 2.1 获取设备名单 (模拟 API 调用)
	deviceList := mockGetDeviceList()

	log.Printf("✅ [MES] 握手成功！共获取到 %d 台设备配置", len(deviceList))

	// 2.2 循环启动采集器
	for _, dev := range deviceList {
		log.Printf("🔌 [System] 正在启动采集协程 -> 设备: %s | 目标: %s:%d", dev.DeviceCode, dev.IP, dev.Port)
		go StartCollector(dev.DeviceCode, dev.IP, dev.Port)
	}

	go startBroadcastManager()

	// === 3. 启动 Web 服务器 ===
	r := gin.Default()

	// 跨域配置
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// === 路由定义 ===
	r.GET("/ws", WsHandler)
	r.GET("/api/params", ParamsHandler)

	r.GET("/api/devices", func(c *gin.Context) {
		devices := []map[string]interface{}{
			{"id": 1, "deviceName": "1#配制反应釜", "deviceCode": "R-101", "deviceType": "生产设备", "status": "在线"},
			{"id": 2, "deviceName": "灌装机#1", "deviceCode": "FIL-01", "deviceType": "生产设备", "status": "在线"},
			{"id": 3, "deviceName": "备用反应釜", "deviceCode": "R-102", "deviceType": "辅助设备", "status": "在线"},
		}
		c.JSON(http.StatusOK, devices)
	})

	// 📸 [抓拍接口]
	r.POST("/api/trigger-alarm", func(c *gin.Context) {
		var req struct {
			DeviceCode string  `json:"deviceCode"`
			Value      float64 `json:"value"`
			Msg        string  `json:"msg"`
		}
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}

		log.Printf("📸 [Job] 收到抓拍请求: 设备 %s", req.DeviceCode)
		mockRtspUrl := "rtsp://localhost:8554/device_01"
		resultChan := AsyncCaptureRequest(req.DeviceCode, mockRtspUrl)

		select {
		case res := <-resultChan:
			if res.Error != nil {
				log.Printf("❌ 处理失败: %v", res.Error)
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": res.Error.Error()})
			} else {
				c.JSON(http.StatusOK, gin.H{
					"status": "success",
					"imgUrl": res.ImgUrl,
					"msg":    "抓拍成功 (内存流处理)",
				})
			}
		case <-time.After(8 * time.Second):
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "系统繁忙，处理超时"})
		}
	})

	// 🕹️ PTZ 云台控制
	r.POST("/api/ptz", func(c *gin.Context) {
		var req struct {
			Direction string  `json:"direction"`
			Speed     float64 `json:"speed"`
		}
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}

		log.Printf("🕹️ 收到云台指令: %s (速度: %.1f)", req.Direction, req.Speed)
		soapMessage := generateMockOnvifXML(req.Direction)
		log.Println("📡 [模拟发送 ONVIF 信号] -> Camera")
		log.Println(soapMessage)

		c.JSON(http.StatusOK, gin.H{"status": "success", "msg": "指令已发送"})
	})

	// 👇👇👇 新增: 接收 Python 发来的信号并存入 Milvus 👇👇👇
	r.POST("/api/upload_signal", func(c *gin.Context) {
		// 1. 定义接收结构 (匹配 Python 的 JSON)
		var req struct {
			DeviceCode string             `json:"device_code"`
			Timestamp  int64              `json:"timestamp"`
			Signal     map[string]float64 `json:"signal"` // temp, flow, rpm
			State      string             `json:"state"`
		}

		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败"})
			return
		}

		// 2. 构造向量 [温度, 流量, 转速]
		// 注意: 如果 map 里没有 key，float64 默认是 0
		temp := req.Signal["temp"]
		flow := req.Signal["flow"]
		rpm := req.Signal["rpm"]

		vector := []float32{float32(temp), float32(flow), float32(rpm)}

		// 3. 写入 Milvus
		// SaveToMilvus 必须在 db_writer.go 中定义
		err := SaveToMilvus(req.DeviceCode, vector, req.State, req.Timestamp)
		if err != nil {
			log.Printf("❌ Milvus 写入失败: %v", err)
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		// 成功返回
		c.JSON(200, gin.H{"status": "success"})
	})

	log.Println("🚀 服务器启动监听 :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal("服务器启动失败:", err)
	}
}

// === 下面是辅助函数 (保持不变) ===

func mockGetDeviceList() []DeviceConfig {
	time.Sleep(500 * time.Millisecond)
	return []DeviceConfig{
		{DeviceCode: "R-101", IP: "127.0.0.1", Port: 5020},
		{DeviceCode: "R-102", IP: "192.168.1.102", Port: 502},
		{DeviceCode: "FIL-01", IP: "192.168.1.201", Port: 502},
	}
}

func InitRedis() {
	RedisCli = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})
	ctx := context.Background()
	_, err := RedisCli.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("❌ Redis 连接失败: %v", err)
	}
	log.Println("✅ Redis Connected!")
}

func checkFFmpeg() {
	cmd := exec.Command("ffmpeg", "-version")
	if err := cmd.Run(); err != nil {
		log.Println("❌ 严重错误: 未检测到 FFmpeg！")
	} else {
		log.Println("✅ [环境检查] FFmpeg 已安装。")
	}
}

func generateMockOnvifXML(direction string) string {
	x, y := 0.0, 0.0
	switch direction {
	case "left":
		x = -0.5
	case "right":
		x = 0.5
	case "up":
		y = 0.5
	case "down":
		y = -0.5
	case "stop":
		x, y = 0.0, 0.0
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>...<tt:PanTilt x="%.1f" y="%.1f"...`, x, y)
}

func startBroadcastManager() {
	for {
		select {
		case state := <-DataChan:
			currentStatus = state
			broadcastToClients(state)
		case slice := <-GlobalSlicer.OutChan:
			broadcastToClients(slice)
		}
	}
}

func broadcastToClients(data interface{}) {
	wsMutex.Lock()
	defer wsMutex.Unlock()
	for client := range wsClients {
		client.SetWriteDeadline(time.Now().Add(2 * time.Second))
		err := client.WriteJSON(data)
		if err != nil {
			client.Close()
			delete(wsClients, client)
		}
	}
}
