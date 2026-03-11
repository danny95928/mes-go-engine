package main

import (
	"context"
	"fmt"
	"log"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// ==========================================
// 1. MySQL 部分
// ==========================================

// AlarmLog 定义 MySQL 表结构
type AlarmLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	Type       string    `json:"type"`       // "signal_alarm"(阈值), "visual_alarm"(视觉), "signal_report"(定时)
	Content    string    `json:"content"`    // 温度值或图片路径
	DeviceCode string    `json:"deviceCode"` // 设备编号
	Status     string    `json:"status"`     // "Pending", "Processed"
}

var DB *gorm.DB

// InitDBWriter 初始化入口
func InitDBWriter() {
	// A. 初始化 InfluxDB
	InitInfluxDB()
	// B. 初始化 MySQL (带重试机制)
	initMySQL()
	// C. 初始化 Milvus (新增)
	InitMilvus()
}

func initMySQL() {
	// ⚠️ 增加 allowNativePasswords=true 以增强兼容性
	dsn := "root:rootpassword@tcp(localhost:3306)/mes_db?charset=utf8mb4&parseTime=True&loc=Local&allowNativePasswords=true"

	var err error

	// === 🛠️ 重试机制 (最多等待 60 秒) ===
	log.Println("⏳ [MySQL] 正在等待数据库就绪...")

	maxRetries := 30
	for i := 1; i <= maxRetries; i++ {
		// 尝试打开连接
		DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})

		if err == nil {
			// 获取通用数据库对象 sql.DB 以进行 Ping 测试
			sqlDB, _ := DB.DB()
			if err = sqlDB.Ping(); err == nil {
				// 连接成功！
				log.Printf("✅ [MySQL] 连接成功 (尝试次数: %d)", i)
				break
			}
		}

		// 如果连接失败，打印日志并等待
		log.Printf("⚠️ [MySQL] 连接失败 (%d/%d): %v - 2秒后重试...", i, maxRetries, err)
		time.Sleep(2 * time.Second)
	}

	// 如果重试完了还是失败，再退出
	if err != nil {
		log.Fatalf("❌ [MySQL] 最终连接失败，请检查 Docker 容器状态。错误: %v", err)
	}

	// 自动建表
	err = DB.AutoMigrate(&AlarmLog{})
	if err != nil {
		log.Printf("⚠️ [MySQL] 自动建表失败: %v", err)
	} else {
		log.Println("✅ MySQL Database Connected & Migrated!")
	}
}

// SaveToMySQL 存入数据并返回自增 ID
func SaveToMySQL(logType string, deviceCode string, content string) uint {
	if DB == nil {
		return 0
	}
	newLog := AlarmLog{
		Type:       logType,
		DeviceCode: deviceCode,
		Content:    content,
		Status:     "Pending",
		CreatedAt:  time.Now(),
	}

	result := DB.Create(&newLog)
	if result.Error != nil {
		log.Printf("❌ MySQL 写入失败: %v", result.Error)
		return 0
	}

	log.Printf("💾 [MySQL] 落库成功 ID: %d | Type: %s", newLog.ID, logType)
	return newLog.ID
}

// ==========================================
// 2. InfluxDB 部分
// ==========================================

var InfluxClient influxdb2.Client
var WriteAPI api.WriteAPI

const (
	InfluxURL    = "http://localhost:8086"
	InfluxToken  = "my-super-secret-auth-token"
	InfluxOrg    = "myorg"
	InfluxBucket = "mybucket"
)

func InitInfluxDB() {
	InfluxClient = influxdb2.NewClient(InfluxURL, InfluxToken)
	WriteAPI = InfluxClient.WriteAPI(InfluxOrg, InfluxBucket)
	go func() {
		for err := range WriteAPI.Errors() {
			log.Printf("❌ [InfluxDB Error] %v", err)
		}
	}()
	log.Println("🌊 [InfluxDB] 客户端已连接 (Async Mode)")
}

