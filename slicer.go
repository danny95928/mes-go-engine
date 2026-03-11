package main

import (
	"math"
	"time"
)

// SliceSummary 定义发给前端的切片统计包
type SliceSummary struct {
	Type      string  `json:"type"`       // 消息类型: "slice"
	ID        int     `json:"id"`         // 切片编号
	StartTime string  `json:"start_time"` // 开始时间
	AvgTemp   float64 `json:"avg_temp"`   // 平均温度
	MaxTemp   float64 `json:"max_temp"`   // 最高温
	MinTemp   float64 `json:"min_temp"`   // 最低温
	Duration  float64 `json:"duration"`   // 持续时长(秒)
}

type Slicer struct {
	CurrentID int
	Buffer    []float64
	StartTime time.Time
	Interval  time.Duration
	OutChan   chan SliceSummary
}

func NewSlicer(interval time.Duration) *Slicer {
	return &Slicer{
		CurrentID: 1,
		Buffer:    make([]float64, 0),
		StartTime: time.Now(),
		Interval:  interval,
		OutChan:   make(chan SliceSummary),
	}
}

// Ingest 接收实时数据，满了就打包发送
func (s *Slicer) Ingest(temp float64) {
	s.Buffer = append(s.Buffer, temp)

	// 如果时间到了，进行切片计算
	if time.Since(s.StartTime) >= s.Interval {
		s.packAndSend()
	}
}

func (s *Slicer) packAndSend() {
	if len(s.Buffer) == 0 {
		s.StartTime = time.Now()
		return
	}

	// 1. Go 高效计算统计信息
	sum, maxVal, minVal := 0.0, -9999.0, 9999.0
	for _, v := range s.Buffer {
		sum += v
		if v > maxVal {
			maxVal = v
		}
		if v < minVal {
			minVal = v
		}
	}
	avg := sum / float64(len(s.Buffer))

	// 2. 构建数据包
	summary := SliceSummary{
		Type:      "slice", // 标记这是切片数据
		ID:        s.CurrentID,
		StartTime: s.StartTime.Format("15:04:05"),
		AvgTemp:   math.Round(avg*100) / 100, // 保留2位小数
		MaxTemp:   maxVal,
		MinTemp:   minVal,
		Duration:  s.Interval.Seconds(),
	}

	// 3. 发送 (非阻塞)
	select {
	case s.OutChan <- summary:
	default:
	}

	// 4. 重置状态
	s.CurrentID++
	s.Buffer = make([]float64, 0)
	s.StartTime = time.Now()
}
