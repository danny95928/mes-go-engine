package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/goburrow/modbus"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/segmentio/kafka-go"
	// ⚠️ 注意：这里不再需要 import influxdb，因为我们只调用 db_writer.go
)

// SignalData 通用信号结构体 (会被 db_writer.go 共享使用)
type SignalData struct {
	Source      string  `json:"source"`
	Value       float64 `json:"value"`
	DeviceCode  string  `json:"device_code"`
	Timestamp   int64   `json:"ts"`
	Description string  `json:"desc,omitempty"`
	ImageUrl    string  `json:"image_url,omitempty"`
	AgingFactor float64 `json:"aging_factor,omitempty"`
	Prob        float64 `json:"prob,omitempty"`
}

// DeviceState 前端 WebSocket 兼容结构
type DeviceState struct {
	Type        string  `json:"type"`
	Temp        float64 `json:"temp"`
	Flow        float64 `json:"flow"`
	AgingFactor float64 `json:"aging_factor"`
	Prob        float64 `json:"prob"`
	ImageUrl    string  `json:"image_url,omitempty"`
	DeviceCode  string  `json:"device_code,omitempty"`
}

// 全局变量 (❌ 已删除 InfluxClient 和 WriteAPI，因为它们在 db_writer.go 中)
var DataChan = make(chan DeviceState)
var GlobalSlicer = NewSlicer(10 * time.Second)
var KafkaWriter *kafka.Writer
var MinioClient *minio.Client
var KafkaDialer *kafka.Dialer

// 常量 (❌ 已删除 InfluxURL 等配置，因为它们在 db_writer.go 中)
const (
	RNNWindowSize = 50
	KafkaAddr     = "localhost:9092"
	MinioEndpoint = "localhost:9000"
	MinioID       = "minioadmin"
	MinioKey      = "minioadmin"
	MinioBucket   = "industrial-images"
)

// ==========================================
// 1. 初始化模块
// ==========================================

func InitDependencies() {
	initKafka()
	initMinio()
	// ✅ 直接调用 db_writer.go 中的函数 (首字母已大写)
	InitDBWriter()
}

func initKafka() {
	KafkaDialer = &kafka.Dialer{Timeout: 5 * time.Second, DualStack: true}
	conn, err := KafkaDialer.Dial("tcp", KafkaAddr)
	if err != nil {
		log.Printf("⚠️ [Kafka Init] 无法连接 Broker: %v", err)
	} else {
		defer conn.Close()
		controller, err := conn.Controller()
		if err == nil {
			var controllerConn *kafka.Conn
			controllerConn, err = KafkaDialer.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
			if err == nil {
				defer controllerConn.Close()
				topicConfigs := []kafka.TopicConfig{
					// ✅ 1. 温度数据 Topic
					{Topic: "raw_telemetry", NumPartitions: 1, ReplicationFactor: 1},
					// ✅ 2. 新增：流量数据 Topic (实现物理隔离)
					{Topic: "flow_telemetry", NumPartitions: 1, ReplicationFactor: 1},

					{Topic: "alarm_events", NumPartitions: 1, ReplicationFactor: 1},
					{Topic: "batch_reports", NumPartitions: 1, ReplicationFactor: 1},
					{Topic: "rnn_training_data", NumPartitions: 1, ReplicationFactor: 1},
				}
				controllerConn.CreateTopics(topicConfigs...)
				log.Println("✅ [Kafka Init] Topic 检查/创建完成")
			}
		}
	}

	KafkaWriter = &kafka.Writer{
		Addr:         kafka.TCP(KafkaAddr),
		Balancer:     &kafka.LeastBytes{},
		WriteTimeout: 10 * time.Second,
		BatchSize:    1,
		RequiredAcks: kafka.RequireOne,
	}
	log.Println("📡 [Kafka Producer] 同步模式已启动")
	go StartKafkaHealthCheck()
}

func StartKafkaHealthCheck() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		dialer := &kafka.Dialer{Timeout: 2 * time.Second, DualStack: true}
		conn, err := dialer.Dial("tcp", KafkaAddr)
		if err != nil {
			log.Printf("🔴 [Kafka Monitor] 网络中断: %v", err)
			continue
		}
		_, err = conn.ReadPartitions()
		if err != nil {
			log.Printf("🔴 [Kafka Monitor] 服务假死: %v", err)
		}
		conn.Close()
	}
}