func CloseInfluxDB() {
	if InfluxClient != nil {
		WriteAPI.Flush()
		InfluxClient.Close()
		log.Println("🌊 [InfluxDB] 连接已关闭")
	}
}

func SaveToInflux(data SignalData) {
	if WriteAPI == nil {
		return
	}
	p := influxdb2.NewPoint(
		"industrial_signals",
		map[string]string{
			"device": data.DeviceCode,
			"source": data.Source,
		},
		map[string]interface{}{
			"value": data.Value,
		},
		time.Unix(data.Timestamp, 0),
	)
	WriteAPI.WritePoint(p)
}

func SaveToDB(data DeviceState) {
	if WriteAPI == nil {
		return
	}
	p := influxdb2.NewPointWithMeasurement("reactor_status").
		AddTag("unit_id", "1").
		AddTag("device", data.DeviceCode).
		AddField("temperature", data.Temp).
		AddField("aging_factor", data.AgingFactor).
		AddField("failure_prob", data.Prob).
		SetTime(time.Now())
	WriteAPI.WritePoint(p)
}

// ==========================================
// 3. Milvus 部分 (新增)
// ==========================================

var MilvusClient client.Client

func InitMilvus() {
	ctx := context.Background()
	var err error

	// 连接 Milvus 容器
	MilvusClient, err = client.NewClient(ctx, client.Config{
		Address: "localhost:19530",
	})
	if err != nil {
		log.Printf("❌ Milvus 连接失败: %v", err)
		return
	}

	// 检查 Collection 是否存在，不存在则创建
	collName := "sensor_states"
	has, _ := MilvusClient.HasCollection(ctx, collName)
	if !has {
		schema := &entity.Schema{
			CollectionName: collName,
			Description:    "设备状态向量库",
			Fields: []*entity.Field{
				{
					Name:       "id",
					DataType:   entity.FieldTypeInt64,
					PrimaryKey: true,
					AutoID:     true,
				},
				{
					Name:     "vector",
					DataType: entity.FieldTypeFloatVector,
					TypeParams: map[string]string{
						entity.TypeParamDim: "3", // 维度3: [温度, 流量, 转速]
					},
				},
				{
					Name:     "device_code",
					DataType: entity.FieldTypeVarChar,
					TypeParams: map[string]string{
						entity.TypeParamMaxLength: "64",
					},
				},
				{
					Name:     "state", // RUNNING, IDLE, FAULT
					DataType: entity.FieldTypeVarChar,
					TypeParams: map[string]string{
						entity.TypeParamMaxLength: "64",
					},
				},
				{
					Name:     "timestamp",
					DataType: entity.FieldTypeInt64,
				},
			},
		}
		err = MilvusClient.CreateCollection(ctx, schema, entity.DefaultShardNumber)
		if err != nil {
			log.Printf("❌ 创建 Collection 失败: %v", err)
			return
		}

		// 创建索引
		idx, _ := entity.NewIndexIvfFlat(entity.L2, 1024)
		MilvusClient.CreateIndex(ctx, collName, "vector", idx, false)
		MilvusClient.LoadCollection(ctx, collName, false)
		log.Println("✅ Milvus Collection 'sensor_states' 已创建")
	} else {
		log.Println("✅ Milvus Collection 已存在")
		MilvusClient.LoadCollection(ctx, collName, false)
	}
}

// SaveToMilvus 写入向量数据
func SaveToMilvus(device string, vector []float32, state string, ts int64) error {
	if MilvusClient == nil {
		return fmt.Errorf("Milvus 客户端未初始化")
	}

	ctx := context.Background()

	// 构造列数据
	vectorCol := entity.NewColumnFloatVector("vector", 3, [][]float32{vector})
	deviceCol := entity.NewColumnVarChar("device_code", []string{device})
	stateCol := entity.NewColumnVarChar("state", []string{state})
	tsCol := entity.NewColumnInt64("timestamp", []int64{ts})

	_, err := MilvusClient.Insert(ctx, "sensor_states", "", vectorCol, deviceCol, stateCol, tsCol)
	return err
}
