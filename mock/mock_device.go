package main

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/tbrandon/mbserver"
)

// 定义工艺阶段
const (
	StageHeatTo70 = iota
	StageHold70
	StageCoolTo60
	StageHold60
	StageCoolTo30
	StageFinalMix
)

func main() {
	server := mbserver.NewServer()
	// 监听 TCP 端口
	err := server.ListenTCP("127.0.0.1:5020")
	if err != nil {
		log.Fatalf("❌ 无法启动 TCP 监听: %v", err)
	}
	defer server.Close()

	log.Println("⚡ 虚拟反应釜 + 质量流量计 已启动 (Modbus TCP) ⚡")
	log.Println("🌐 监听: 127.0.0.1:5020 | Reg[0]:温度 | Reg[2]:流量")

	// 模拟参数
	const TimeScale = 60.0
	currentTemp := 25.0
	currentFlow := 0.0 // 新增：质量流量 (kg/h)
	currentStage := StageHeatTo70
	stageTimer := 0.0

	tickInterval := 200 * time.Millisecond
	ticker := time.NewTicker(tickInterval)

	for range ticker.C {
		deltaMinutes := (tickInterval.Seconds() * TimeScale) / 60.0
		stageTimer += deltaMinutes
		var stageName string

		// === 1. 温度与流量 状态机逻辑 ===
		switch currentStage {
		case StageHeatTo70:
			stageName = "加热进料"
			// 加热阶段：进料泵开启，流量较大 (约 500 kg/h)
			currentTemp += 10.0 * deltaMinutes
			currentFlow = 500.0 + (rand.Float64()-0.5)*20 // 正常波动
			if currentTemp >= 70.0 {
				currentTemp = 70.0
				currentStage = StageHold70
				stageTimer = 0
			}
		case StageHold70:
			stageName = fmt.Sprintf("70℃保温 (%.1f min)", stageTimer)
			// 保温阶段：泵低速循环，流量小 (约 50 kg/h)
			currentTemp = 70.0 + (rand.Float64()-0.5)*0.4
			currentFlow = 50.0 + (rand.Float64()-0.5)*5
			if stageTimer >= 5.0 {
				currentStage = StageCoolTo60
				stageTimer = 0
			}
		case StageCoolTo60:
			stageName = "冷却加水"
			// 冷却阶段：冷却水循环，流量中等 (约 300 kg/h)
			currentTemp -= 8.0 * deltaMinutes
			currentFlow = 300.0 + (rand.Float64()-0.5)*15
			if currentTemp <= 60.0 {
				currentTemp = 60.0
				currentStage = StageHold60
				stageTimer = 0
			}
		case StageHold60:
			stageName = fmt.Sprintf("60℃保持 (%.1f min)", stageTimer)
			currentTemp = 60.0 + (rand.Float64()-0.5)*0.4
			currentFlow = 50.0 + (rand.Float64()-0.5)*5
			if stageTimer >= 30.0 {
				currentStage = StageCoolTo30
				stageTimer = 0
			}
		case StageCoolTo30:
			stageName = "急速冷排"
			currentTemp -= 5.0 * deltaMinutes
			currentFlow = 600.0 + (rand.Float64()-0.5)*30 // 排料，大流量
			if currentTemp <= 30.0 {
				currentTemp = 30.0
				currentStage = StageFinalMix
				stageTimer = 0
			}
		case StageFinalMix:
			stageName = "最终混合"
			currentTemp = 30.0 + (rand.Float64()-0.5)*0.4
			currentFlow = 0.0 // 停止流动
		}

		// === 2. 模拟异常注入 (用于测试 AI 报警) ===
		// 1% 概率触发流量计异常 (模拟堵塞或泄漏)
		if rand.Float32() < 0.01 {
			log.Println("⚠️ [模拟故障] 注入流量异常数据!")
			currentFlow = 1200.0 // 瞬间激增 (爆管模拟)
		}

		// === 3. 写入寄存器 ===
		// Reg[0]: 温度 * 10
		server.HoldingRegisters[0] = uint16(currentTemp * 10)

		// Reg[2]: 流量 * 10 (Modbus一个寄存器最大65535，这里足够存 1200.0 * 10)
		server.HoldingRegisters[2] = uint16(currentFlow * 10)

		if rand.Float32() < 0.05 {
			log.Printf("🔥 [%s] Temp=%.1f | Flow=%.1f kg/h", stageName, currentTemp, currentFlow)
		}
	}
}