func initMinio() {
	var err error
	MinioClient, err = minio.New(MinioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(MinioID, MinioKey, ""),
		Secure: false,
	})
	if err != nil {
		log.Printf("❌ MinIO 初始化失败: %v", err)
		return
	}
	ctx := context.Background()
	exists, err := MinioClient.BucketExists(ctx, MinioBucket)
	if err == nil && !exists {
		MinioClient.MakeBucket(ctx, MinioBucket, minio.MakeBucketOptions{})
		log.Printf("🪣 MinIO Bucket '%s' 创建成功", MinioBucket)
	}
	log.Println("💾 MinIO Client 就绪")
}

// ==========================================
// 2. 核心采集逻辑
// ==========================================

func StartCollector(deviceCode string, ip string, port int) {
	if KafkaWriter == nil || MinioClient == nil {
		InitDependencies()
	}
	// ✅ 退出时关闭 InfluxDB (调用 db_writer.go 中的函数)
	defer CloseInfluxDB()

	address := fmt.Sprintf("%s:%d", ip, port)
	handler := modbus.NewTCPClientHandler(address)
	handler.Timeout = 2 * time.Second
	handler.SlaveId = 1

	log.Printf("🔌 [%s] 连接 Modbus @ %s", deviceCode, address)
	err := handler.Connect()
	if err != nil {
		log.Printf("⚠️ [%s] 连接失败，切换至模拟模式", deviceCode)
	} else {
		defer handler.Close()
	}
	client := modbus.NewClient(handler)

	var lastAlarmTime time.Time
	alarmCooldown := 10 * time.Second
	reportTicker := time.NewTicker(1 * time.Minute)
	rnnBuffer := make([]float64, 0, RNNWindowSize)
	var sumTemp float64
	var countTemp int
	ticker := time.NewTicker(100 * time.Millisecond)

	for {
		select {
		case <-reportTicker.C:
			if countTemp > 0 {
				avg := sumTemp / float64(countTemp)
				valStr := fmt.Sprintf("%.2f", avg)

				// ✅ 调用 db_writer.go 的 MySQL 函数 (忽略返回值)
				_ = SaveToMySQL("signal_report", deviceCode, valStr)

				payload := map[string]interface{}{
					"type":    "text",
					"content": fmt.Sprintf("设备 %s 均温报告: %s", deviceCode, valStr),
				}
				sendToRedis("ai_task_queue", payload)
				sendToKafka("batch_reports", payload)
				sumTemp, countTemp = 0, 0
			}

		case <-ticker.C:
			var currTemp, currFlow float64
			res, err := client.ReadHoldingRegisters(0, 3)
			if err == nil && len(res) >= 6 {
				currTemp = float64(binary.BigEndian.Uint16(res[0:2])) / 10.0
				currFlow = float64(binary.BigEndian.Uint16(res[4:6])) / 10.0
			} else {
				currTemp = 20.0 + rand.Float64()*70.0
				currFlow = 50.0 + rand.Float64()*200.0
			}

			sumTemp += currTemp
			countTemp++
			af := CalculateAgingFactor(currTemp)
			prob := AdjustWeibullProb(0.28, af)

			// 构建数据包
			tempSignal := SignalData{
				Source: "temp_source", Value: currTemp, DeviceCode: deviceCode, Timestamp: time.Now().Unix(),
				AgingFactor: af, Prob: prob,
			}
			flowSignal := SignalData{
				Source: "flow_source", Value: currFlow, DeviceCode: deviceCode, Timestamp: time.Now().Unix(),
			}

			// === 数据分发 (关键修改) ===

			// 1. Kafka 分流
			// 温度 -> raw_telemetry
			sendToKafkaWithKey("raw_telemetry", deviceCode, tempSignal)

			// ✅ 流量 -> flow_telemetry (实现分离)
			sendToKafkaWithKey("flow_telemetry", deviceCode, flowSignal)

			// 2. ✅ InfluxDB (直接调用 db_writer.go 中的函数)
			SaveToInflux(tempSignal)
			SaveToInflux(flowSignal)

			// 3. UI
			uiState := DeviceState{
				Type: "realtime", Temp: currTemp, Flow: currFlow, AgingFactor: af, Prob: prob, DeviceCode: deviceCode,
			}
			select {
			case DataChan <- uiState:
			default:
			}
			GlobalSlicer.Ingest(currTemp)

			// === 异常检测 ===

			// 流量异常
			if currFlow > 1000.0 {
				log.Printf("🚨 [%s] 流量异常: %.1f kg/h", deviceCode, currFlow)
				desc := fmt.Sprintf("设备 %s 流量激增 (%.1f)", deviceCode, currFlow)

				// ✅ 调用 MySQL
				_ = SaveToMySQL("alarm_events", deviceCode, desc)

				alertPayload := SignalData{
					Source: "flow_source", Value: currFlow, DeviceCode: deviceCode, Timestamp: time.Now().Unix(), Description: desc,
				}
				sendToRedis("ai_task_queue", alertPayload)
				sendToKafka("alarm_events", alertPayload)
			}

			// 温度异常
			if currTemp > 80.0 && time.Since(lastAlarmTime) > alarmCooldown {
				lastAlarmTime = time.Now()
				log.Printf("🔥 [%s] 高温报警: %.1f", deviceCode, currTemp)

				// ✅ 调用 MySQL
				_ = SaveToMySQL("signal_alarm", deviceCode, fmt.Sprintf("%.2f", currTemp))

				alertPayload := SignalData{
					Source: "temp_source", Value: currTemp, DeviceCode: deviceCode, Timestamp: time.Now().Unix(), Description: fmt.Sprintf("高温报警 %.1f", currTemp),
				}
				sendToRedis("ai_task_queue", alertPayload)
				sendToKafka("alarm_events", alertPayload)
				go processAlarmImage(currTemp, deviceCode)
			}

			// RNN
			rnnBuffer = append(rnnBuffer, currTemp)
			if len(rnnBuffer) >= RNNWindowSize {
				rnnPayload := map[string]interface{}{"device": deviceCode, "data": rnnBuffer, "ts": time.Now().Unix()}
				sendToRedis("rnn_data_queue", rnnPayload)
				sendToKafka("rnn_training_data", rnnPayload)
				rnnBuffer = make([]float64, 0, RNNWindowSize)
			}
		}
	}
}

// ==========================================
// 3. 辅助函数
// ==========================================

func processAlarmImage(temp float64, deviceCode string) {
	fileName := fmt.Sprintf("alarm_%s_%d.jpg", deviceCode, time.Now().Unix())
	f, err := os.Create(fileName)
	if err != nil {
		log.Printf("❌ 无法创建本地图片: %v", err)
		return
	}
	f.WriteString(fmt.Sprintf("MOCK IMAGE DATA FOR %s AT %.1f DEG", deviceCode, temp))
	f.Close()

	defer func() {
		if err := os.Remove(fileName); err != nil {
			log.Printf("⚠️ 本地图片清理失败: %v", err)
		} else {
			log.Printf("🧹 本地图片已清理: %s", fileName)
		}
	}()

	ctx := context.Background()
	contentType := "image/jpeg"
	info, err := MinioClient.FPutObject(ctx, MinioBucket, fileName, fileName, minio.PutObjectOptions{ContentType: contentType})

	imgUrl := ""
	if err != nil {
		log.Printf("❌ MinIO 上传失败: %v", err)
	} else {
		imgUrl = fmt.Sprintf("http://localhost:9000/%s/%s", MinioBucket, fileName)
		log.Printf("☁️ 图片已上传: %s (Size: %d)", imgUrl, info.Size)
	}

	if imgUrl != "" {
		DataChan <- DeviceState{Type: "alarm_image", Temp: temp, ImageUrl: imgUrl, DeviceCode: deviceCode}
	}
}

func sendToKafkaWithKey(topic string, key string, payload interface{}) {
	if KafkaWriter == nil {
		return
	}
	jsonBytes, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := KafkaWriter.WriteMessages(ctx, kafka.Message{Topic: topic, Key: []byte(key), Value: jsonBytes})
	if err != nil {
		log.Printf("❌ [Kafka Write Fail] Topic: %s | Error: %v", topic, err)
	}
}

func sendToKafka(topic string, payload interface{}) {
	sendToKafkaWithKey(topic, "", payload)
}

func sendToRedis(queueName string, payload interface{}) {
	if RedisCli == nil {
		return
	}
	jsonBytes, _ := json.Marshal(payload)
	RedisCli.LPush(context.Background(), queueName, jsonBytes)
}
